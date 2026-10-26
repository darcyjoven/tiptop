// Package deploy 实现 `bdl-lsp deploy`：把 Genero 源码上传到远程服务器并
// 远程编译，最后把编译器的输出解析成诊断。
//
// 安全模型（P5 计划 §7.4）：
//   - 部署必须显式触发（命令/CLI），不在保存时自动执行；
//   - 不存储密码，依赖 SSH key / agent；默认严格 host key 校验；
//   - 只允许配置文件中声明的目标；
//   - 上传走临时文件 + rename，避免远程编译读到半个文件；
//   - scp 回退时逐文件聚合错误，不把"部分成功"报成成功。
package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 约束：这些上限用于挡住明显写错的配置（例如把所有文件都 include 进来
// 导致 ssh 命令过长）。真实项目远超这些值时应当拆分目标。
const (
	// DefaultConfigName 是工作区内的默认配置文件名。
	DefaultConfigName = "deploy.json"
	// DefaultConfigDir 是配置所在的目录：W6 起为 `.project/deploy/`
	// （`.project` 根只留 `secrets.json`）。
	DefaultConfigDir = ".project/deploy"
	// LegacyConfigDir 是最老的配置目录（P1-2 之前）：只读兼容。
	LegacyConfigDir = ".bdl-lsp"
	// LegacyProjectConfigDir 是 W6 之前的配置目录（`.project` 根下的 deploy.json）。
	LegacyProjectConfigDir = ".project"

	maxPort = 65535
)

// Config 是整份部署配置（对应 `.project/deploy/deploy.json`）。
type Config struct {
	// Targets 是按名字索引的部署目标。
	Targets map[string]*Target `json:"targets"`
	// CreatedBy / GeneratedAt / Doc 是**插件生成**时写下的元信息（VSCode 插件
	// 是 .project/deploy/deploy.json 的唯一写入者，用户改设置而不是手改文件）。
	// 手写配置可以不带这些字段。
	//
	// 必须列进结构体：解析用 DisallowUnknownFields，插件写的字段不认识就会
	// 直接报错（部署配置写错字段名不该静默忽略，这里同理）。
	CreatedBy   string `json:"createdBy,omitempty"`
	GeneratedAt string `json:"generatedAt,omitempty"`
	Doc         string `json:"doc,omitempty"`
	// DefaultTarget 是未用 -target 指定时使用的目标名。
	DefaultTarget string `json:"defaultTarget,omitempty"`
	// Path 是配置文件的绝对路径（加载时填充，不参与 JSON）。
	Path string `json:"-"`
}

// Target 是单个部署目标。
type Target struct {
	// Host 是远程主机名或 IP（必填）。
	Host string `json:"host"`
	// Port 是 SSH 端口，默认 22。
	Port int `json:"port,omitempty"`
	// User 是 SSH 用户名（必填）。
	User string `json:"user"`
	// RemoteRoot 是远程工作根目录（必填，POSIX 绝对路径）。
	RemoteRoot string `json:"remoteRoot"`
	// LocalRoot 是本地根目录（相对配置文件所在的工作区根），默认 "."。
	LocalRoot string `json:"localRoot,omitempty"`
	// Upload 控制上传行为。
	Upload Upload `json:"upload"`
	// Compile 是远程编译命令模板，键为扩展名（不含点）：4gl / per。
	Compile map[string]string `json:"compile,omitempty"`
	// StrictHostKey 控制是否严格校验 host key，默认 true。
	//
	// 用指针是为了区分"未配置"与"显式 false"：部署场景下把 false 当默认
	// 会静默削弱安全，必须由用户显式关闭。
	StrictHostKey *bool `json:"strictHostKey,omitempty"`
	// ConnectTimeout 是单次连接超时（秒），默认 20。
	ConnectTimeout int `json:"connectTimeout,omitempty"`
}

// Upload 控制上传行为。
type Upload struct {
	// Include 是包含的 glob 列表（相对 LocalRoot），默认 4gl/per。
	Include []string `json:"include,omitempty"`
	// Exclude 是排除的 glob 列表，优先级高于 Include。
	Exclude []string `json:"exclude,omitempty"`
	// Mode 是首选传输方式：rsync（默认）/ scp。
	Mode string `json:"mode,omitempty"`
	// RsyncArgs 是追加给 rsync 的参数，默认 ["-az"]。
	RsyncArgs []string `json:"rsyncArgs,omitempty"`
	// Fallback 是首选方式失败后的回退方式：scp / none（默认 scp）。
	Fallback string `json:"fallback,omitempty"`
	// Delete 控制 rsync 是否 --delete（默认 false）。
	//
	// 默认关闭：Genero 项目里远程目录常混有编译产物与运行时文件，
	// 误删代价高。
	Delete bool `json:"delete,omitempty"`
}

// 传输方式常量。
const (
	// TransportRsync 走 rsync over ssh。
	TransportRsync = "rsync"
	// TransportScp 走 OpenSSH scp。
	TransportScp = "scp"
	// FallbackNone 表示不回退。
	FallbackNone = "none"
)

// 默认值。
const (
	defaultPort           = 22
	defaultConnectTimeout = 20
	defaultRsyncArgs      = "-az"
)

// LoadConfig 读取配置文件。
//
// path 为空时按 `<workspace>/.project/deploy/deploy.json` 查找（W6）；
// 找不到再依次回退 `<workspace>/.project/deploy.json`、`<workspace>/.bdl-lsp/deploy.json`
// （历史布局，只读兼容）。workspace 为空则用当前工作目录。
func LoadConfig(workspace, path string) (*Config, error) {
	if path == "" {
		base := workspace
		if base == "" {
			wd, err := os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("无法获取当前目录: %w", err)
			}
			base = wd
		}
		path = filepath.Join(base, DefaultConfigDir, DefaultConfigName)
		if _, err := os.Stat(path); err != nil {
			for _, legacyDir := range []string{LegacyProjectConfigDir, LegacyConfigDir} {
				legacy := filepath.Join(base, legacyDir, DefaultConfigName)
				if _, lerr := os.Stat(legacy); lerr == nil {
					path = legacy
					break
				}
			}
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析配置路径失败: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("读取部署配置 %s 失败: %w", abs, err)
	}

	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(data)))
	// 未知字段直接报错：部署配置写错字段名（如 remoteRoot 拼成 remoteRoot）
	// 时若静默忽略，会部署到意想不到的路径。
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("解析部署配置 %s 失败: %w", abs, err)
	}
	cfg.Path = abs
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyDefaults 填充默认值。
func (c *Config) applyDefaults() {
	for _, t := range c.Targets {
		if t == nil {
			continue
		}
		if t.Port == 0 {
			t.Port = defaultPort
		}
		if t.LocalRoot == "" {
			t.LocalRoot = "."
		}
		if t.ConnectTimeout == 0 {
			t.ConnectTimeout = defaultConnectTimeout
		}
		if t.StrictHostKey == nil {
			t.StrictHostKey = boolPtr(true)
		}
		if t.Upload.Mode == "" {
			t.Upload.Mode = TransportRsync
		}
		if t.Upload.Fallback == "" {
			t.Upload.Fallback = TransportScp
		}
		if len(t.Upload.RsyncArgs) == 0 {
			t.Upload.RsyncArgs = []string{defaultRsyncArgs}
		}
		if len(t.Upload.Include) == 0 {
			t.Upload.Include = []string{"**/*.4gl", "**/*.per"}
		}
	}
	if c.DefaultTarget == "" && len(c.Targets) == 1 {
		for name := range c.Targets {
			c.DefaultTarget = name
		}
	}
}

// Validate 校验配置完整性。
//
// 校验全部目标（而不是只校验被选中的那个）：配置写错应当在 dry-run 阶段
// 就暴露，而不是等到用时才失败。
func (c *Config) Validate() error {
	if len(c.Targets) == 0 {
		return fmt.Errorf("配置里没有任何 targets")
	}
	var problems []string
	for name, t := range c.Targets {
		if t == nil {
			problems = append(problems, fmt.Sprintf("target %q 是 null", name))
			continue
		}
		problems = append(problems, t.validate(name)...)
	}
	if c.DefaultTarget != "" {
		if _, ok := c.Targets[c.DefaultTarget]; !ok {
			problems = append(problems,
				fmt.Sprintf("defaultTarget %q 不存在于 targets", c.DefaultTarget))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("部署配置有误：\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// validate 校验单个目标，返回问题列表。
func (t *Target) validate(name string) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf("target %q: "+format, append([]any{name}, args...)...))
	}

	if t.Host == "" {
		add("host 必填")
	}
	if t.User == "" {
		add("user 必填")
	}
	if t.RemoteRoot == "" {
		add("remoteRoot 必填")
	} else if !strings.HasPrefix(t.RemoteRoot, "/") {
		// Windows 本地路径与 POSIX 远程路径混用是最常见的配置错误，
		// 直接拒绝比让 rsync 在后面报难懂的错更好。
		add("remoteRoot 必须是 POSIX 绝对路径（以 / 开头），实际 %q", t.RemoteRoot)
	}
	if t.Port < 1 || t.Port > maxPort {
		add("port 必须在 1..%d 之间，实际 %d", maxPort, t.Port)
	}
	if t.ConnectTimeout < 0 {
		add("connectTimeout 不能为负")
	}

	switch t.Upload.Mode {
	case TransportRsync, TransportScp:
	default:
		add("upload.mode 只支持 rsync / scp，实际 %q", t.Upload.Mode)
	}
	switch t.Upload.Fallback {
	case TransportScp, TransportRsync, FallbackNone:
	default:
		add("upload.fallback 只支持 scp / rsync / none，实际 %q", t.Upload.Fallback)
	}
	if t.Upload.Fallback != FallbackNone && t.Upload.Fallback == t.Upload.Mode {
		add("upload.fallback 与 mode 相同（%s），回退无意义", t.Upload.Mode)
	}

	for _, p := range checkGlobs("upload.include", t.Upload.Include) {
		add("%s", p)
	}
	for _, p := range checkGlobs("upload.exclude", t.Upload.Exclude) {
		add("%s", p)
	}

	// 编译命令模板必须含 {file}，否则无法按文件替换，会反复编译同一个东西。
	for ext, tpl := range t.Compile {
		if !strings.Contains(tpl, "{file}") {
			add("compile.%s 缺少 {file} 占位符：%q", ext, tpl)
		}
	}
	return problems
}

// Pick 按名字选目标；name 为空时用 defaultTarget。
func (c *Config) Pick(name string) (*Target, string, error) {
	if name == "" {
		name = c.DefaultTarget
	}
	if name == "" {
		return nil, "", fmt.Errorf("未指定 -target，且配置里没有 defaultTarget（有 %d 个目标）", len(c.Targets))
	}
	t, ok := c.Targets[name]
	if !ok {
		return nil, "", fmt.Errorf("目标 %q 不存在；可用：%s", name, strings.Join(c.TargetNames(), ", "))
	}
	return t, name, nil
}

// TargetNames 返回排序后的目标名（用于错误提示与列表示）。
func (c *Config) TargetNames() []string {
	names := make([]string, 0, len(c.Targets))
	for name := range c.Targets {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

// WorkspaceRoot 返回工作区根（配置文件所在工作区的根）。
//
// 支持三种位置：`.project/deploy/deploy.json`（W6）、`.project/deploy.json`、
// `.bdl-lsp/deploy.json`。都认不出来时（用户用 -config 指到任意路径）退回
// "配置文件的祖父目录"，保持与旧行为一致。
func (c *Config) WorkspaceRoot() string {
	if c.Path == "" {
		return ""
	}
	dir := filepath.Dir(c.Path)
	for {
		base := filepath.Base(dir)
		if base == ".project" || base == ".bdl-lsp" || base == ".link" {
			return filepath.Dir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Dir(filepath.Dir(c.Path))
		}
		dir = parent
	}
}

// LocalRootAbs 返回目标的本地根绝对路径。
func (t *Target) LocalRootAbs(workspaceRoot string) string {
	if filepath.IsAbs(t.LocalRoot) {
		return filepath.Clean(t.LocalRoot)
	}
	return filepath.Clean(filepath.Join(workspaceRoot, t.LocalRoot))
}

// strictHostKey 返回是否严格校验 host key（默认 true）。
func (t *Target) strictHostKey() bool {
	return t.StrictHostKey == nil || *t.StrictHostKey
}

// timeout 返回连接超时。
func (t *Target) timeout() time.Duration {
	if t.ConnectTimeout <= 0 {
		return defaultConnectTimeout * time.Second
	}
	return time.Duration(t.ConnectTimeout) * time.Second
}

// boolPtr 返回 bool 的指针。
func boolPtr(v bool) *bool { return &v }

// sortStrings 是 sort.Strings 的薄封装（避免在本文件引入 sort 只为一处调用）。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
