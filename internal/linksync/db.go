package linksync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"time"

	go_ora "github.com/sijms/go-ora/v2"
)

// dbSource 用纯 Go 驱动直连 Oracle 查 DS.GAL_FILE。
//
// 为什么是 go-ora（Q15 三度定稿）：它**不需要 Oracle 客户端**，纯 Go 编译，
// 也没有 CGO 依赖；代价只是二进制大 2–3 MB。用 godror/Instant Client 的
// 方案已明确取消（会让构建、分发、跨平台全部复杂化）。
type dbSource struct {
	cfg     DBConfig
	dataDir string
}

func newDBSource(cfg DBConfig, dataDir string) (*dbSource, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("%w：bdl.link.sync.db.host 为空", ErrNotConfigured)
	}
	if strings.TrimSpace(cfg.Service) == "" {
		return nil, fmt.Errorf("%w：bdl.link.sync.db.service 为空", ErrNotConfigured)
	}
	if strings.TrimSpace(cfg.User) == "" {
		return nil, fmt.Errorf("%w：bdl.link.sync.db.user 为空", ErrNotConfigured)
	}
	if cfg.Query == "" {
		cfg.Query = DefaultQuery
	}
	return &dbSource{cfg: cfg, dataDir: dataDir}, nil
}

func (d *dbSource) Name() string { return "golang" }

func (d *dbSource) Fetch(ctx context.Context) ([]byte, Stat, error) {
	password, err := resolvePassword(d.cfg, d.dataDir)
	if err != nil {
		return nil, Stat{}, err
	}
	port := d.cfg.Port
	if port <= 0 {
		port = 1521
	}
	dsn := go_ora.BuildUrl(d.cfg.Host, port, d.cfg.Service, d.cfg.User, password, nil)

	// 采集是批处理：给足超时，但要有上限，避免卡死编辑器。
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	db, err := sql.Open("oracle", dsn)
	if err != nil {
		return nil, Stat{}, fmt.Errorf("连接 Oracle: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	rows, err := db.QueryContext(ctx, d.cfg.Query)
	if err != nil {
		return nil, Stat{}, fmt.Errorf("查询 DS.GAL_FILE: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, Stat{}, err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	// 表头：与 `link import` 要求一致（GAL01..GAL04，大写）。
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = strings.ToUpper(strings.TrimSpace(c))
	}
	if err := w.Write(header); err != nil {
		return nil, Stat{}, err
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
			return nil, Stat{}, err
		}
		for i, v := range vals {
			rec[i] = cellString(v)
		}
		if err := w.Write(rec); err != nil {
			return nil, Stat{}, err
		}
		lines++
	}
	if err := rows.Err(); err != nil {
		return nil, Stat{}, err
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, Stat{}, err
	}
	if lines == 0 {
		return nil, Stat{}, fmt.Errorf("查询返回 0 行：确认表名/账号权限/`gal04` 过滤条件")
	}
	return buf.Bytes(), Stat{
		Source: "golang",
		Detail: fmt.Sprintf("DS.GAL_FILE@%s:%d/%s", d.cfg.Host, port, d.cfg.Service),
		Rows:   lines,
	}, nil
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

// resolvePassword 按优先级取密码：配置里的 `${env:VAR}` → 环境变量
// `BDL_ORA_PASSWORD` → `<dataDir>/secrets.json`。
//
// 为什么不接受明文写在 settings.json：那份文件会被提交进仓库。
func resolvePassword(cfg DBConfig, dataDir string) (string, error) {
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

// secretFromFile 从 `<dataDir>/secrets.json` 读密码。
//
// 文件权限必须是 0600：这份文件里有密码，权限松了直接报错，
// 而不是"默默能用"。
func secretFromFile(dataDir, user string) (string, error) {
	path := secretsPath(dataDir)
	st, err := os.Stat(path)
	if err != nil {
		return "", nil // 没有这个文件是常态
	}
	if st.Mode().Perm()&0o077 != 0 {
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
