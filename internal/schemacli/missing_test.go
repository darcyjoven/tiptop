package schemacli

// `schema sync -missing`：只补本地缺失的表，并在报告里列出新增的表名。
//
// 真正连库那段（oradb）在 CI 里无法验证，所以这里覆盖可测的部分：
//   - 本地表集合（索引 ∪ tables/*.json）的口径；
//   - 从数据库表清单里挑缺失表（大小写不敏感）；
//   - 报告里的「新增表」行；
//   - `-source file -missing` 的 CLI 全链路（按 -only-new 处理）；
//   - `-missing` 与 `-prune` 的非法组合。

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalTableKeys(t *testing.T) {
	dir := t.TempDir()
	if _, err := Import(ImportOptions{CSVData: []byte(syncCSV), DataDir: dir, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	// 索引里没有、但表文件存在的表也算「本地已有」：手工放进来的资料
	// 不能被当成缺失再查一遍、更不该被当成新表报出来。
	writeFileT(t, filepath.Join(dir, TablesSubDir, "zzz_file.json"), "{}\n")

	keys, err := localTableKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AAA_FILE", "BBB_FILE", "ZZZ_FILE"} {
		if !keys[want] {
			t.Errorf("本地表集合应包含 %s：%v", want, keys)
		}
	}
	if keys["CCC_FILE"] {
		t.Errorf("CCC_FILE 不在本地，不该出现：%v", keys)
	}
}

func TestMissingTables(t *testing.T) {
	local := map[string]bool{"AAA_FILE": true, "ZZZ_FILE": true}
	got := missingTables(local, []string{"aaa_file", "bbb_file", "zzz_file"})
	if len(got) != 1 || got[0] != "bbb_file" {
		t.Errorf("应只挑出 bbb_file，实际 %v", got)
	}
	if got := missingTables(local, []string{"AAA_FILE", "ZZZ_FILE"}); len(got) != 0 {
		t.Errorf("都在本地时不该有缺失：%v", got)
	}
	if got := missingTables(nil, nil); len(got) != 0 {
		t.Errorf("空清单应返回空：%v", got)
	}
}

// TestNewTablesReported 报告里要能读出「新增表」这一行（插件按它告诉用户加了哪些表）。
func TestNewTablesReported(t *testing.T) {
	dir := t.TempDir()
	res, err := Import(ImportOptions{CSVData: []byte(syncCSV), DataDir: dir, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NewTables) != 2 || res.NewTables[0] != "aaa_file" || res.NewTables[1] != "bbb_file" {
		t.Fatalf("新增表名不对：%v", res.NewTables)
	}
	var buf bytes.Buffer
	printImportReport(&buf, "schema 同步", res, "test", 0, fixedNow, false)
	if !strings.Contains(buf.String(), "新增表          aaa_file, bbb_file") {
		t.Errorf("报告缺少新增表清单：\n%s", buf.String())
	}
}

// TestSyncMissingFromFileCLI `-source file -missing`：本地已有的表一个字节都不动，
// 只补 CSV 里新出现的表，并把它列在报告里。
func TestSyncMissingFromFileCLI(t *testing.T) {
	root := t.TempDir()
	csvPath := filepath.Join(root, DefaultDataDir, "gal_schema.csv")
	writeFileT(t, csvPath, syncCSV)

	var o1, e1 bytes.Buffer
	if code := run([]string{"sync", "-source", "file", "-root", root, "-missing"}, &o1, &e1); code != 0 {
		t.Fatalf("首次退出码 %d：%s", code, e1.String())
	}
	if !strings.Contains(o1.String(), "新增表          aaa_file, bbb_file") {
		t.Errorf("首次应列出新增表：\n%s", o1.String())
	}

	dir := filepath.Join(root, DefaultDataDir, DefaultSchemaDir)
	before := readBytesT(t, filepath.Join(dir, TablesSubDir, "AAA_FILE.json"))

	// 再跑一次：两张都在本地 → 新增 0，且不写盘
	var o2, e2 bytes.Buffer
	if code := run([]string{"sync", "-source", "file", "-root", root, "-missing"}, &o2, &e2); code != 0 {
		t.Fatalf("第二次退出码 %d：%s", code, e2.String())
	}
	if out := o2.String(); !strings.Contains(out, "新增 0") || strings.Contains(out, "新增表") {
		t.Errorf("没有缺失时报告应是新增 0、且不列新增表：\n%s", out)
	}
	if after := readBytesT(t, filepath.Join(dir, TablesSubDir, "AAA_FILE.json")); !bytes.Equal(before, after) {
		t.Error("已有表文件不该被改写")
	}

	// CSV 里多出一张 ccc_file → 只补这一张
	writeFileT(t, csvPath, syncCSV+"ccc_file,ccc01,备注,VARCHAR2,10,10,,,Y,0\n")
	var o3, e3 bytes.Buffer
	if code := run([]string{"sync", "-source", "file", "-root", root, "-missing"}, &o3, &e3); code != 0 {
		t.Fatalf("第三次退出码 %d：%s", code, e3.String())
	}
	if !strings.Contains(o3.String(), "新增表          ccc_file") {
		t.Errorf("应只列出 ccc_file：\n%s", o3.String())
	}
	if _, err := os.Stat(filepath.Join(dir, TablesSubDir, "CCC_FILE.json")); err != nil {
		t.Errorf("ccc 的表文件应已写出：%v", err)
	}
}

// TestOnlyTablesCaseInsensitive 覆盖 -missing 的真实落点：数据库表清单是小写，
// 而索引键/OnlyTables 比较是大写，写错一侧就会「过滤条件有值却一张表都没处理」。
func TestOnlyTablesCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	res, err := Import(ImportOptions{
		CSVData:    []byte(syncCSV),
		DataDir:    dir,
		OnlyTables: []string{" aaa_file "},
		OnlyNew:    true,
		Now:        fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TablesNew != 1 || len(res.NewTables) != 1 || res.NewTables[0] != "aaa_file" {
		t.Fatalf("应只写 aaa_file：%+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, TablesSubDir, "BBB_FILE.json")); !os.IsNotExist(err) {
		t.Errorf("bbb_file 不在 OnlyTables 里，不该写：%v", err)
	}
}

func TestMissingRejectsPrune(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"sync", "-source", "file", "-root", root, "-missing", "-prune"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("-missing 与 -prune 同时给出应退出码 2，实际 %d（stderr=%s）", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "-missing") {
		t.Errorf("错误信息应提到 -missing：%s", stderr.String())
	}
}
