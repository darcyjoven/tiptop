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
	Enable   bool
	Source   string // golang | command | file
	Interval int    // 秒；0 = 不自动同步
	OnStart  bool
	DB       DBConfig
	Command  string
	File     string
}

// DBConfig 是 Oracle 连接配置。
//
// 密码**绝不写进 settings.json**：支持 `${env:VAR}` 引用环境变量，
// 或写在 `<project>/.link/secrets.json`（0600）。
type DBConfig struct {
	Host     string
	Port     int
	Service  string
	User     string
	Password string
	Query    string
}

// Source 按配置构造采集来源。
func (c Config) NewSource(dataDir string) (Source, error) {
	switch strings.ToLower(strings.TrimSpace(c.Source)) {
	case "", "golang", "db":
		return newDBSource(c.DB, dataDir)
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
		// 默认：<project>/.link/gal_file.csv（服务器脚本导出的落点）。
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
