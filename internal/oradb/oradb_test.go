package oradb

// 从 linksync 迁过来的密码来源测试（连接/密码规则现在是公共的），
// 外加 Validate / PortOr 的断言。

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

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
	got, err := ResolvePassword(Config{Password: "${env:MY_PW}", User: "u"}, dir)
	if err != nil || got != "from-env" {
		t.Fatalf("got %q, %v", got, err)
	}
	// 2) BDL_ORA_PASSWORD
	t.Setenv("BDL_ORA_PASSWORD", "from-bdl-env")
	got, err = ResolvePassword(Config{User: "u"}, dir)
	if err != nil || got != "from-bdl-env" {
		t.Fatalf("got %q, %v", got, err)
	}
	// 3) secrets.json（含通配 `*`）
	t.Setenv("BDL_ORA_PASSWORD", "")
	path := secretsPath(dir)
	if err := os.WriteFile(path, []byte(`{"u": "from-file", "*": "default-pw"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = ResolvePassword(Config{User: "u"}, dir)
	if err != nil || got != "from-file" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = ResolvePassword(Config{User: "other"}, dir)
	if err != nil || got != "default-pw" {
		t.Fatalf("通配密码：got %q, %v", got, err)
	}
	// 4) 都没有 → 明确报错
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePassword(Config{User: "u"}, dir); err == nil {
		t.Fatal("没有密码时应报错")
	}
}

func TestSecretsFileModeIsEnforced(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows 没有 unix 权限位（Go 对可写文件一律报 0666），
		// secretsPermTooLoose 在那边**刻意**返回 false，所以这里
		// 断言"权限过松要报错"没有意义。
		t.Skip("Windows 的 mode 位是合成的，跳过 unix 权限语义的断言")
	}
	dir := t.TempDir()
	if err := os.WriteFile(secretsPath(dir), []byte(`{"u":"pw"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BDL_ORA_PASSWORD", "")
	if _, err := ResolvePassword(Config{User: "u"}, dir); err == nil {
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

func TestValidateMessages(t *testing.T) {
	err := Config{}.Validate()
	if err == nil {
		t.Fatal("空配置应报错")
	}
	msg := err.Error()
	// 错误信息必须告诉用户"去哪儿填"，而不是只报"未配置"
	for _, want := range []string{"bdl.prod.oracle.host", "bdl.prod.oracle.service", "bdl.prod.oracle.user"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息应提到 %s，实际：%s", want, msg)
		}
	}
	if err := (Config{Host: "h", Service: "s", User: "u"}).Validate(); err != nil {
		t.Errorf("配全了不该报错：%v", err)
	}
}

func TestPortOr(t *testing.T) {
	if got := (Config{}).PortOr(); got != 1521 {
		t.Errorf("默认端口应是 1521，实际 %d", got)
	}
	if got := (Config{Port: 1522}).PortOr(); got != 1522 {
		t.Errorf("指定端口应原样返回，实际 %d", got)
	}
}

func TestQueryCSVRequiresQuery(t *testing.T) {
	if _, _, err := QueryCSV(t.Context(), Config{Host: "h", Service: "s", User: "u"}, t.TempDir(), 0, nil); err == nil {
		t.Error("查询语句为空时应报错（在连库之前）")
	}
}
