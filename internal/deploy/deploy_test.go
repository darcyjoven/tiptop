package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner 记录执行的命令并返回预设结果。
type fakeRunner struct {
	calls   []call
	results map[string]Output // 键是命令名
}

type call struct {
	name string
	args []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string) (Output, error) {
	f.calls = append(f.calls, call{name: name, args: args})
	if out, ok := f.results[name]; ok {
		return out, nil
	}
	return Output{CmdLine: name}, nil
}

// cmdNames 返回所有被调用过的命令名。
func (f *fakeRunner) cmdNames() []string {
	var names []string
	for _, c := range f.calls {
		names = append(names, c.name)
	}
	return names
}

// writeConfig 在临时目录里写配置并返回工作区根。
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, DefaultConfigDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建配置目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, DefaultConfigName), []byte(body), 0o644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	return root
}

func TestLoadConfigDefaults(t *testing.T) {
	root := writeConfig(t, `{
	  "targets": {"test": {"host": "h", "user": "u", "remoteRoot": "/u1/app"}}
	}`)
	cfg, err := LoadConfig(root, "")
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	tgt := cfg.Targets["test"]
	if tgt.Port != 22 {
		t.Errorf("默认端口应为 22，实际 %d", tgt.Port)
	}
	if tgt.LocalRoot != "." {
		t.Errorf("默认本地根应为 .，实际 %q", tgt.LocalRoot)
	}
	if !tgt.strictHostKey() {
		t.Error("默认应严格校验 host key")
	}
	if tgt.Upload.Mode != TransportRsync {
		t.Errorf("默认传输应为 rsync，实际 %q", tgt.Upload.Mode)
	}
	if tgt.Upload.Fallback != TransportScp {
		t.Errorf("默认回退应为 scp，实际 %q", tgt.Upload.Fallback)
	}
	if cfg.DefaultTarget != "test" {
		t.Errorf("单目标时 defaultTarget 应自动填，实际 %q", cfg.DefaultTarget)
	}
}

func TestLoadConfigRejectsUnknownField(t *testing.T) {
	// remoteRoot 拼错成 remote_root 必须报错，不能静默部署到空路径。
	root := writeConfig(t, `{
	  "targets": {"test": {"host": "h", "user": "u", "remote_root": "/u1/app"}}
	}`)
	_, err := LoadConfig(root, "")
	if err == nil {
		t.Fatal("未知字段应报错")
	}
	if !strings.Contains(err.Error(), "remote_root") {
		t.Errorf("错误信息应提到拼错的字段 remote_root：%v", err)
	}
}

func TestValidateRemoteRootAbsolute(t *testing.T) {
	root := writeConfig(t, `{
	  "targets": {"test": {"host": "h", "user": "u", "remoteRoot": "C:\\u1\\app"}}
	}`)
	_, err := LoadConfig(root, "")
	if err == nil {
		t.Fatal("Windows 风格远程路径应被拒绝")
	}
	if !strings.Contains(err.Error(), "POSIX") {
		t.Errorf("错误应提示 POSIX：%v", err)
	}
}

func TestValidateCompileTemplateNeedsFile(t *testing.T) {
	root := writeConfig(t, `{
	  "targets": {"test": {"host": "h", "user": "u", "remoteRoot": "/u1/app",
	    "compile": {"4gl": "fglcomp -M"}}}
	}`)
	_, err := LoadConfig(root, "")
	if err == nil {
		t.Fatal("compile 模板缺 {file} 应报错")
	}
	if !strings.Contains(err.Error(), "{file}") {
		t.Errorf("错误应提示 {file}：%v", err)
	}
}

func TestValidateFallbackSameAsMode(t *testing.T) {
	root := writeConfig(t, `{
	  "targets": {"test": {"host": "h", "user": "u", "remoteRoot": "/u1/app",
	    "upload": {"mode": "scp", "fallback": "scp"}}}
	}`)
	_, err := LoadConfig(root, "")
	if err == nil {
		t.Fatal("fallback 与 mode 相同应报错")
	}
}

func TestPickTarget(t *testing.T) {
	root := writeConfig(t, `{
	  "targets": {
	    "a": {"host": "h", "user": "u", "remoteRoot": "/x"},
	    "b": {"host": "h2", "user": "u2", "remoteRoot": "/y"}
	  },
	  "defaultTarget": "b"
	}`)
	cfg, err := LoadConfig(root, "")
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if _, name, err := cfg.Pick(""); err != nil || name != "b" {
		t.Errorf("应取 defaultTarget=b，实际 %q err=%v", name, err)
	}
	if _, name, err := cfg.Pick("a"); err != nil || name != "a" {
		t.Errorf("应取 a，实际 %q err=%v", name, err)
	}
	if _, _, err := cfg.Pick("zzz"); err == nil {
		t.Error("不存在的目标应报错")
	}
}

func TestCollectFilesIncludeExclude(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.4gl"), "MAIN\nEND MAIN\n")
	mustWrite(t, filepath.Join(root, "sub", "b.per"), "LAYOUT\nEND\n")
	mustWrite(t, filepath.Join(root, "sub", "c.txt"), "ignore")
	mustWrite(t, filepath.Join(root, "42f", "d.4gl"), "MAIN\nEND MAIN\n")
	mustWrite(t, filepath.Join(root, ".git", "e.4gl"), "should skip")

	files, err := CollectFiles(root, Upload{
		Include: []string{"**/*.4gl", "**/*.per"},
		Exclude: []string{"**/42f/**"},
	})
	if err != nil {
		t.Fatalf("收集失败: %v", err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Rel] = f.Kind
	}
	if _, ok := got["a.4gl"]; !ok {
		t.Error("a.4gl 应被收集")
	}
	if _, ok := got["sub/b.per"]; !ok {
		t.Error("sub/b.per 应被收集")
	}
	if _, ok := got["sub/c.txt"]; ok {
		t.Error("c.txt 不应被收集")
	}
	if _, ok := got["42f/d.4gl"]; ok {
		t.Error("42f/d.4gl 应被 exclude")
	}
	if _, ok := got[".git/e.4gl"]; ok {
		t.Error(".git 下的文件永不上传")
	}
	// 排序稳定
	for i := 1; i < len(files); i++ {
		if files[i].Rel < files[i-1].Rel {
			t.Errorf("文件未按 Rel 排序: %s 在 %s 之后", files[i].Rel, files[i-1].Rel)
		}
	}
}

func TestCollectFilesCRLFWarning(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "crlf.4gl"), "MAIN\r\nEND MAIN\r\n")
	files, err := CollectFiles(root, Upload{Include: []string{"**/*.4gl"}})
	if err != nil {
		t.Fatalf("收集失败: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("应收集 1 个文件，实际 %d", len(files))
	}
	if len(files[0].Warnings) == 0 {
		t.Error("CRLF 文件应产生告警")
	}
	if !strings.Contains(files[0].Warnings[0], "CRLF") {
		t.Errorf("告警应提到 CRLF：%v", files[0].Warnings)
	}
}

func TestGlobDoubleStar(t *testing.T) {
	tests := []struct {
		pattern string
		rel     string
		want    bool
	}{
		{"**/*.4gl", "a.4gl", true},
		{"**/*.4gl", "x/y/a.4gl", true},
		{"**/*.4gl", "x/y/a.per", false},
		{"*.4gl", "a.4gl", true},
		{"*.4gl", "x/a.4gl", false},
		{"**/42f/**", "42f/a.4gl", true},
		{"**/42f/**", "m/42f/sub/a.4gl", true},
		{"**/42f/**", "m/a.4gl", false},
		{"src/**/*.per", "src/a/b.per", true},
		{"src/**/*.per", "src/a/b.4gl", false},
		{"src/**/*.per", "other/src/a/b.per", false},
	}
	for _, tc := range tests {
		g, err := compileGlob(tc.pattern)
		if err != nil {
			t.Fatalf("编译 %q 失败: %v", tc.pattern, err)
		}
		if got := g.Match(tc.rel); got != tc.want {
			t.Errorf("%q 匹配 %q = %v，期望 %v", tc.pattern, tc.rel, got, tc.want)
		}
	}
}

func TestRemotePathPOSIX(t *testing.T) {
	f := File{Rel: "sub/a.4gl", Kind: Kind4GL}
	got := f.RemotePath("/u1/app")
	if got != "/u1/app/sub/a.4gl" {
		t.Errorf("远程路径应为 POSIX 拼接，实际 %q", got)
	}
}

func TestParseCompilerOutput(t *testing.T) {
	output := strings.Join([]string{
		"aapr106.4gl:232:12: error: -8403: Identifier not defined",
		"aapt110.per:15: warning: Undefined item",
		"/u1/app/sub/b.4gl:7: error: -1234: Syntax error",
		"** Error: cannot open file",
		"Compiling aapr106.4gl...",
		"",
	}, "\n")

	diags := ParseCompilerOutput(output, func(remote string) string {
		return mapRemoteToLocal(remote, "/u1/app", "/local/app")
	})
	if len(diags) != 4 {
		t.Fatalf("应解析出 4 条诊断，实际 %d: %+v", len(diags), diags)
	}

	first := diags[0]
	if first.File != filepath.Join("/local/app", "aapr106.4gl") {
		t.Errorf("相对路径应映射到本地根，实际 %q", first.File)
	}
	if first.Line != 232 || first.Column != 12 {
		t.Errorf("位置应为 232:12，实际 %d:%d", first.Line, first.Column)
	}
	if first.Severity != SeverityError {
		t.Errorf("级别应为 error，实际 %q", first.Severity)
	}
	if first.Code != "8403" {
		t.Errorf("错误码应为 8403，实际 %q", first.Code)
	}
	if !strings.Contains(first.Message, "Identifier not defined") {
		t.Errorf("消息不符：%q", first.Message)
	}

	if diags[1].Severity != SeverityWarning {
		t.Errorf("第二条应为 warning，实际 %q", diags[1].Severity)
	}

	third := diags[2]
	if third.File != filepath.Join("/local/app", "sub/b.4gl") {
		t.Errorf("绝对远程路径应映射到本地，实际 %q", third.File)
	}
	if third.Column != 0 {
		t.Errorf("无列号时应为 0，实际 %d", third.Column)
	}

	if diags[3].Line != 0 {
		t.Errorf("无位置信息的诊断 Line 应为 0，实际 %d", diags[3].Line)
	}

	// 普通输出行不应被当成诊断
	for _, d := range diags {
		if strings.Contains(d.Message, "Compiling") {
			t.Error("普通输出被误当诊断")
		}
	}
}

func TestSummarize(t *testing.T) {
	diags := []Diagnostic{
		{Severity: SeverityError}, {Severity: SeverityWarning}, {Severity: SeverityError},
	}
	errs, warns := Summarize(diags)
	if errs != 2 || warns != 1 {
		t.Errorf("应为 2 error / 1 warning，实际 %d / %d", errs, warns)
	}
}

func TestDryRunNoCommands(t *testing.T) {
	root := writeConfig(t, `{
	  "targets": {"test": {"host": "h", "user": "u", "remoteRoot": "/u1/app",
	    "compile": {"4gl": "fglcomp -M {file}"}}}
	}`)
	mustWrite(t, filepath.Join(root, "a.4gl"), "MAIN\nEND MAIN\n")

	fr := &fakeRunner{}
	var logs []string
	res, err := Run(context.Background(), Options{
		Workspace: root,
		DryRun:    true,
		Runner:    fr,
		Log: func(f string, a ...any) {
			logs = append(logs, fmt.Sprintf(f, a...))
		},
	})
	if err != nil {
		t.Fatalf("dry-run 失败: %v", err)
	}
	if len(fr.calls) != 0 {
		t.Errorf("dry-run 不应执行任何命令，实际调用了 %v", fr.cmdNames())
	}
	if len(res.Files) != 1 {
		t.Errorf("应列出 1 个文件，实际 %d", len(res.Files))
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "upload a.4gl") {
		t.Errorf("dry-run 应打印上传计划：%s", joined)
	}
	if !strings.Contains(joined, "compile a.4gl") {
		t.Errorf("dry-run 应打印编译计划：%s", joined)
	}
}

func TestUploadRsyncThenCompile(t *testing.T) {
	root := writeConfig(t, `{
	  "targets": {"test": {"host": "h", "user": "u", "remoteRoot": "/u1/app",
	    "compile": {"4gl": "fglcomp -M {file}"}}}
	}`)
	mustWrite(t, filepath.Join(root, "a.4gl"), "MAIN\nEND MAIN\n")

	fr := &fakeRunner{results: map[string]Output{
		"ssh":   {Stdout: ""},
		"rsync": {Stdout: "sent"},
	}}
	res, err := Run(context.Background(), Options{Workspace: root, Runner: fr})
	if err != nil {
		t.Fatalf("部署失败: %v", err)
	}
	if res.Transport != TransportRsync {
		t.Errorf("应用 rsync，实际 %q", res.Transport)
	}
	if res.Uploaded != 1 {
		t.Errorf("应上传 1 个文件，实际 %d", res.Uploaded)
	}
	names := fr.cmdNames()
	if !contains(names, "rsync") {
		t.Errorf("应调用 rsync，实际 %v", names)
	}
	if !contains(names, "ssh") {
		t.Errorf("应调用 ssh 做远程编译，实际 %v", names)
	}
	if !res.Ok() {
		t.Error("无诊断时应为成功")
	}
}

func TestUploadFallbackToScp(t *testing.T) {
	root := writeConfig(t, `{
	  "targets": {"test": {"host": "h", "user": "u", "remoteRoot": "/u1/app"}}
	}`)
	mustWrite(t, filepath.Join(root, "a.4gl"), "MAIN\nEND MAIN\n")

	fr := &fakeRunner{results: map[string]Output{
		// rsync 失败（非零退出码），scp 成功
		"rsync": {Stderr: "rsync: command not found", Err: errors.New("exit status 127")},
		"scp":   {},
		"ssh":   {},
	}}
	res, err := Run(context.Background(), Options{Workspace: root, Runner: fr, SkipCompile: true})
	if err != nil {
		t.Fatalf("部署失败: %v", err)
	}
	if !res.Fallback {
		t.Error("应标记发生过回退")
	}
	if res.Transport != TransportScp {
		t.Errorf("回退后应为 scp，实际 %q", res.Transport)
	}
	names := fr.cmdNames()
	if !contains(names, "scp") {
		t.Errorf("应调用 scp，实际 %v", names)
	}
}

func TestUploadScpPartialFailureReports(t *testing.T) {
	root := writeConfig(t, `{
	  "targets": {"test": {"host": "h", "user": "u", "remoteRoot": "/u1/app",
	    "upload": {"mode": "scp", "fallback": "none"}}}
	}`)
	mustWrite(t, filepath.Join(root, "ok.4gl"), "MAIN\nEND MAIN\n")
	mustWrite(t, filepath.Join(root, "bad.4gl"), "MAIN\nEND MAIN\n")

	fr := &scpFailSecond{}
	res, err := Run(context.Background(), Options{Workspace: root, Runner: fr, SkipCompile: true})
	if err != nil {
		t.Fatalf("上传失败应记进 Result 而不是返回 error: %v", err)
	}
	if res.UploadErr == nil {
		t.Error("整体上传失败应记进 res.UploadErr")
	}
	if len(res.UploadFailed) != 1 {
		t.Fatalf("应有 1 个文件上传失败，实际 %d", len(res.UploadFailed))
	}
	if res.Ok() {
		t.Error("部分失败时 Ok() 必须为 false——不能显示成成功")
	}
	if res.Uploaded != 1 {
		t.Errorf("成功上传数应为 1，实际 %d", res.Uploaded)
	}
}

// scpFailSecond 让第二个 scp 失败。
type scpFailSecond struct {
	scpCount int
}

func (f *scpFailSecond) Run(_ context.Context, name string, _ []string) (Output, error) {
	if name != "scp" {
		return Output{}, nil
	}
	f.scpCount++
	if f.scpCount == 2 {
		return Output{Stderr: "permission denied", Err: errors.New("exit status 1")}, nil
	}
	return Output{}, nil
}

func TestCompileDiagnosticsMappedToLocal(t *testing.T) {
	root := writeConfig(t, `{
	  "targets": {"test": {"host": "h", "user": "u", "remoteRoot": "/u1/app",
	    "compile": {"4gl": "fglcomp -M {file}"}}}
	}`)
	mustWrite(t, filepath.Join(root, "a.4gl"), "MAIN\nEND MAIN\n")

	fr := &fakeRunner{results: map[string]Output{
		"ssh": {Stdout: "a.4gl:3: error: -8403: Identifier not defined\n"},
	}}
	res, err := Run(context.Background(), Options{
		Workspace: root, Runner: fr, SkipUpload: true,
	})
	if err != nil {
		t.Fatalf("部署失败: %v", err)
	}
	if len(res.Diagnostics) != 1 {
		t.Fatalf("应解析出 1 条诊断，实际 %d", len(res.Diagnostics))
	}
	d := res.Diagnostics[0]
	if d.File != filepath.Join(root, "a.4gl") {
		t.Errorf("诊断应映射到本地文件，实际 %q", d.File)
	}
	if res.Ok() {
		t.Error("有 error 诊断时应为失败")
	}
}

func TestCompileTemplateQuoted(t *testing.T) {
	// 远程路径里有空格时必须转义，否则远程 shell 会拆成两个参数。
	got := expandTemplate("fglcomp -M {file}", "/u1/my app/a.4gl")
	if !strings.Contains(got, "'/u1/my app/a.4gl'") {
		t.Errorf("含空格的路径应被引号包裹，实际 %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"simple", "simple"},
		{"/u1/app/a.4gl", "/u1/app/a.4gl"},
		{"with space", "'with space'"},
		{"it's", `'it'\''s'`},
		{"", "''"},
	}
	for _, tc := range tests {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestSSHArgsStrictHostKey(t *testing.T) {
	strict := &Target{Host: "h", User: "u", Port: 22, StrictHostKey: boolPtr(true)}
	args := strings.Join(sshCommandArgs(strict, "true"), " ")
	if !strings.Contains(args, "StrictHostKeyChecking=yes") {
		t.Errorf("严格模式应带 StrictHostKeyChecking=yes：%s", args)
	}
	if strings.Contains(args, "StrictHostKeyChecking=no") {
		t.Errorf("绝不能出现 StrictHostKeyChecking=no：%s", args)
	}

	loose := &Target{Host: "h", User: "u", Port: 22, StrictHostKey: boolPtr(false)}
	args2 := strings.Join(sshCommandArgs(loose, "true"), " ")
	if !strings.Contains(args2, "StrictHostKeyChecking=accept-new") {
		t.Errorf("非严格模式应用 accept-new（而非 no）：%s", args2)
	}
}

func TestSSHArgsOrder(t *testing.T) {
	// 目标必须在命令之前，否则 ssh 会把命令当成主机名。
	tgt := &Target{Host: "h", User: "u", Port: 2222, StrictHostKey: boolPtr(true)}
	args := sshCommandArgs(tgt, "echo hi")
	hostIdx, cmdIdx := -1, -1
	for i, a := range args {
		if a == "u@h" {
			hostIdx = i
		}
		if strings.Contains(a, "echo hi") {
			cmdIdx = i
		}
	}
	if hostIdx < 0 || cmdIdx < 0 {
		t.Fatalf("参数里应有目标与命令：%v", args)
	}
	if hostIdx > cmdIdx {
		t.Errorf("目标应排在命令之前：%v", args)
	}
	if args[hostIdx+1] != args[cmdIdx] {
		t.Errorf("命令应紧跟在目标之后：%v", args)
	}
}

// --- 测试辅助 ---

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestSelectFilesRelativeToLocalRoot 验证显式文件路径按 localRoot 解析，
// 而不是按进程当前目录解析。
//
// 回归：曾用 filepath.Abs（进程 CWD），在 workspace 子目录场景下拼出
// `<localRoot>/../../../src` 这类越界路径，远程 mkdir 直接 permission denied。
func TestSelectFilesRelativeToLocalRoot(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "src", "a.4gl"), "MAIN\nEND MAIN\n")
	mustWrite(t, filepath.Join(root, "src", "b.4gl"), "MAIN\nEND MAIN\n")

	// 不切换 CWD：root 是临时目录，进程 CWD 与它无关。
	files, err := selectFiles(root, Upload{Include: []string{"**/*.4gl"}}, []string{"src/a.4gl"})
	if err != nil {
		t.Fatalf("选择文件失败: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("应选中 1 个文件，实际 %d: %+v", len(files), files)
	}
	if files[0].Rel != "src/a.4gl" {
		t.Errorf("Rel 应为 src/a.4gl，实际 %q", files[0].Rel)
	}
}

func TestSelectFilesRejectsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.4gl"), "MAIN\nEND MAIN\n")
	// 本地根之外的文件必须被拒绝，否则会拼出越界的远程路径。
	if _, err := selectFiles(root, Upload{Include: []string{"**/*.4gl"}},
		[]string{"../outside.4gl"}); err == nil {
		t.Error("本地根之外的路径应被拒绝")
	}
}

func TestSelectFilesRejectsUnsupportedType(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "x")
	if _, err := selectFiles(root, Upload{Include: []string{"**/*.4gl"}},
		[]string{"a.txt"}); err == nil {
		t.Error("非 .4gl/.global/.per 文件应被拒绝")
	}
}
