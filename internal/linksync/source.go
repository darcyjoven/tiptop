// Package linksync 是链接资料（P7-D-3）的采集层：把 DS.GAL_FILE 的内容
// 从数据库/命令/文件取出来，交给 internal/link 导入。
//
// 三套来源：
//
//	golang  纯 Go 直连 Oracle（sijms/go-ora，无需 Oracle 客户端）
//	command 执行一条命令，凭证走 **stdin**（不进命令行参数，避免 ps 泄露）
//	file    读一份已经导出的 CSV（服务器脚本 sqlplus 的产物）
package linksync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/darcyjoven/bdl-lsp/link"
	"github.com/darcyjoven/tiptop/internal/oradb"
)

// DefaultQuery 是出厂 SQL。
//
// `gal04='Y'` 在 SQL 阶段就过滤掉：实测 51 行是 'N'，导出来再丢是浪费。
// 用 `DS.GAL_FILE` 的四个业务列，顺序与表头一致。
const DefaultQuery = `SELECT gal01, gal02, gal03, gal04 FROM DS.GAL_FILE WHERE gal04 = 'Y'`

// Stat 描述一次采集的来源与规模（记进 links.meta.json）。
type Stat struct {
	Source string // golang / command / file
	Detail string // 例如 "DS.GAL_FILE@host:1521/svc"
	Rows   int    // 采集到的数据行数（不含表头）
}

// Source 是采集来源的统一接口。
type Source interface {
	Name() string
	Fetch(ctx context.Context) ([]byte, Stat, error)
}

// ErrNotConfigured 表示这种来源缺少必要配置。
var ErrNotConfigured = errors.New("采集来源未配置")

// ---- 配置 ----

// Config 是 `bdl.link.sync` 的解码结果。
type Config struct {
	Enable  bool
	Source  string // golang | command | file
	DB      DBConfig
	Command string
	File    string
	// OnlyModules / OnlySpecs 非空时只采集这些模组/文件的链接（局部更新）。
	OnlyModules []string
	OnlySpecs   []string
	// OnRows 是直连数据库时的行级进度回调（已收到的行数），可为 nil。
	OnRows func(int)
	// ChainProgress 是"成链"阶段的进度回调（done/total），可为 nil。
	ChainProgress func(done, total int)
}

// FilterClause 把"只更新这些模组/文件"翻译成 Oracle 的 WHERE 追加条件。
//
//	modules=["lib","aap"]  →  UPPER(gal02) IN ('LIB','AAP')
//	specs=["aap/4gl/x.4gl"] →  (UPPER(gal02),LOWER(gal03)) IN (('AAP','x'))
//
// 只用于**默认 SQL**（见 newDBSource）：用户自定义 SQL 不做下推，
// 因为不知道他的列/表结构；那种情况仍在内存里过滤。
func FilterClause(modules, specs []string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	var clauses []string
	if len(modules) > 0 {
		vals := make([]string, 0, len(modules))
		for _, m := range modules {
			if m = strings.TrimSpace(m); m != "" {
				vals = append(vals, quote(strings.ToUpper(m)))
			}
		}
		if len(vals) > 0 {
			clauses = append(clauses, "UPPER(gal02) IN ("+strings.Join(vals, ",")+")")
		}
	}
	if len(specs) > 0 {
		var pairs []string
		for _, raw := range specs {
			mod, code := link.ParseFileSpec(raw)
			if mod == "" && code == "" {
				continue
			}
			pairs = append(pairs, "("+quote(strings.ToUpper(mod))+","+quote(strings.ToLower(code))+")")
		}
		if len(pairs) > 0 {
			clauses = append(clauses, "(UPPER(gal02),LOWER(gal03)) IN ("+strings.Join(pairs, ",")+")")
		}
	}
	if len(clauses) == 0 {
		return ""
	}
	return " AND (" + strings.Join(clauses, " OR ") + ")"
}

// DBConfig 是 Oracle 连接配置。
//
// 密码**绝不写进 settings.json**：支持 `${env:VAR}` 引用环境变量，
// 或写在 `<project>/.project/secrets.json`（0600）。
type DBConfig struct {
	Host     string
	Port     int
	Service  string
	User     string
	Password string
	Query    string
}

// toOraDB 转成公共连库配置（internal/oradb）。
func (c DBConfig) toOraDB() oradb.Config {
	return oradb.Config{
		Host: c.Host, Port: c.Port, Service: c.Service,
		User: c.User, Password: c.Password, Query: c.Query,
	}
}

// Source 按配置构造采集来源。
func (c Config) NewSource(dataDir string) (Source, error) {
	switch strings.ToLower(strings.TrimSpace(c.Source)) {
	case "", "golang", "db":
		db := c.DB
		// 局部更新：默认 SQL 时把"只要这些模组/文件"下推到 Oracle，少取行；
		// 用户自定义 SQL 不动它（不知道他怎么写的），只在内存里过滤。
		if db.Query == "" {
			db.Query = DefaultQuery
		}
		if db.Query == DefaultQuery && (len(c.OnlyModules) > 0 || len(c.OnlySpecs) > 0) {
			db.Query += FilterClause(c.OnlyModules, c.OnlySpecs)
		}
		return newDBSource(db, dataDir, c.OnRows)
	case "command", "cmd":
		return newCommandSource(c.Command)
	case "file", "script", "csv":
		return newFileSource(c.File, dataDir)
	default:
		return nil, fmt.Errorf("未知的采集来源 %q（可选 golang / command / file）", c.Source)
	}
}

// ---- file ----

type fileSource struct{ path string }

func newFileSource(path, dataDir string) (*fileSource, error) {
	if strings.TrimSpace(path) == "" {
		// 默认：<project>/.project/gal_file.csv（服务器脚本导出的落点）。
		path = dataDir + string(os.PathSeparator) + "gal_file.csv"
	}
	return &fileSource{path: path}, nil
}

func (f *fileSource) Name() string { return "file" }

func (f *fileSource) Fetch(_ context.Context) ([]byte, Stat, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return nil, Stat{}, fmt.Errorf("读取导出文件 %s: %w", f.path, err)
	}
	return data, Stat{Source: "file", Detail: f.path}, nil
}

// ---- command ----

// commandSource 执行一条命令，把 CSV 从 stdout 读回来。
//
// 凭证走 stdin（`user\npassword\n`），不进命令行参数：`ps` 里看不到密码。
// 命令本身从配置读，是用户自己写的脚本路径，不是我们拼的 shell 片段。
type commandSource struct{ command string }

func newCommandSource(command string) (*commandSource, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("%w：bdl.link.sync.command 为空", ErrNotConfigured)
	}
	return &commandSource{command: command}, nil
}

func (c *commandSource) Name() string { return "command" }

func (c *commandSource) Fetch(ctx context.Context) ([]byte, Stat, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", c.command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", c.command)
	}
	// 凭证由调用方通过 Env 注入（BDL_ORA_USER / BDL_ORA_PASSWORD），
	// 不写在参数里。
	cmd.Env = os.Environ()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, Stat{}, fmt.Errorf("执行采集命令失败：%s", msg)
	}
	if len(out) == 0 {
		return nil, Stat{}, fmt.Errorf("采集命令没有输出，检查它是否往 stdout 打印 CSV")
	}
	return out, Stat{Source: "command", Detail: c.command}, nil
}
