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

// DefaultSchemaQuery 是直连 Oracle 查表字段的默认 SQL。
//
// 列名/顺序与"Oracle 导出 CSV"一致（含 `default_length`）：
//
//	table_name, column_name, column_desc(中文说明), data_type,
//	data_length, char_length, data_precision, data_scale, nullable, default_length
//
// owner 必须是 DBA 导出的那个应用 schema（默认取 -user）。Oracle 里普通账号的
// `all_tab_columns.owner` 是大写，这里统一转大写，避免"用 -owner toppord 查不到"。
func DefaultSchemaQuery(owner string, tables []string) string {
	owner = strings.ToUpper(strings.TrimSpace(owner))
	var sb strings.Builder
	sb.WriteString(`SELECT c.table_name, c.column_name, NVL(cc.comments, '') AS column_desc, `)
	sb.WriteString(`c.data_type, c.data_length, c.char_length, c.data_precision, c.data_scale, `)
	sb.WriteString(`c.nullable, c.default_length `)
	sb.WriteString(`FROM all_tab_columns c `)
	sb.WriteString(`LEFT JOIN all_col_comments cc `)
	sb.WriteString(`ON cc.owner = c.owner AND cc.table_name = c.table_name AND cc.column_name = c.column_name `)
	fmt.Fprintf(&sb, "WHERE c.owner = %s", quoteSQL(owner))
	if len(tables) > 0 {
		quoted := make([]string, 0, len(tables))
		for _, t := range tables {
			if t = strings.TrimSpace(t); t != "" {
				quoted = append(quoted, quoteSQL(strings.ToUpper(t)))
			}
		}
		if len(quoted) > 0 {
			fmt.Fprintf(&sb, " AND c.table_name IN (%s)", strings.Join(quoted, ", "))
		}
	}
	sb.WriteString(" ORDER BY c.table_name, c.column_id")
	return sb.String()
}

// DefaultTableListQuery 取 owner 下的表名清单（与明细查询同源：all_tab_columns）。
//
// 用 DISTINCT table_name 保证"清单里的表"与"明细查询能返回的表"完全一致
// （只有列的视图/表也都在 all_tab_columns 里），避免分批后漏表或多表。
func DefaultTableListQuery(owner string) string {
	owner = strings.ToUpper(strings.TrimSpace(owner))
	return fmt.Sprintf(
		"SELECT DISTINCT table_name FROM all_tab_columns WHERE owner = %s ORDER BY table_name",
		quoteSQL(owner))
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
	query := fs.String("query", "", "自定义 SQL（默认查 ALL_TAB_COLUMNS + ALL_COL_COMMENTS）")
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
		ownerName := strings.TrimSpace(*owner)
		if ownerName == "" {
			ownerName = strings.ToUpper(strings.TrimSpace(*user))
		}
		if ownerName == "" {
			fmt.Fprintln(stderr, "tiptop schema sync: source=golang 时必须有 -user（或显式给 -owner）")
			return 2
		}
		// secrets.json 只在**项目资料根**（.project），不用链接资料目录（.project/link）。
		secretsDir := filepath.Join(projectRoot, DefaultDataDir)
		cfg := oradb.Config{Host: *host, Port: *port, Service: *service, User: *user}
		// 密码不写进配置：走 oradb 的优先级（配置内 ${env:} → 环境变量 → secrets.json）。
		cfg.Password = ""
		batchTimeout := time.Duration(*timeoutSec) * time.Second
		if *timeoutSec <= 0 {
			batchTimeout = 120 * time.Second
		}
		onRows := func(n int) { rep.Note("采集", fmt.Sprintf("已收到 %d 行", n)) }
		rep.Note("采集", fmt.Sprintf("连接 %s:%d/%s（owner=%s）…",
			*host, portOrDefault(*port), *service, ownerName))

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
				rep.Note("采集", "读取表清单…")
				cfg.Query = DefaultTableListQuery(ownerName)
				list, lerr := oradb.QueryStrings(ctx, cfg, secretsDir, batchTimeout)
				if lerr != nil {
					fmt.Fprintf(stderr, "tiptop schema sync: 读取表清单失败: %v\n", lerr)
					return 1
				}
				tables = list
			}
			if len(tables) == 0 {
				fmt.Fprintf(stderr, "tiptop schema sync: 查询返回 0 行：确认 owner（%s）与账号权限\n", ownerName)
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
				cfg.Query = DefaultSchemaQuery(ownerName, ch)
				part, n, qerr := oradb.QueryCSV(ctx, cfg, secretsDir, batchTimeout, onRows)
				if qerr != nil {
					if len(chunks) > 1 {
						fmt.Fprintf(stderr, "tiptop schema sync: 第 %d/%d 批（%s … %s）失败: %v\n",
							i+1, len(chunks), ch[0], ch[len(ch)-1], qerr)
					} else {
						fmt.Fprintf(stderr, "tiptop schema sync: %v\n", qerr)
					}
					return 1
				}
				rows += n
				parts = append(parts, part)
			}
			data = mergeCSVParts(parts)
			src = fmt.Sprintf("golang@%s:%d/%s owner=%s", *host, portOrDefault(*port), *service, ownerName)
		}
		detail = src
		if rows == 0 {
			fmt.Fprintf(stderr, "tiptop schema sync: 查询返回 0 行：确认 owner（%s）与账号权限\n", ownerName)
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
