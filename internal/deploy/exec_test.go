package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recRunner 记录执行过的命令行，便于断言"跑了什么/没跑什么"。
// （deploy_test.go 里已有 fakeRunner，这里另起一个名字避免冲突。）
type recRunner struct {
	calls []string
	out   Output
	err   error
}

func (f *recRunner) Run(_ context.Context, name string, args []string) (Output, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return f.out, f.err
}

// stubTools 让 requireTool 在测试环境里总是通过。
func stubTools(t *testing.T) {
	t.Helper()
	old := lookPath
	lookPath = func(string) (string, error) { return "/usr/bin/stub", nil }
	t.Cleanup(func() { lookPath = old })
}

func testTarget() *Target {
	t := &Target{Host: "erp", Port: 22, User: "tiptop", RemoteRoot: "/u1/toppord"}
	t.Upload.Mode = TransportScp
	t.Upload.Fallback = FallbackNone
	return t
}

func TestRelPath(t *testing.T) {
	ok, err := RelPath(`tiptop\apm\4gl\axcp500.4gl`) // 反斜杠统一成 /
	if err != nil {
		t.Fatalf("反斜杠应被规范化：%v", err)
	}
	if ok != "tiptop/apm/4gl/axcp500.4gl" {
		t.Fatalf("规范化结果不对：%q", ok)
	}
	if got, _ := RelPath("./a/./b.4gl"); got != "a/b.4gl" {
		t.Fatalf("应去掉 ./：%q", got)
	}
	for _, bad := range []string{"", "  ", "/abs/x.4gl", "../x.4gl", "a/../../x.4gl", "C:/x.4gl"} {
		if _, err := RelPath(bad); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
}

func TestLocalRemotePathFor(t *testing.T) {
	local, err := LocalPathFor("/ws", "a/b.4gl")
	if err != nil {
		t.Fatal(err)
	}
	if local != filepath.Join("/ws", "a", "b.4gl") {
		t.Fatalf("本地路径不对：%s", local)
	}
	remote, err := RemotePathFor("/u1/toppord/", "a/b.4gl")
	if err != nil {
		t.Fatal(err)
	}
	if remote != "/u1/toppord/a/b.4gl" {
		t.Fatalf("远程路径不对：%s", remote)
	}
}

func TestRemoteScript(t *testing.T) {
	got := RemoteScript("/u1/toppord", "cd axc/4gl && r.c2 axcp500")
	if got != "cd /u1/toppord && cd axc/4gl && r.c2 axcp500" {
		t.Fatalf("脚本不对：%s", got)
	}
	// 生产根含空格要转义
	got = RemoteScript("/u1/my app", "r.l2 x")
	if !strings.Contains(got, "cd '/u1/my app'") {
		t.Fatalf("含空格的根应加引号：%s", got)
	}
}

func TestUploadOneDryRun(t *testing.T) {
	stubTools(t)
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "a", "b.4gl"), []byte("MAIN\nEND MAIN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recRunner{}
	var logs []string
	if err := UploadOne(context.Background(), r, testTarget(), ws, "a/b.4gl", true,
		func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }); err != nil {
		t.Fatalf("dry-run 不该失败：%v", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("dry-run 不该执行任何命令，实际：%v", r.calls)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "/u1/toppord/a/b.4gl") {
		t.Fatalf("计划里应给出远程路径：%s", joined)
	}
}

func TestUploadOneScp(t *testing.T) {
	stubTools(t)
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "a", "b.4gl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recRunner{}
	if err := UploadOne(context.Background(), r, testTarget(), ws, "a/b.4gl", false, nil); err != nil {
		t.Fatalf("scp 上传失败：%v", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("应先 mkdir 再 scp，实际 %d 条：%v", len(r.calls), r.calls)
	}
	if !strings.HasPrefix(r.calls[0], "ssh ") || !strings.Contains(r.calls[0], "mkdir -p /u1/toppord/a") {
		t.Fatalf("第一条应是 ssh mkdir：%s", r.calls[0])
	}
	if !strings.HasPrefix(r.calls[1], "scp ") || !strings.Contains(r.calls[1], "tiptop@erp:/u1/toppord/a/b.4gl") {
		t.Fatalf("第二条应是 scp 到目标路径：%s", r.calls[1])
	}
}

func TestUploadOneMissingLocal(t *testing.T) {
	stubTools(t)
	ws := t.TempDir()
	if err := UploadOne(context.Background(), &recRunner{}, testTarget(), ws, "no/such.4gl", false, nil); err == nil {
		t.Fatal("本地文件不存在时应报错")
	}
}
