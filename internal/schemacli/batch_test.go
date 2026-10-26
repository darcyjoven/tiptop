package schemacli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChunkStrings 钉住分批切片的边界：不分批（size<=0）、恰好一批、以及余数批。
func TestChunkStrings(t *testing.T) {
	if got := chunkStrings(nil, 200); got != nil {
		t.Fatalf("空清单应为 nil，实际 %v", got)
	}
	if got := chunkStrings([]string{"A", "B"}, 200); len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("不超过一批时应为单个分片，实际 %v", got)
	}
	if got := chunkStrings([]string{"A", "B"}, 0); len(got) != 1 {
		t.Fatalf("size=0 表示不分批，应为单个分片，实际 %v", got)
	}
	list := make([]string, 450)
	for i := range list {
		list[i] = "T"
	}
	got := chunkStrings(list, 200)
	if len(got) != 3 || len(got[0]) != 200 || len(got[1]) != 200 || len(got[2]) != 50 {
		t.Fatalf("450 张表按 200 应分 3 批（200/200/50），实际 %v", got)
	}
}

// TestMergeCSVParts 钉住多批 CSV 合并：只保留第一份表头。
func TestMergeCSVParts(t *testing.T) {
	one := []byte("A,B\n1,2\n")
	two := []byte("A,B\n3,4\n")
	merged := string(mergeCSVParts([][]byte{one, two}))
	if merged != "A,B\n1,2\n3,4\n" {
		t.Fatalf("合并结果不对：%q", merged)
	}
	if string(mergeCSVParts([][]byte{one})) != string(one) {
		t.Fatal("单批应原样返回")
	}
	if mergeCSVParts(nil) != nil {
		t.Fatal("空输入应返回 nil")
	}
}

// TestDefaultTableListQuery 表清单查询必须与明细查询同源（DS.ZTA_FILE），
// 且固定 zta02='ds'、按 zta01 原名排序。
func TestDefaultTableListQuery(t *testing.T) {
	q := DefaultTableListQuery()
	if !strings.Contains(q, "ds.zta_file") || !strings.Contains(q, "DISTINCT zta01") {
		t.Fatalf("清单查询不对：%s", q)
	}
	if !strings.Contains(q, "zta02 = 'ds'") || !strings.Contains(q, "ORDER BY zta01") {
		t.Fatalf("zta02 固定与排序不对：%s", q)
	}
}

// TestCleanupTempFiles 只删 `.tmp-*`，不动正式资料文件。
func TestCleanupTempFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".tmp-IMA_FILE-123", ".tmp-#T28_SID-9", "IMA_FILE.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	n, err := cleanupTempFiles(dir)
	if err != nil {
		t.Fatalf("cleanupTempFiles: %v", err)
	}
	if n != 2 {
		t.Fatalf("应删 2 个临时文件，实际 %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "IMA_FILE.json")); err != nil {
		t.Fatalf("正式表文件不应被删：%v", err)
	}
	// 目录不存在不算错误（首次导入）
	if n2, err := cleanupTempFiles(filepath.Join(dir, "nope")); err != nil || n2 != 0 {
		t.Fatalf("不存在的目录应返回 0,nil，实际 %d,%v", n2, err)
	}
}
