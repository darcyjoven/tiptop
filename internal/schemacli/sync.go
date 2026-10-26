package schemacli

// `tiptop schema sync`：直连 Oracle 查表字段（或读一份 CSV），走与
// `schema import` **完全同一条**合并/写盘逻辑。
//
// 为什么把两种来源放在一个子命令里：结果必须完全一致（同样的索引结构、
// 同样的增量语义、同样的报告），差别只在"CSV 字节从哪来"。
// 数据库那次查询的列名/顺序刻意与 DBA 导出的 CSV 一致，因此下游解析不用分叉。

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/darcyjoven/tiptop/internal/oradb"
	"github.com/darcyjoven/tiptop/internal/progress"
)

// DefaultSchemaQuery 是直连 Oracle 查表字段的默认 SQL（TiTop 专用，2026-10-26 确认）。
//
// 说明列**不取 Oracle 备注**（`ALL_COL_COMMENTS` 在用户环境基本没维护），而是取
// TiTop 字典 `DS.GAQ_FILE.gaq03`（`gaq02='2'` = 栏位说明）；表与列来自
// `DS.ZTA_FILE`（`zta02='ds'`，固定）+ `USER_TAB_COLUMNS`。因此连接账号必须是
// **应用 schema 账号**（自己拥有这些表，且对 `DS.ZTA_FILE`/`DS.GAQ_FILE` 有读权限）。
//
// 列名/顺序与"Oracle 导出 CSV"一致（含 `default_length`）：
//
//	table_name, column_name, column_desc(中文说明), data_type,
//	data_length, char_length, data_precision, data_scale, nullable, default_length
//
// tables 非空时追加 `zta01 IN (...)`（分批用；zta01 是 ZTA_FILE 里的原始写法）。
func DefaultSchemaQuery(tables []string) string {
	var sb strings.Builder
	sb.WriteString(`SELECT zta01 AS table_name, LOWER(column_name) AS column_name, NVL(gaq03, '') AS column_desc, `)
	sb.WriteString(`data_type, data_length, char_length, data_precision, data_scale, nullable, default_length `)
	sb.WriteString(`FROM ds.zta_file, user_tab_columns `)
	sb.WriteString(`LEFT JOIN ds.gaq_file ON column_name = UPPER(gaq01) AND gaq02 = '2' `)
	sb.WriteString(`WHERE zta02 = 'ds' AND table_name = UPPER(zta01)`)
	if len(tables) > 0 {
		quoted := make([]string, 0, len(tables))
		for _, t := range tables {
			if t = strings.TrimSpace(t); t != "" {
				quoted = append(quoted, quoteSQL(t))
			}
		}
		if len(quoted) > 0 {
			fmt.Fprintf(&sb, " AND zta01 IN (%s)", strings.Join(quoted, ", "))
		}
	}
	sb.WriteString(" ORDER BY table_name, column_name")
	return sb.String()
}

// DefaultTableListQuery 取 TiTop 字典里 `zta02='ds'` 的表清单（分批用）。
//
// 与明细查询同源于 DS.ZTA_FILE，避免分批后漏表/多表；返回 zta01 的原始写法
// （TiTop 里通常是小写），批次条件直接用它。
func DefaultTableListQuery() string {
	return "SELECT DISTINCT zta01 FROM ds.zta_file WHERE zta02 = 'ds' ORDER BY zta01"
}

// dictMissingHint 把"TiTop 字典表不存在/无读权限"翻译成可执行的提示。
func dictMissingHint(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "ORA-00942") {
		return "（TiTop 字典表 DS.ZTA_FILE / DS.GAQ_FILE 不存在或当前账号无读权限：请改用 CSV 导入 tiptop schema import -csv …）"
	}
	return ""
}

// chunkStrings 把表清单按 size 切片；size<=0 或数量不超过一批时返回单个分片。
func chunkStrings(list []string, size int) [][]string {
	if len(list) == 0 {
		return nil
	}
	if size <= 0 || len(list) <= size {
		return [][]string{list}
	}
	out := make([][]string, 0, (len(list)+size-1)/size)
	for i := 0; i < len(list); i += size {
		end := i + size
		if end > len(list) {
			end = len(list)
		}
		out = append(out, list[i:end])
	}
	return out
}

// mergeCSVParts 合并多批 CSV：只保留第一份的表头，后续批次跳过表头行。
func mergeCSVParts(parts [][]byte) []byte {
	if len(parts) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for i, p := range parts {
		if i == 0 {
			buf.Write(p)
			continue
		}
		if idx := bytes.IndexByte(p, '\n'); idx >= 0 {
			buf.Write(p[idx+1:])
		}
	}
	return buf.Bytes()
}

// quoteSQL 把标识符写成 Oracle 字符串字面量（转义单引号，避免拼坏 SQL）。
func quoteSQL(s string) string {
	return "'" + strings.ReplaceAll(strings.TrimSpace(s), "'", "''") + "'"
}

// runSync 解析参数并执行"直连数据库 / 读文件"的同步。
func runSync(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("schema sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := fs.String("source", "golang", "来源：golang（直连 Oracle）/ file（读 CSV）")
	file := fs.String("file", "", "source=file 时的 CSV（默认 <资料目录>/gal_schema.csv）")
	host := fs.String("host", "", "Oracle 主机（source=golang）")
	port := fs.Int("port", 1521, "Oracle 端口")
	service := fs.String("service", "", "Oracle 服务名")
	user := fs.String("user", "", "Oracle 只读账号（也用作默认 owner 的大写）")
	owner := fs.String("owner", "", "要采集的应用 schema（默认 -user 的大写）")
	query := fs.String("query", "", "自定义 SQL（默认走 TiTop 字典：DS.ZTA_FILE + DS.GAQ_FILE + USER_TAB_COLUMNS）")
	root := fs.String("root", "", "项目根目录（默认当前目录）")
	out := fs.String("out", "", "资料目录（默认 <root>/.project/schema）")
	tables := fs.String("tables", "", "只同步这些表，逗号分隔（大小写不敏感；其余表原样保留）")
	tablesFile := fs.String("tables-file", "", "只同步这些表（文件里一行一个，供插件传大列表）")
	onlyNew := fs.Bool("only-new", false, "只写本地不存在的表，已有表文件不动")
	prune := fs.Bool("prune", false, "删除本地有、本次没出现的表（只能与全量一起用）")
	dryRun := fs.Bool("dry-run", false, "只采集与统计，不写盘")
	batchSize := fs.Int("batch-size", 200, "golang 全量查询每批 N 张表（0=不分批）")
	timeoutSec := fs.Int("timeout", 120, "单批查询超时（秒）")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	projectRoot, err := resolveRoot(*root)
	if err != nil {
		fmt.Fprintf(stderr, "tiptop schema sync: %v\n", err)
		return 1
	}
	dataDir, err := resolveDataDir(projectRoot, *out)
	if err != nil {
		fmt.Fprintf(stderr, "tiptop schema sync: %v\n", err)
		return 1
	}
	only, err := mergeTables(*tables, *tablesFile)
	if err != nil {
		fmt.Fprintf(stderr, "tiptop schema sync: %v\n", err)
		return 1
	}

	start := time.Now()
	rep := progress.New(stderr)
	var (
		data   []byte
		src    string
		detail string
		rows   int
	)
	switch strings.ToLower(strings.TrimSpace(*source)) {
	case "", "golang", "db":
		if strings.TrimSpace(*owner) != "" && strings.TrimSpace(*query) == "" {
			fmt.Fprintln(stderr, "tiptop schema sync: 提示：默认 SQL 走 USER_TAB_COLUMNS + TiTop 字典（DS.ZTA_FILE/DS.GAQ_FILE），不再使用 -owner；如需按 owner 采通用库请用 -query")
		}
		if strings.TrimSpace(*user) == "" {
			fmt.Fprintln(stderr, "tiptop schema sync: source=golang 时必须有 -user（应用 schema 账号）")
			return 2
		}
		// secrets.json 只在**项目资料根**（.project），不是链接资料目录（.project/link）。
		secretsDir := filepath.Join(projectRoot, DefaultDataDir)
		cfg := oradb.Config{Host: *host, Port: *port, Service: *service, User: *user}
		// 密码不写进配置：走 oradb 的优先级（配置内 ${env:} → 环境变量 → secrets.json）。
		cfg.Password = ""
		batchTimeout := time.Duration(*timeoutSec) * time.Second
		if *timeoutSec <= 0 {
			batchTimeout = 120 * time.Second
		}
		onRows := func(n int) { rep.Note("采集", fmt.Sprintf("已收到 %d 行", n)) }
		rep.Note("采集", fmt.Sprintf("连接 %s:%d/%s（user=%s；TiTop 字典 ds.zta_file/ds.gaq_file）…",
			*host, portOrDefault(*port), *service, *user))

		ctx := context.Background()
		q := strings.TrimSpace(*query)
		switch {
		case q != "":
			// 自定义 SQL：无法安全地拼批次条件，保持单条查询（超时可加大 -timeout）。
			cfg.Query = q
			data, rows, err = oradb.QueryCSV(ctx, cfg, secretsDir, batchTimeout, onRows)
			if err != nil {
				fmt.Fprintf(stderr, "tiptop schema sync: %v\n", err)
				return 1
			}
			src = fmt.Sprintf("golang(自定义SQL)@%s:%d/%s", *host, portOrDefault(*port), *service)
		default:
			tables := only
			if len(tables) == 0 {
				rep.Note("采集", "读取表清单（DS.ZTA_FILE，zta02='ds'）…")
				cfg.Query = DefaultTableListQuery()
				list, lerr := oradb.QueryStrings(ctx, cfg, secretsDir, batchTimeout)
				if lerr != nil {
					fmt.Fprintf(stderr, "tiptop schema sync: 读取表清单失败: %v%s\n", lerr, dictMissingHint(lerr))
					return 1
				}
				tables = list
			}
			if len(tables) == 0 {
				fmt.Fprintln(stderr, "tiptop schema sync: DS.ZTA_FILE 里没有 zta02='ds' 的表（或当前账号无读权限）：请确认账号与字典表，或改用 CSV 导入")
				return 1
			}
			chunks := chunkStrings(tables, *batchSize)
			if len(chunks) > 1 {
				rep.Note("采集", fmt.Sprintf("共 %d 张表，按每批 %d 张分 %d 批查询",
					len(tables), *batchSize, len(chunks)))
			}
			parts := make([][]byte, 0, len(chunks))
			for i, ch := range chunks {
				if len(chunks) > 1 {
					rep.Note("采集", fmt.Sprintf("批次 %d/%d：%s … %s（%d 张表）",
						i+1, len(chunks), ch[0], ch[len(ch)-1], len(ch)))
				}
				cfg.Query = DefaultSchemaQuery(ch)
				part, n, qerr := oradb.QueryCSV(ctx, cfg, secretsDir, batchTimeout, onRows)
				if qerr != nil {
					if len(chunks) > 1 {
						fmt.Fprintf(stderr, "tiptop schema sync: 第 %d/%d 批（%s … %s）失败: %v%s\n",
							i+1, len(chunks), ch[0], ch[len(ch)-1], qerr, dictMissingHint(qerr))
					} else {
						fmt.Fprintf(stderr, "tiptop schema sync: %v%s\n", qerr, dictMissingHint(qerr))
					}
					return 1
				}
				rows += n
				parts = append(parts, part)
			}
			data = mergeCSVParts(parts)
			src = fmt.Sprintf("golang@%s:%d/%s owner=ds(TiTop)", *host, portOrDefault(*port), *service)
		}
		detail = src
		if rows == 0 {
			fmt.Fprintln(stderr, "tiptop schema sync: 查询返回 0 行：确认账号对 DS.ZTA_FILE/USER_TAB_COLUMNS 的权限与 zta02='ds' 的数据")
			return 1
		}
	case "file", "csv":
		path := strings.TrimSpace(*file)
		if path == "" {
			// 与插件「从 CSV 导入」的落点一致：<资料目录>/gal_schema.csv（新布局）。
			// 旧工程把 CSV 放在 <项目>/.project/gal_schema.csv，找不到时回退。
			path = filepath.Join(dataDir, "gal_schema.csv")
			if _, err := os.Stat(path); err != nil {
				legacyCSV := filepath.Join(projectRoot, DefaultDataDir, "gal_schema.csv")
				if _, lerr := os.Stat(legacyCSV); lerr == nil {
					path = legacyCSV
				}
			}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "tiptop schema sync: 读取 %s: %v\n", path, err)
			return 1
		}
		data = b
		src = filepath.Base(path)
		detail = path
	default:
		fmt.Fprintf(stderr, "tiptop schema sync: 未知来源 %q（用 golang 或 file）\n", *source)
		return 2
	}
	_ = detail

	res, err := Import(ImportOptions{
		CSVData:    data,
		DataDir:    dataDir,
		OnlyTables: only,
		OnlyNew:    *onlyNew,
		Prune:      *prune,
		DryRun:     *dryRun,
		Now:        start,
		Source:     src,
		Progress:   progressFn(rep),
	})
	if err != nil {
		fmt.Fprintf(stderr, "tiptop schema sync: %v\n", err)
		return 1
	}
	rep.Note("采集完成", fmt.Sprintf("%d 行", res.RowsTotal))
	if !*dryRun {
		rep.Note("写入完成", fmt.Sprintf("%d 个表文件", res.Written))
	}
	reportProblems(stderr, res.Problems)
	printImportReport(stdout, "schema 同步", res, src, rows, start, *dryRun)
	return 0
}

func portOrDefault(p int) int {
	if p > 0 {
		return p
	}
	return 1521
}
