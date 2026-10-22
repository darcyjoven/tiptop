package linksync

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gal.csv")
	if err := os.WriteFile(path, []byte("GAL01,GAL02,GAL03,GAL04\np,APM,apmt100,Y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := newFileSource(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	data, stat, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "apmt100") {
		t.Errorf("取回内容不对：%q", data)
	}
	if stat.Source != "file" || stat.Detail != path {
		t.Errorf("stat 不对：%+v", stat)
	}
}

func TestFileSourceMissingFile(t *testing.T) {
	src, err := newFileSource(filepath.Join(t.TempDir(), "nope.csv"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.Fetch(context.Background()); err == nil {
		t.Fatal("文件不存在时应报错")
	}
}

// 采集命令在两种平台上都走 shell（Windows `cmd /C`，其余 `sh -c`，见 source.go），
// 所以测试里的命令要按平台方言写：Windows 没有 printf / true。

// csvEchoCommand 把两行 CSV 打到 stdout。
func csvEchoCommand() string {
	if runtime.GOOS == "windows" {
		return `echo GAL01,GAL02,GAL03,GAL04 & echo p,APM,apmt100,Y`
	}
	return `printf 'GAL01,GAL02,GAL03,GAL04\np,APM,apmt100,Y\n'`
}

// failingCommand 以非零退出。
func failingCommand() string { return "exit 3" }

// silentCommand 成功但不产出任何输出。
func silentCommand() string {
	if runtime.GOOS == "windows" {
		return "rem no output"
	}
	return "true"
}

func TestCommandSource(t *testing.T) {
	src, err := newCommandSource(csvEchoCommand())
	if err != nil {
		t.Fatal(err)
	}
	data, stat, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "apmt100") {
		t.Errorf("命令输出不对：%q", data)
	}
	if stat.Source != "command" {
		t.Errorf("stat.Source = %q", stat.Source)
	}
}

func TestCommandSourceFailure(t *testing.T) {
	src, err := newCommandSource(failingCommand())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.Fetch(context.Background()); err == nil {
		t.Fatal("命令失败时应报错")
	}
	// 空输出也算失败：用户脚本没往 stdout 打印 CSV 时要立刻说清楚。
	src2, _ := newCommandSource(silentCommand())
	if _, _, err := src2.Fetch(context.Background()); err == nil {
		t.Fatal("命令无输出时应报错")
	}
}

func TestCommandSourceEmpty(t *testing.T) {
	if _, err := newCommandSource("   "); err == nil {
		t.Fatal("空命令应在构造时就报错")
	}
}

func TestExpandEnvRef(t *testing.T) {
	t.Setenv("BDL_ORA_PASSWORD", "s3cret")
	cases := map[string]string{
		"${env:BDL_ORA_PASSWORD}": "s3cret",
		"$BDL_ORA_PASSWORD":       "s3cret",
		"plain":                   "plain",
		"":                        "",
		"${env:NOPE_NOT_SET}":     "",
	}
	for in, want := range cases {
		if got := expandEnvRef(in); got != want {
			t.Errorf("expandEnvRef(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestResolvePasswordPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BDL_ORA_PASSWORD", "")
	// 1) 配置里的 ${env:...} 最优先
	t.Setenv("MY_PW", "from-env")
	got, err := resolvePassword(DBConfig{Password: "${env:MY_PW}", User: "u"}, dir)
	if err != nil || got != "from-env" {
		t.Fatalf("got %q, %v", got, err)
	}
	// 2) BDL_ORA_PASSWORD
	t.Setenv("BDL_ORA_PASSWORD", "from-bdl-env")
	got, err = resolvePassword(DBConfig{User: "u"}, dir)
	if err != nil || got != "from-bdl-env" {
		t.Fatalf("got %q, %v", got, err)
	}
	// 3) secrets.json
	t.Setenv("BDL_ORA_PASSWORD", "")
	path := secretsPath(dir)
	if err := os.WriteFile(path, []byte(`{"u": "from-file", "*": "default-pw"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = resolvePassword(DBConfig{User: "u"}, dir)
	if err != nil || got != "from-file" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = resolvePassword(DBConfig{User: "other"}, dir)
	if err != nil || got != "default-pw" {
		t.Fatalf("通配密码：got %q, %v", got, err)
	}
	// 4) 都没有 → 明确报错
	os.Remove(path)
	if _, err := resolvePassword(DBConfig{User: "u"}, dir); err == nil {
		t.Fatal("没有密码时应报错")
	}
}

func TestSecretsFileModeIsEnforced(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows 没有 unix 权限位，Go 对可写文件一律报 0666，
		// 这项"权限过松"的断言在那边没有意义（也意味着 Windows 上
		// .link/secrets.json 这条密码来源不可用，只能用环境变量）。
		t.Skip("Windows 的 mode 位是合成的，跳过 unix 权限语义的断言")
	}
	dir := t.TempDir()
	path := secretsPath(dir)
	if err := os.WriteFile(path, []byte(`{"u":"pw"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BDL_ORA_PASSWORD", "")
	if _, err := resolvePassword(DBConfig{User: "u"}, dir); err == nil {
		t.Fatal("secrets.json 权限过松时应报错，而不是默默可用")
	}
}

func TestParseSecrets(t *testing.T) {
	got, err := parseSecrets([]byte("{\n \"A\": \"1\",\n \"b\": \"2\"\n}"))
	if err != nil {
		t.Fatal(err)
	}
	if got["a"] != "1" || got["b"] != "2" {
		t.Errorf("解析结果不对：%v", got)
	}
	if _, err := parseSecrets([]byte("[1,2]")); err == nil {
		t.Error("数组应报错")
	}
	if _, err := parseSecrets([]byte(`{"a": 1}`)); err == nil {
		t.Error("非字符串值应报错")
	}
}

func TestConfigNewSourceValidation(t *testing.T) {
	if _, err := (Config{Source: "golang"}).NewSource(t.TempDir()); err == nil {
		t.Error("golang 缺 host 应报错")
	}
	if _, err := (Config{Source: "command"}).NewSource(t.TempDir()); err == nil {
		t.Error("command 缺命令应报错")
	}
	if _, err := (Config{Source: "nope"}).NewSource(t.TempDir()); err == nil {
		t.Error("未知来源应报错")
	}
	src, err := (Config{Source: "file", File: "/tmp/x.csv"}).NewSource(t.TempDir())
	if err != nil || src.Name() != "file" {
		t.Fatalf("file 来源构造失败：%v", err)
	}
}
