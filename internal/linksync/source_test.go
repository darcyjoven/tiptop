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
func TestConfigNewSourceValidation(t *testing.T) {
	if _, err := (Config{Source: "golang"}).NewSource(t.TempDir(), t.TempDir()); err == nil {
		t.Error("golang 缺 host 应报错")
	}
	if _, err := (Config{Source: "command"}).NewSource(t.TempDir(), t.TempDir()); err == nil {
		t.Error("command 缺命令应报错")
	}
	if _, err := (Config{Source: "nope"}).NewSource(t.TempDir(), t.TempDir()); err == nil {
		t.Error("未知来源应报错")
	}
	src, err := (Config{Source: "file", File: "/tmp/x.csv"}).NewSource(t.TempDir(), t.TempDir())
	if err != nil || src.Name() != "file" {
		t.Fatalf("file 来源构造失败：%v", err)
	}
}

// TestNewSourcePassesSecretsDir 钉住 W7.3：golang 来源拿到的 secrets 目录
// 必须是**项目资料根**（调用方传的 secretsDir），不能用链接资料目录顶替——
// 否则会去 .project/link/secrets.json 找密码。
func TestNewSourcePassesSecretsDir(t *testing.T) {
	dataDir := t.TempDir()
	secretsDir := t.TempDir()
	src, err := (Config{
		Source: "golang",
		DB:     DBConfig{Host: "db", Service: "svc", User: "u"},
	}).NewSource(dataDir, secretsDir)
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	db, ok := src.(*dbSource)
	if !ok {
		t.Fatalf("golang 应构造 dbSource，实际 %T", src)
	}
	if db.secretsDir != secretsDir {
		t.Fatalf("secretsDir 应为 %q，实际 %q", secretsDir, db.secretsDir)
	}
}
