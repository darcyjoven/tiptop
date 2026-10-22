package linksync

import (
	"context"
	"os"
	"path/filepath"
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

func TestCommandSource(t *testing.T) {
	src, err := newCommandSource("printf 'GAL01,GAL02,GAL03,GAL04\\np,APM,apmt100,Y\\n'")
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
	src, err := newCommandSource("exit 3")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.Fetch(context.Background()); err == nil {
		t.Fatal("命令失败时应报错")
	}
	// 空输出也算失败：用户脚本没往 stdout 打印 CSV 时要立刻说清楚。
	src2, _ := newCommandSource("true")
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
