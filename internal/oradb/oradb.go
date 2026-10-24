// Package oradb 是 Oracle 直连的公共部分：连接、密码来源、查询结果转 CSV。
//
// 链接资料采集（linksync）与表字段资料（schemacli）都需要"连上 Oracle 查一次，
// 把结果当 CSV 交给各自的导入逻辑"，连接/密码/权限校验的规则必须完全一致，
// 所以抽到这里共用。
//
// 密码来源优先级（与文档一致）：
//  1. 配置里的 `${env:VAR}` / `$VAR` 展开；
//  2. 环境变量 `BDL_ORA_PASSWORD`；
//  3. `<数据目录>/secrets.json`（`{"用户名": "密码"}`，`*` 为默认；unix 下必须 0600）。
//
// 明文密码写在 settings.json 是不推荐的用法（那份文件会被提交），但这里不禁止——
// 用户自己的选择；文档里明确不推荐。
package oradb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	go_ora "github.com/sijms/go-ora/v2"
)

// Config 是一次 Oracle 连接与查询。
type Config struct {
	Host     string
	Port     int
	Service  string
	User     string
	Password string
	// Query 为空时由调用方填入默认 SQL。
	Query string
}

// ErrNotConfigured 表示数据库连接没配全。
var ErrNotConfigured = fmt.Errorf("数据库连接未配置")

// Validate 检查必需项，错误信息直接给出"去哪儿填"。
func (c Config) Validate() error {
	missing := make([]string, 0, 3)
	if strings.TrimSpace(c.Host) == "" {
		missing = append(missing, "主机（设置 `bdl.prod.oracle.host`）")
	}
	if strings.TrimSpace(c.Service) == "" {
		missing = append(missing, "服务名（`bdl.prod.oracle.service`）")
	}
	if strings.TrimSpace(c.User) == "" {
		missing = append(missing, "只读账号（`bdl.prod.oracle.user`）")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w：缺少 %s", ErrNotConfigured, strings.Join(missing, "、"))
	}
	return nil
}

// Port 返回有效端口（默认 1521）。
func (c Config) PortOr() int {
	if c.Port > 0 {
		return c.Port
	}
	return 1521
}

// QueryCSV 执行查询并把结果写成 CSV（表头 = 列名大写），返回 CSV 与数据行数。
//
// 交给调用方的是 CSV 而不是 []Row：两边的导入逻辑（`link import` / `schema import`）
// 本来就以 CSV 为输入，走同一条路可以保证"数据库采集"与"文件导入"结果一致。
func QueryCSV(ctx context.Context, cfg Config, dataDir string, timeout time.Duration, onRows func(int)) ([]byte, int, error) {
	if err := cfg.Validate(); err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(cfg.Query) == "" {
		return nil, 0, fmt.Errorf("内部错误：查询语句为空")
	}
	password, err := ResolvePassword(cfg, dataDir)
	if err != nil {
		return nil, 0, err
	}
	dsn := go_ora.BuildUrl(cfg.Host, cfg.PortOr(), cfg.Service, cfg.User, password, nil)

	// 采集是批处理：给足超时，但要有上限，避免卡死编辑器。
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	db, err := sql.Open("oracle", dsn)
	if err != nil {
		return nil, 0, fmt.Errorf("连接 Oracle: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	rows, err := db.QueryContext(ctx, cfg.Query)
	if err != nil {
		return nil, 0, fmt.Errorf("查询失败: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, 0, err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = strings.ToUpper(strings.TrimSpace(c))
	}
	if err := w.Write(header); err != nil {
		return nil, 0, err
	}

	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	rec := make([]string, len(cols))
	lines := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, 0, err
		}
		for i, v := range vals {
			rec[i] = cellString(v)
		}
		if err := w.Write(rec); err != nil {
			return nil, 0, err
		}
		lines++
		// 行级进度：远端导出是"一次性大查询"，这是唯一能看出它还在动的信号。
		if onRows != nil && lines%5000 == 0 {
			onRows(lines)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, 0, err
	}
	if onRows != nil {
		onRows(lines)
	}
	return buf.Bytes(), lines, nil
}

// ResolvePassword 按优先级取密码（`${env:VAR}` → 环境变量 → secrets.json）。
func ResolvePassword(cfg Config, dataDir string) (string, error) {
	if p := expandEnvRef(cfg.Password); p != "" {
		return p, nil
	}
	if p := os.Getenv("BDL_ORA_PASSWORD"); p != "" {
		return p, nil
	}
	if p, err := secretFromFile(dataDir, cfg.User); err != nil {
		return "", err
	} else if p != "" {
		return p, nil
	}
	return "", fmt.Errorf("缺少数据库密码：设置环境变量 BDL_ORA_PASSWORD，或写入 %s（见文档）",
		secretsPath(dataDir))
}

// cellString 把驱动返回值转成 CSV 字段（`nil` → 空串）。
func cellString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	case string:
		return t
	case time.Time:
		return t.Format(time.RFC3339)
	default:
		return fmt.Sprint(t)
	}
}

// expandEnvRef 支持 `${env:NAME}` 与 `$NAME` 两种写法。
func expandEnvRef(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if inner, ok := strings.CutPrefix(v, "${env:"); ok {
		name := strings.TrimSuffix(inner, "}")
		return os.Getenv(strings.TrimSpace(name))
	}
	if strings.HasPrefix(v, "$") {
		return os.Getenv(strings.TrimPrefix(v, "$"))
	}
	// 明文密码也接受（用户自己的选择），但文档里不推荐。
	return v
}

// secretsPermTooLoose 报告 secrets.json 的权限是否过松。
//
// Windows 没有 unix 权限位：Go 对可写文件一律报 0666，按这个判据会**永远**
// 判成"权限过松"，等于这条密码来源在 Windows 上不可用（2026-10-23 发现）。
// 那边的安全性由文件 ACL 决定，本项目不去猜——所以跳过校验，文档说明。
func secretsPermTooLoose(mode os.FileMode) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	return mode.Perm()&0o077 != 0
}

// secretFromFile 从 `<dataDir>/secrets.json` 读密码。
//
// 文件权限必须是 0600：这份文件里有密码，权限松了直接报错，
// 而不是"默默能用"（Windows 例外，见 secretsPermTooLoose）。
func secretFromFile(dataDir, user string) (string, error) {
	path := secretsPath(dataDir)
	st, err := os.Stat(path)
	if err != nil {
		return "", nil // 没有这个文件是常态
	}
	if secretsPermTooLoose(st.Mode()) {
		return "", fmt.Errorf("%s 权限过松（%04o），请 chmod 600", path, st.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secrets, err := parseSecrets(data)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if p := secrets[strings.ToLower(user)]; p != "" {
		return p, nil
	}
	return secrets["*"], nil
}
func secretsPath(dataDir string) string {
	return dataDir + string(os.PathSeparator) + "secrets.json"
}

// parseSecrets 解析 `{"用户名": "密码"}` 形式的 JSON（`*` 表示默认）。
//
// 手写解析而不是 encoding/json：内容只有一层字符串映射，省一个依赖点，
// 也避免"用户写了个数组"时报出难以理解的类型错误。
func parseSecrets(data []byte) (map[string]string, error) {
	out := map[string]string{}
	s := string(data)
	i := 0
	skip := func() {
		for i < len(s) && (s[i] == ' ' || s[i] == '\n' || s[i] == '\r' || s[i] == '\t' || s[i] == ',') {
			i++
		}
	}
	readString := func() (string, bool) {
		if i >= len(s) || s[i] != '"' {
			return "", false
		}
		i++
		var b strings.Builder
		for i < len(s) {
			if s[i] == '\\' && i+1 < len(s) {
				b.WriteByte(s[i+1])
				i += 2
				continue
			}
			if s[i] == '"' {
				i++
				return b.String(), true
			}
			b.WriteByte(s[i])
			i++
		}
		return "", false
	}
	skip()
	if i >= len(s) || s[i] != '{' {
		return nil, fmt.Errorf("secrets.json 必须是 {\"用户\": \"密码\"} 形式的对象")
	}
	i++
	for {
		skip()
		if i < len(s) && s[i] == '}' {
			break
		}
		key, ok := readString()
		if !ok {
			return nil, fmt.Errorf("secrets.json 解析失败（第 %d 字节）", i)
		}
		skip()
		if i >= len(s) || s[i] != ':' {
			return nil, fmt.Errorf("secrets.json 解析失败：%q 后缺少冒号", key)
		}
		i++
		skip()
		val, ok := readString()
		if !ok {
			return nil, fmt.Errorf("secrets.json 解析失败：%q 的值不是字符串", key)
		}
		out[strings.ToLower(key)] = val
	}
	return out, nil
}
