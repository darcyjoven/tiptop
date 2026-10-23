package deploy

// P1-5：部署配置目录从 `.bdl-lsp/` 迁到 `.project/`，并要能吃下**插件合成**的文件。

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadConfigAcceptsPluginGenerated 插件合成的文件带 createdBy/generatedAt/doc
// 元信息：解析用 DisallowUnknownFields，这些字段必须被认识（否则部署直接报错）。
func TestLoadConfigAcceptsPluginGenerated(t *testing.T) {
	root := writeConfig(t, `{
	  "createdBy": "vscode-bdl 0.5.5",
	  "generatedAt": "2026-10-23T12:00:00.000Z",
	  "doc": "由 VSCode 插件根据设置生成；请改设置，不要手改本文件。",
	  "defaultTarget": "prod",
	  "targets": {
	    "prod": {
	      "host": "h.example.com",
	      "port": 2222,
	      "user": "apps",
	      "remoteRoot": "/home/apps/tiptop",
	      "localRoot": ".",
	      "upload": {"mode": "rsync", "fallback": "scp", "delete": false},
	      "strictHostKey": true,
	      "compile": {"4gl": "fglcomp ${file}"}
	    }
	  }
	}`)
	cfg, err := LoadConfig(root, "")
	if err != nil {
		t.Fatalf("插件合成的配置应能加载: %v", err)
	}
	if cfg.CreatedBy != "vscode-bdl 0.5.5" {
		t.Errorf("createdBy 应被读入，实际 %q", cfg.CreatedBy)
	}
	if cfg.DefaultTarget != "prod" {
		t.Errorf("defaultTarget 应为 prod，实际 %q", cfg.DefaultTarget)
	}
	tgt := cfg.Targets["prod"]
	if tgt == nil || tgt.Port != 2222 || !tgt.strictHostKey() {
		t.Errorf("目标解析不对: %+v", tgt)
	}
	if got := tgt.Compile["4gl"]; got != "fglcomp ${file}" {
		t.Errorf("compile 模板不对: %q", got)
	}
}

// TestLoadConfigLegacyDirFallback 新目录没有文件、旧目录有 → 用旧目录（只读兼容）。
func TestLoadConfigLegacyDirFallback(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, LegacyConfigDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"targets": {"old": {"host": "h", "user": "u", "remoteRoot": "/u/app"}}}`
	if err := os.WriteFile(filepath.Join(dir, DefaultConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(root, "")
	if err != nil {
		t.Fatalf("旧目录应兼容: %v", err)
	}
	if cfg.Targets["old"] == nil {
		t.Fatalf("应从旧目录读到目标，实际 %+v", cfg.Targets)
	}
	if want := filepath.Join(dir, DefaultConfigName); cfg.Path != want {
		t.Errorf("Path 应指向旧目录文件 %q，实际 %q", want, cfg.Path)
	}
}

// TestLoadConfigPrefersNewDir 两个目录都有时用新目录。
func TestLoadConfigPrefersNewDir(t *testing.T) {
	root := t.TempDir()
	for dir, name := range map[string]string{DefaultConfigDir: "new", LegacyConfigDir: "old"} {
		d := filepath.Join(root, dir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"targets": {"` + name + `": {"host": "h", "user": "u", "remoteRoot": "/u/app"}}}`
		if err := os.WriteFile(filepath.Join(d, DefaultConfigName), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := LoadConfig(root, "")
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Targets["new"] == nil || cfg.Targets["old"] != nil {
		t.Errorf("应优先新目录，实际 %+v", cfg.Targets)
	}
}
