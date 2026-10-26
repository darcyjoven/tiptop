// Package schemacli 提供 `tiptop schema import` / `tiptop schema status` 子命令。
//
// 为什么要有它：bdl-lsp 需要知道"某张表有哪些字段、字段中文说明是什么"，
// 才能在编辑器里做字段提示/跳转。这份资料来自 DBA 从 Oracle 导出的
// "表字段"CSV，属于低频、成批的转换动作，适合放在终端跑一次，编辑器侧
// 靠文件 mtime 自动热加载——与 link 子命令同一个理由。
//
// 输出格式（bdl-lsp 按同一约定读取，改动必须两边同步）：
//
//	<out>/tables.json            索引（schema/importedAt/source/rowsTotal/tables）
//	<out>/tables/<大写表名>.json  每张表的列清单
//
// 数据目录默认 <项目根>/.project/.schema，与 link 的 .project/links.jsonl 同级。
package schemacli

import (
	"flag"
	"fmt"
	"github.com/darcyjoven/tiptop/internal/progress"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Run 执行 `tiptop schema <子命令> [参数]`。
//
// 退出码：0 成功；1 业务失败（CSV 读不了/索引损坏/没有资料）；2 用法错误。
//
// version 参数保留是为了与 `link`/`deploy` 的调用方式一致；schema 资料里
// 不记录工具版本（格式由 bdl-lsp 与 tiptop 共同约定，见 SchemaVersion）。
func Run(args []string, version string) int {
	return run(args, os.Stdout, os.Stderr)
}

func run(args []string, stdout, stderr io.Writer) int {
	sub := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "import":
		return runImport(args, stdout, stderr)
	case "sync":
		return runSync(args, stdout, stderr)
	case "status":
		return runStatus(args, stdout, stderr)
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "tiptop schema: 未知子命令 %q\n", sub)
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `用法：
  tiptop schema import -csv <导出.csv> [-root <项目根>] [-out <资料目录>]
                       [-tables a,b,c] [-only-new] [-dry-run]
  tiptop schema sync [-source golang|file] [-root <项目根>] [-out <资料目录>]
                     [-host H -port 1521 -service S -user U [-owner O]] [-query <SQL>]
                     [-tables a,b,c] [-only-new] [-prune] [-dry-run]
  tiptop schema status [-root <项目根>] [-out <资料目录>]

import 把 Oracle 导出的"表字段"CSV 转成 <项目根>/.project/.schema 下的资料：
       tables.json（索引）+ tables/<大写表名>.json（每张表的列清单），
       供 bdl-lsp 做字段说明与提示。
sync   直连 Oracle 查 ALL_TAB_COLUMNS + ALL_COL_COMMENTS（source=golang，默认），
       或读一份已经导出的 CSV（source=file）。结果与 import 完全一致。
       密码取环境变量 BDL_ORA_PASSWORD 或 <项目>/.project/secrets.json。
status 查看已有资料的统计（表数/列数/导入时间/来源）。

选项：
  -csv        Oracle 导出的表字段 CSV（import 必填）
  -file       source=file 时的 CSV（默认 <项目>/.project/gal_schema.csv）
  -host -port -service -user   Oracle 连接（source=golang）
  -owner      要采集的应用 schema（默认 -user 的大写）
  -query      自定义 SQL（默认见上；列名与导出 CSV 一致最省事）
  -root       项目根目录（默认：当前目录）
  -out        资料目录（默认 <root>/.project/schema；旧 .project/.schema 只读兼容）
  -tables     只同步这些表，逗号分隔（大小写不敏感；其余表原样保留）
  -tables-file 同 -tables，但从文件读（一行一个；表多时用，避免命令行过长）
  -only-new   只写本地不存在的表，已有表文件一个字节都不动
  -prune      删除本地有、本次没出现的表（只与全量一起用）
  -dry-run    只采集/解析与统计，不写盘

说明：默认全量——采集到的表重算，没出现的表保留不删（加 -prune 才删）；
      -tables 与 -only-new 可同时给出：只在给定表里挑"本地不存在的"。
`)
}

// runImport 解析参数、执行导入并打印中文报告。
func runImport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("schema import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	csvPath := fs.String("csv", "", "Oracle 导出的表字段 CSV（必填）")
	root := fs.String("root", "", "项目根目录（默认当前目录）")
	out := fs.String("out", "", "资料目录（默认 <root>/.project/schema）")
	tables := fs.String("tables", "", "只导入这些表，逗号分隔（大小写不敏感）")
	tablesFile := fs.String("tables-file", "", "只导入这些表（文件里一行一个，供插件传大列表）")
	onlyNew := fs.Bool("only-new", false, "只写本地不存在的表，已有表文件不动")
	dryRun := fs.Bool("dry-run", false, "只解析与统计，不写盘")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*csvPath) == "" {
		fmt.Fprintln(stderr, "tiptop schema import: 必须指定 -csv")
		return 2
	}

	projectRoot, err := resolveRoot(*root)
	if err != nil {
		fmt.Fprintf(stderr, "tiptop schema import: %v\n", err)
		return 1
	}
	dataDir, err := resolveDataDir(projectRoot, *out)
	if err != nil {
		fmt.Fprintf(stderr, "tiptop schema import: %v\n", err)
		return 1
	}
	only, err := mergeTables(*tables, *tablesFile)
	if err != nil {
		fmt.Fprintf(stderr, "tiptop schema import: %v\n", err)
		return 1
	}

	start := time.Now()
	rep := progress.New(stderr)
	rep.Note("采集", fmt.Sprintf("读取 %s", filepath.Base(*csvPath)))
	res, err := Import(ImportOptions{
		CSVPath:    *csvPath,
		DataDir:    dataDir,
		OnlyTables: only,
		OnlyNew:    *onlyNew,
		DryRun:     *dryRun,
		Now:        start,
		Source:     filepath.Base(*csvPath),
		Progress:   progressFn(rep),
	})
	if err != nil {
		fmt.Fprintf(stderr, "tiptop schema import: %v\n", err)
		return 1
	}

	// 跳过的行写到 stderr：stdout 的报告保持规约约定的形状，给插件显示。
	reportProblems(stderr, res.Problems)
	rep.Note("采集完成", fmt.Sprintf("%d 行", res.RowsTotal))
	if !*dryRun {
		rep.Note("写入完成", fmt.Sprintf("%d 个表文件", res.Written))
	}
	printImportReport(stdout, "schema 导入", res, res.Source, 0, start, *dryRun)
	return 0
}

// progressFn 把 ImportOptions.Progress 转成进度行（按需节流，避免几千张表刷屏）。
func progressFn(rep *progress.Reporter) func(phase, detail string, i, n int) {
	if rep == nil {
		return nil
	}
	return func(phase, detail string, i, n int) {
		if progress.Every(i, n, 200) {
			rep.Step(phase, detail, i, n)
		}
	}
}

// mergeTables 合并 `-tables`（逗号分隔）与 `-tables-file`（一行一个）。
//
// 文件形式是为了绕开命令行长度上限：插件在"筛选出的表很多"时会走这条路。
func mergeTables(list, file string) ([]string, error) {
	out := upperList(splitList(list))
	if strings.TrimSpace(file) == "" {
		return out, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("读取 -tables-file: %w", err)
	}
	seen := map[string]bool{}
	for _, t := range out {
		seen[t] = true
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		up := strings.ToUpper(line)
		if !seen[up] {
			seen[up] = true
			out = append(out, up)
		}
	}
	return out, nil
}

// printImportReport 打印导入/同步的共同报告。
//
// 形状对插件是契约（它按行解析"受影响表/新增/变更"这类信息），
// 所以 import 与 sync 用同一个函数，避免两处漂移。
func printImportReport(w io.Writer, label string, res *ImportResult, source string, queriedRows int, start time.Time, dryRun bool) {
	tablesTotal := res.TablesNew + res.TablesChanged + res.TablesSame
	fmt.Fprintf(w, "== %s ==\n", label)
	if source != "" {
		fmt.Fprintf(w, "来源            %s\n", source)
	}
	if queriedRows > 0 {
		fmt.Fprintf(w, "查询行数        %d\n", queriedRows)
	}
	fmt.Fprintf(w, "CSV 行数        %d\n", res.RowsTotal)
	extra := ""
	if res.TablesPruned > 0 {
		extra = fmt.Sprintf(" / 删除 %d", res.TablesPruned)
	}
	fmt.Fprintf(w, "表              %d（新增 %d / 变更 %d / 未变 %d / 保留 %d%s）\n",
		tablesTotal, res.TablesNew, res.TablesChanged, res.TablesSame, res.TablesKept, extra)
	fmt.Fprintf(w, "列              %d\n", res.Columns)
	fmt.Fprintf(w, "数据目录        %s\n", res.DataDir)
	fmt.Fprintf(w, "耗时            %s\n", time.Since(start).Round(time.Millisecond))
	if res.TablesKept > 0 && res.TablesPruned == 0 {
		fmt.Fprintf(w, "（本地多出 %d 张表未删除：全量同步加 -prune 可清理）\n", res.TablesKept)
	}
	if dryRun {
		fmt.Fprintln(w, "（dry-run：未写盘）")
	}
}

// runStatus 打印已有 schema 资料的统计。
func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("schema status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "项目根目录（默认当前目录）")
	out := fs.String("out", "", "资料目录（默认 <root>/.project/schema）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	projectRoot, err := resolveRoot(*root)
	if err != nil {
		fmt.Fprintf(stderr, "tiptop schema status: %v\n", err)
		return 1
	}
	dataDir, err := resolveDataDir(projectRoot, *out)
	if err != nil {
		fmt.Fprintf(stderr, "tiptop schema status: %v\n", err)
		return 1
	}

	idx, exists, err := loadIndex(dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "tiptop schema status: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "== schema 资料 ==\n")
	fmt.Fprintf(stdout, "数据目录        %s\n", dataDir)
	if !exists {
		fmt.Fprintf(stdout, "状态            没有资料\n")
		fmt.Fprintf(stdout, "提示            先运行：tiptop schema import -csv <导出.csv>\n")
		return 1
	}
	columns := 0
	for _, e := range idx.Tables {
		columns += e.Columns
	}
	importedAt := idx.ImportedAt
	if importedAt == "" {
		importedAt = "未知"
	}
	fmt.Fprintf(stdout, "表              %d\n", len(idx.Tables))
	fmt.Fprintf(stdout, "列              %d\n", columns)
	fmt.Fprintf(stdout, "导入时间        %s\n", importedAt)
	fmt.Fprintf(stdout, "来源            %s\n", idx.Source)
	return 0
}

// resolveRoot 取项目根：给了 -root 用它，否则用当前目录，都转成绝对路径。
func resolveRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("取当前目录失败: %w", err)
		}
		root = cwd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("解析项目根 %s: %w", root, err)
	}
	return abs, nil
}

// resolveDataDir 解析资料目录：默认 <root>/.project/schema（W6）；
// -out 给相对路径时按项目根解析（与 link 子命令一致）。
//
// 未显式给 -out 时按"新布局 → 旧布局（.project/.schema）"挑：已经有一次
// 旧布局资料的工程不会被写到新目录、造成两份资料分叉；迁移命令可整体搬家。
func resolveDataDir(root, out string) (string, error) {
	if strings.TrimSpace(out) == "" {
		return resolveDefaultDataDir(root), nil
	}
	if filepath.IsAbs(out) {
		return filepath.Clean(out), nil
	}
	abs, err := filepath.Abs(filepath.Join(root, out))
	if err != nil {
		return "", fmt.Errorf("解析资料目录 %s: %w", out, err)
	}
	return abs, nil
}

// resolveDefaultDataDir 选择默认字段资料目录：新布局优先，回退旧布局（只读兼容）。
// 都没有时返回新布局路径（首次导入写这里）。
func resolveDefaultDataDir(root string) string {
	newDir := filepath.Join(root, DefaultDataDir, DefaultSchemaDir)
	if hasIndex(newDir) {
		return newDir
	}
	legacy := filepath.Join(root, DefaultDataDir, LegacySchemaDir)
	if hasIndex(legacy) {
		return legacy
	}
	return newDir
}

// hasIndex 报告目录下是否有 tables.json。
func hasIndex(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, IndexFileName))
	return err == nil
}

// reportProblems 把跳过行等警告写到 stderr（最多 10 条，其余折叠计数）。
func reportProblems(stderr io.Writer, problems []string) {
	if len(problems) == 0 {
		return
	}
	show := problems
	if len(show) > 10 {
		show = show[:10]
	}
	for _, p := range show {
		fmt.Fprintf(stderr, "tiptop schema import: %s\n", p)
	}
	if n := len(problems) - len(show); n > 0 {
		fmt.Fprintf(stderr, "tiptop schema import: …另有 %d 行同类问题已跳过（未中断导入）\n", n)
	}
}

// splitList 按逗号切分，去掉空白项。
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// upperList 把表名统一成大写，便于大小写不敏感地比较。
func upperList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, strings.ToUpper(v))
	}
	return out
}
