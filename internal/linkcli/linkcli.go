// `tiptop link` 子命令：导入与查看程序链接资料（P7-D）。
//
// 为什么做成子命令而不是编辑器里的按钮：导入要读几千个文件、跑十几秒，
// 属于"一次性/低频"动作，放进编辑器 UI 只会卡界面。用户在终端跑一次，
// 编辑器侧靠 mtime 自动热加载。
package linkcli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/darcyjoven/bdl-lsp/link"
	"github.com/darcyjoven/bdl-lsp/parser"
	"github.com/darcyjoven/tiptop/internal/linksync"
)

// Run 执行 `tiptop link <子命令> [参数]`。
//
// 退出码：0 成功；1 业务失败（没有资料/导入失败）；2 用法错误。
func Run(args []string, version string) int {
	sub := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "import":
		return runImport(args, version)
	case "sync":
		return runSync(args, version)
	case "status":
		return runStatus(args)
	case "help", "--help", "-h":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "tiptop link: 未知子命令 %q\n", sub)
		usage()
		return 2
	}
}

func usage() {
	fmt.Print(`用法：
  tiptop link import -csv <导出.csv> [-root <项目根>] [-out <资料目录>]
  tiptop link sync [-source golang|command|file] [-root <项目根>] [-out <资料目录>]
  tiptop link status [-root <项目根>] [-out <资料目录>]

  # 局部更新（只重算受影响的程序，其余链保留）
  tiptop link sync   -modules lib,sub,qry
  tiptop link sync   -files aap/4gl/saapt310.4gl
  tiptop link import -csv gal.csv -files aap/4gl/saapt310.4gl
  # 想先看"会影响几个作业"：加 -dry-run（不写盘）

import 把 DS.GAL_FILE 的导出转成 <项目根>/.project/links.jsonl。
       需要读源码判断哪一条是 MAIN：全量约 10–20 秒，局部（-modules/-files）亚秒级。
sync   直接从数据库/命令/文件采集并导入（P7-D-3）。golang 来源用纯 Go 驱动
       直连 Oracle，密码取环境变量 BDL_ORA_PASSWORD 或 .project/secrets.json。
status 查看已有资料的统计（不解析源码，秒回）。

选项：
  -root       项目根目录（默认：当前目录）
  -csv        导出 CSV 路径（import 必填）
  -out        资料目录（默认 <root>/.project）
  -modules    只更新这些模组（gal02），逗号分隔
  -files      只更新这些 4gl，逗号分隔；形如 aap/4gl/saapt310.4gl / aap/saapt310 / saapt310.4gl
  -roots      模组根，逗号分隔（默认自动探测）
  -global     全局可见模组，逗号分隔（默认 sub,lib,qry，仅 status 显示用）
  -replace    覆盖已有资料（默认就是这样；保留此开关是为了将来做"合并"）
  -source     采集来源：golang（默认）/ command / file
  -command    采集命令（source=command）
  -file       已导出的 CSV 路径（source=file，默认 <out>/gal_file.csv）
  -host -port -service -user -query   Oracle 连接（source=golang）
  -dry-run    只采集与校验，不写盘（局部更新会打印"受影响作业"条数，便于先确认）
`)
}

// reportScanProgress 把源码扫描进度写到 stderr（stdout 留给结果）。
//
// 每 200 个文件或 10% 打一行：7200 个文件约 36 行，既能看到在动，
// 又不会把 CI 日志刷爆。回调可能从多个 worker 并发进入，这里加锁。
func reportScanProgress(done, total int) {
	progressMu.Lock()
	defer progressMu.Unlock()
	if total <= 0 {
		return
	}
	if done == 0 {
		fmt.Fprintf(os.Stderr, "扫描源码 0/%d（需要解析全部 .4gl，首次通常 20–30 秒）…\n", total)
		return
	}
	// 每 400 个文件一行：7600 个文件约 19 行，既能看到在动，又不刷屏。
	if done == total || done%400 == 0 {
		fmt.Fprintf(os.Stderr, "扫描源码 %d/%d（%d%%）\n", done, total, done*100/total)
	}
}

var progressMu sync.Mutex

// runSync 采集 + 导入（P7-D-3）。
func runSync(args []string, version string) int {
	fs := flag.NewFlagSet("link sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "项目根目录（默认当前目录）")
	out := fs.String("out", "", "资料目录（默认 <root>/.link）")
	roots := fs.String("roots", "", "模组根，逗号分隔")
	source := fs.String("source", "golang", "采集来源：golang/command/file")
	command := fs.String("command", "", "采集命令（source=command）")
	file := fs.String("file", "", "已导出的 CSV（source=file）")
	host := fs.String("host", "", "Oracle 主机（source=golang）")
	port := fs.Int("port", 1521, "Oracle 端口")
	service := fs.String("service", "", "Oracle 服务名")
	user := fs.String("user", "", "Oracle 用户")
	query := fs.String("query", "", "自定义 SQL（默认 DS.GAL_FILE 四列）")
	modules := fs.String("modules", "", "只更新这些模组，逗号分隔（局部更新，P1-3）")
	files := fs.String("files", "", "只更新这些 4gl，逗号分隔，如 aap/4gl/saapt310.4gl（局部更新）")
	dryRun := fs.Bool("dry-run", false, "只采集与校验，不写盘（局部更新时会打印将影响几个作业）")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	projectRoot := *root
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}
	projectRoot, _ = filepath.Abs(projectRoot)
	dataDir := *out
	if dataDir == "" {
		dataDir = filepath.Join(projectRoot, link.DefaultDataDir)
	} else if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(projectRoot, dataDir)
	}

	syncCfg := linksync.Config{
		Enable:      true,
		Source:      *source,
		Command:     *command,
		File:        *file,
		OnlyModules: splitList(*modules),
		OnlySpecs:   splitList(*files),
		DB: linksync.DBConfig{
			Host: *host, Port: *port, Service: *service, User: *user, Query: *query,
		},
	}
	pool, err := parser.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tiptop link sync: 初始化解析器: %v\n", err)
		return 2
	}
	defer pool.Close()

	start := time.Now()
	fmt.Fprintf(os.Stderr, "正在采集（%s）…\n", *source)
	res, err := linksync.Run(context.Background(), linksync.Request{
		Config:      syncCfg,
		ProjectRoot: projectRoot,
		DataDir:     dataDir,
		ModuleRoots: splitList(*roots),
		Pool:        pool,
		ToolVersion: version,
		Modules:     splitList(*modules),
		Specs:       splitList(*files),
		Write:       !*dryRun,
		Progress:    reportScanProgress,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tiptop link sync: %v\n", err)
		return 1
	}
	fmt.Printf("== 采集完成（%s）==\n", res.Stat.Source)
	fmt.Printf("来源        %s\n", res.Stat.Detail)
	var sb strings.Builder
	switch {
	case res.Partial != nil:
		res.Partial.Report(&sb)
	case res.Outcome != nil:
		res.Outcome.Report(&sb)
	}
	fmt.Print(sb.String())
	fmt.Printf("总耗时      %s\n", time.Since(start).Round(time.Millisecond))
	if *dryRun {
		if res.Partial != nil {
			fmt.Println("\n（dry-run：未写盘。确认上面\"受影响作业\"的条数后再去掉 -dry-run 执行）")
		} else {
			fmt.Println("\n（dry-run：未写盘）")
		}
	} else {
		fmt.Printf("已写入 %s\n", dataDirOf(res))
	}
	return 0
}

func runImport(args []string, version string) int {
	fs := flag.NewFlagSet("link import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	csvPath := fs.String("csv", "", "DS.GAL_FILE 导出的 CSV（必填）")
	root := fs.String("root", "", "项目根目录（默认当前目录）")
	out := fs.String("out", "", "资料目录（默认 <root>/.project）")
	roots := fs.String("roots", "", "模组根，逗号分隔")
	modules := fs.String("modules", "", "只更新这些模组，逗号分隔（局部更新，P1-3）")
	files := fs.String("files", "", "只更新这些 4gl，逗号分隔，如 aap/4gl/saapt310.4gl（局部更新）")
	dryRun := fs.Bool("dry-run", false, "只解析与统计，不写盘（局部更新时会打印将影响几个作业）")
	fs.Bool("replace", true, "覆盖已有资料")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *csvPath == "" {
		fmt.Fprintln(os.Stderr, "tiptop link import: 必须指定 -csv")
		return 2
	}
	projectRoot := *root
	if projectRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "tiptop link import: 取当前目录失败: %v\n", err)
			return 1
		}
		projectRoot = cwd
	}

	pool, err := parser.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tiptop link import: 初始化解析器失败: %v\n", err)
		return 1
	}
	defer pool.Close()

	mods, specs := splitList(*modules), splitList(*files)
	if len(mods) > 0 || len(specs) > 0 {
		// 局部更新：只重算受影响的程序，其余链保留（P1-3）。
		part, err := link.RunImportPartial(link.PartialImportRequest{
			CSVPath:      *csvPath,
			ProjectRoot:  projectRoot,
			DataDir:      *out,
			ModuleRoots:  splitList(*roots),
			Pool:         pool,
			Modules:      mods,
			Specs:        specs,
			Source:       "file",
			SourceDetail: *csvPath,
			ToolVersion:  version,
			Write:        !*dryRun,
			Now:          time.Now(),
			Progress:     reportScanProgress,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "tiptop link import: %v\n", err)
			return 1
		}
		var sb strings.Builder
		part.Report(&sb)
		fmt.Print(sb.String())
		if *dryRun {
			fmt.Println("\n（dry-run：未写盘。确认上面\"受影响作业\"的条数后再去掉 -dry-run 执行）")
		} else {
			fmt.Printf("\n已写入 %s\n", filepath.Join(part.DataDir, link.LinksFileName))
			fmt.Println("编辑器里无需重启：打开文件时会自动发现资料变更并重载。")
		}
		return 0
	}

	res, err := link.RunImport(link.ImportRequest{
		CSVPath:      *csvPath,
		ProjectRoot:  projectRoot,
		DataDir:      *out,
		ModuleRoots:  splitList(*roots),
		Pool:         pool,
		Source:       "file",
		SourceDetail: *csvPath,
		ToolVersion:  version,
		Write:        !*dryRun,
		Now:          time.Now(),
		Progress:     reportScanProgress,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tiptop link import: %v\n", err)
		return 1
	}
	var sb strings.Builder
	res.Report(&sb)
	fmt.Print(sb.String())
	if *dryRun {
		fmt.Println("\n（dry-run：未写盘）")
		return 0
	}
	fmt.Printf("\n已写入 %s\n", filepath.Join(res.DataDir, link.LinksFileName))
	fmt.Println("编辑器里无需重启：打开文件时会自动发现资料变更并重载。")
	return 0
}

func runStatus(args []string) int {
	fs := flag.NewFlagSet("link status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "项目根目录（默认当前目录）")
	out := fs.String("out", "", "资料目录（默认 <root>/.link）")
	global := fs.String("global", "", "全局可见模组，逗号分隔（默认 sub,lib,qry）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	projectRoot := *root
	if projectRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "tiptop link status: 取当前目录失败: %v\n", err)
			return 1
		}
		projectRoot = cwd
	}
	dir := *out
	if dir == "" {
		dir = filepath.Join(projectRoot, link.DefaultDataDir)
	}
	modules := splitList(*global)
	if len(modules) == 0 {
		modules = link.DefaultGlobalModules()
	}

	st := link.LoadStatus(dir, modules)
	fmt.Printf("== 链接资料 ==\n")
	fmt.Printf("目录        %s\n", st.DataDir)
	if !st.Present {
		if st.Error != "" {
			fmt.Printf("状态        读取失败：%s\n", st.Error)
			return 1
		}
		fmt.Printf("状态        没有资料\n")
		fmt.Println("\n先运行：tiptop link import -csv <导出.csv>")
		return 1
	}
	fmt.Printf("链          %d\n", st.Stats.Chains)
	fmt.Printf("orphan      %d\n", st.Stats.Orphans)
	fmt.Printf("ambiguous   %d\n", st.Stats.Ambiguous)
	fmt.Printf("不同文件    %d\n", st.Stats.Files)
	if m := st.Meta; m != nil {
		fmt.Printf("生成时间    %s（%d 天前）\n", m.GeneratedAt, int(st.Age.Hours()/24))
		fmt.Printf("来源        %s %s\n", m.Source, m.SourceDetail)
		fmt.Printf("缺失文件    %d（链接里有、磁盘上没有，完全忽略）\n", m.MissingFiles)
		if len(m.ResolvedFrom) > 0 {
			fmt.Printf("模组根      %s\n", strings.Join(m.ResolvedFrom, ", "))
		}
		fmt.Printf("导入统计    行 %d / 过滤 %d / 有效 %d\n", m.RowsTotal, m.RowsFilteredN, m.RowsY)
		if p := m.Partial; p != nil {
			fmt.Printf("局部更新    %s：作业 %d 个 / 重算链 %d 条", p.UpdatedAt, p.Programs, p.ChainsUpdated)
			if len(p.Modules) > 0 {
				fmt.Printf(" / 模组 %s", strings.Join(p.Modules, ", "))
			}
			if len(p.Specs) > 0 {
				fmt.Printf(" / 文件 %s", strings.Join(p.Specs, ", "))
			}
			fmt.Println()
			fmt.Println("            （其余链仍是上一次全量导入的结果）")
		}
		switch {
		case m.ExternalsSkipped:
			fmt.Printf("外部函数    候选 %d 个，**超过上限，未启用**（资料可能不完整）\n", m.ExternalsTotal)
		case m.ExternalsTotal > 0:
			fmt.Printf("外部函数    %d 个（被 ≥%d 个文件或 ≥%d 次调用、全语料无定义，视为存在）\n",
				m.ExternalsTotal, m.ExternalsMinFiles, m.ExternalsMinCalls)
			show := m.Externals
			if len(show) > 5 {
				show = show[:5]
			}
			parts := make([]string, 0, len(show))
			for _, e := range show {
				parts = append(parts, fmt.Sprintf("%s(%d)", e.Name, e.Files))
			}
			fmt.Printf("            %s%s\n", strings.Join(parts, " "),
				map[bool]string{true: " …", false: ""}[m.ExternalsTotal > len(show)])
		}
	} else {
		fmt.Printf("生成时间    未知（缺 %s）\n", link.MetaFileName)
	}
	fmt.Printf("全局可见    %s\n", strings.Join(modules, ", "))
	return 0
}

// dataDirOf 取本次结果实际写入的资料目录（全量与局部两种结果形态）。
func dataDirOf(res *linksync.Result) string {
	switch {
	case res == nil:
		return ""
	case res.Partial != nil:
		return res.Partial.DataDir
	case res.Outcome != nil:
		return res.Outcome.DataDir
	default:
		return ""
	}
}

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
