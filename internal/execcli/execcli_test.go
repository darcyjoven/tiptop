package execcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkWorkspace 造一个最小工作区：.project/deploy/deploy.json + 一个源文件。
func mkWorkspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	dir := filepath.Join(ws, ".project", "deploy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"targets":{"prod":{"host":"erp","port":22,"user":"tiptop",` +
		`"remoteRoot":"/u1/toppord","localRoot":".",` +
		`"upload":{"mode":"scp","fallback":"none"},"strictHostKey":true}},"defaultTarget":"prod"}`
	if err := os.WriteFile(filepath.Join(dir, "deploy.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "a", "b.4gl"), []byte("MAIN\nEND MAIN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return ws
}

// 没有配置时要给出可读错误，而不是崩。
func TestRunNoConfig(t *testing.T) {
	ws := t.TempDir()
	if code := Run([]string{"upload", "-workspace", ws, "-file", "a/b.4gl", "-dry-run"}, "test"); code == 0 {
		t.Fatal("没有部署配置时应失败")
	}
}

// upload -dry-run 只打印计划、不连网。
func TestUploadDryRun(t *testing.T) {
	ws := mkWorkspace(t)
	if code := Run([]string{"upload", "-workspace", ws, "-file", "a/b.4gl", "-dry-run", "-json"}, "test"); code != 0 {
		t.Fatalf("dry-run 应成功，退出码 %d", code)
	}
}

// download -dry-run 同样只打印计划；缺 -file 要报错。
func TestDownloadDryRun(t *testing.T) {
	ws := mkWorkspace(t)
	if code := Run([]string{"download", "-workspace", ws, "-file", "a/b.4gl", "-dry-run", "-json"}, "test"); code != 0 {
		t.Fatalf("download dry-run 应成功，退出码 %d", code)
	}
	if code := Run([]string{"download", "-workspace", ws, "-dry-run"}, "test"); code == 0 {
		t.Fatal("缺 -file 应报错")
	}
}

// exec -dry-run 打印要执行的命令；缺 -command 要报错。
func TestExecDryRun(t *testing.T) {
	ws := mkWorkspace(t)
	if code := Run([]string{"exec", "-workspace", ws, "-command", "r.l2 axcp500", "-dry-run"}, "test"); code != 0 {
		t.Fatalf("exec dry-run 应成功，退出码 %d", code)
	}
	if code := Run([]string{"exec", "-workspace", ws, "-dry-run"}, "test"); code == 0 {
		t.Fatal("缺 -command 应报错")
	}
}

// 越出工作区的文件路径要被拒绝。
func TestUploadRejectsEscape(t *testing.T) {
	ws := mkWorkspace(t)
	if code := Run([]string{"upload", "-workspace", ws, "-file", "../x.4gl", "-dry-run"}, "test"); code == 0 {
		t.Fatal("越界路径应被拒绝")
	}
	if !strings.Contains("", "") {
		t.Fatal("unreachable")
	}
}
