package schemacli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedNow 是本地时区（+08:00）的一个固定时刻，用来断言 importedAt 的格式。
var fixedNow = time.Date(2026, 10, 23, 21, 0, 0, 0, time.FixedZone("CST", 8*3600))

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录 %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写 %s: %v", path, err)
	}
}

func readBytesT(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	return b
}

func decodeIndex(t *testing.T, dir string) Index {
	t.Helper()
	var idx Index
	if err := json.Unmarshal(readBytesT(t, filepath.Join(dir, IndexFileName)), &idx); err != nil {
		t.Fatalf("解析 %s: %v", IndexFileName, err)
	}
	return idx
}

func decodeTable(t *testing.T, dir, key string) TableFile {
	t.Helper()
	var tf TableFile
	if err := json.Unmarshal(readBytesT(t, tablePath(dir, key)), &tf); err != nil {
		t.Fatalf("解析表 %s: %v", key, err)
	}
	return tf
}

func mustImport(t *testing.T, opts ImportOptions) *ImportResult {
	t.Helper()
	if opts.Now.IsZero() {
		opts.Now = fixedNow
	}
	res, err := Import(opts)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return res
}

// assertKeyOrder 断言 JSON 文本里这些键按给定顺序首次出现。
func assertKeyOrder(t *testing.T, s string, keys ...string) {
	t.Helper()
	prev := -1
	for _, k := range keys {
		i := strings.Index(s, k)
		if i < 0 {
			t.Fatalf("JSON 里缺少 %s：\n%s", k, s)
		}
		if i < prev {
			t.Errorf("JSON 键顺序不对：%s 出现在前一个键之前：\n%s", k, s)
		}
		prev = i
	}
}

// TestImportWritesStructure 覆盖要求 2：写出结构与字段。
func TestImportWritesStructure(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "gal_schema.csv")
	writeFileT(t, csvPath, headerMisspelled+
		"ima_file,ima01,\"物料,编号\",CHAR,0,10,0,0,Y,0\r\n"+
		"ima_file,ima02,数量,NUMBER,22,0,5,2,N,4\r\n")
	dataDir := filepath.Join(dir, ".project", ".schema")

	res := mustImport(t, ImportOptions{CSVPath: csvPath, DataDir: dataDir})
	if res.DataDir != dataDir {
		t.Errorf("DataDir = %q，期望 %q", res.DataDir, dataDir)
	}
	if res.RowsTotal != 2 || res.Columns != 2 || res.TablesNew != 1 {
		t.Errorf("统计不对：行 %d 列 %d 新增 %d", res.RowsTotal, res.Columns, res.TablesNew)
	}

	// ---- tables.json ----
	idx := decodeIndex(t, dataDir)
	if idx.Schema != 1 {
		t.Errorf("schema = %d，期望 1", idx.Schema)
	}
	if idx.ImportedAt != "2026-10-23T21:00:00+08:00" {
		t.Errorf("importedAt = %q，期望 RFC3339 本地时间", idx.ImportedAt)
	}
	if idx.Source != "gal_schema.csv" {
		t.Errorf("source = %q，期望 CSV 基线名", idx.Source)
	}
	if idx.RowsTotal != 2 {
		t.Errorf("rowsTotal = %d，期望 2", idx.RowsTotal)
	}
	entry, ok := idx.Tables["IMA_FILE"]
	if !ok {
		t.Fatalf("索引缺 IMA_FILE，实际键：%v", keysOf(idx.Tables))
	}
	if entry.Name != "ima_file" || entry.Desc != "" || entry.Columns != 2 || entry.File != "IMA_FILE.json" {
		t.Errorf("索引条目不对：%+v", entry)
	}
	if len(idx.Tables) != 1 {
		t.Errorf("索引表数 = %d，期望 1", len(idx.Tables))
	}

	// ---- tables/IMA_FILE.json ----
	tf := decodeTable(t, dataDir, "IMA_FILE")
	if tf.Table != "IMA_FILE" || tf.Desc != "" {
		t.Errorf("表文件头不对：%+v", tf)
	}
	want := []Column{
		{Name: "ima01", Desc: "物料,编号", Type: "CHAR", Length: 10, CharLength: 10, Nullable: true, DefaultLength: "0"},
		{Name: "ima02", Desc: "数量", Type: "NUMBER", Length: 22, CharLength: 0, Precision: 5, Scale: 2, Nullable: false, DefaultLength: "4"},
	}
	if len(tf.Columns) != 2 {
		t.Fatalf("列数 = %d，期望 2", len(tf.Columns))
	}
	for i := range want {
		if tf.Columns[i] != want[i] {
			t.Errorf("第 %d 列 = %+v，期望 %+v", i, tf.Columns[i], want[i])
		}
	}

	// 键顺序必须与规约一致（bdl-lsp 按同一形状读）。
	rawIdx := string(readBytesT(t, filepath.Join(dataDir, IndexFileName)))
	assertKeyOrder(t, rawIdx, `"schema"`, `"importedAt"`, `"source"`, `"rowsTotal"`, `"tables"`)
	rawTbl := string(readBytesT(t, tablePath(dataDir, "IMA_FILE")))
	assertKeyOrder(t, rawTbl, `"table"`, `"desc"`, `"columns"`)
	colsJSON := rawTbl[strings.Index(rawTbl, `"columns"`):]
	assertKeyOrder(t, colsJSON, `"name"`, `"desc"`, `"type"`, `"length"`, `"charLength"`,
		`"precision"`, `"scale"`, `"nullable"`, `"defaultLength"`)

	// 原子写：不能留下临时文件。
	entries, err := os.ReadDir(filepath.Join(dataDir, TablesSubDir))
	if err != nil {
		t.Fatalf("读表目录: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("留下临时文件 %s", e.Name())
		}
	}
}

func keysOf[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestImportIncremental 覆盖要求 3 的默认全量语义：
// CSV 里改过的表重写，没改的表字节与 mtime 都不变，CSV 里没有的表保留。
func TestImportIncremental(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	csv1 := filepath.Join(dir, "one.csv")
	writeFileT(t, csv1, headerMisspelled+
		"aaa_file,aaa01,编号,CHAR,10,10,0,0,Y,0\r\n"+
		"aaa_file,aaa02,名称,CHAR,20,20,0,0,Y,0\r\n"+
		"bbb_file,bbb01,编号,CHAR,5,5,0,0,N,0\r\n"+
		"ccc_file,ccc01,备注,CHAR,30,30,0,0,Y,0\r\n")
	mustImport(t, ImportOptions{CSVPath: csv1, DataDir: dataDir})

	aaaBefore := readBytesT(t, tablePath(dataDir, "AAA_FILE"))
	bbbBefore := readBytesT(t, tablePath(dataDir, "BBB_FILE"))
	bbbStatBefore, err := os.Stat(tablePath(dataDir, "BBB_FILE"))
	if err != nil {
		t.Fatal(err)
	}

	// 第二份：AAA 多一列；BBB 一模一样；CCC 不出现。
	csv2 := filepath.Join(dir, "two.csv")
	writeFileT(t, csv2, headerMisspelled+
		"aaa_file,aaa01,编号,CHAR,10,10,0,0,Y,0\r\n"+
		"aaa_file,aaa02,名称,CHAR,20,20,0,0,Y,0\r\n"+
		"aaa_file,aaa03,新增,CHAR,1,1,0,0,Y,0\r\n"+
		"bbb_file,bbb01,编号,CHAR,5,5,0,0,N,0\r\n")
	res := mustImport(t, ImportOptions{CSVPath: csv2, DataDir: dataDir})
	if res.TablesChanged != 1 || res.TablesSame != 1 || res.TablesKept != 1 || res.TablesNew != 0 {
		t.Errorf("统计不对：新增 %d 变更 %d 未变 %d 保留 %d，期望 0/1/1/1",
			res.TablesNew, res.TablesChanged, res.TablesSame, res.TablesKept)
	}

	if bytes.Equal(aaaBefore, readBytesT(t, tablePath(dataDir, "AAA_FILE"))) {
		t.Error("AAA 的列变了，表文件应该被重写")
	}
	if !bytes.Equal(bbbBefore, readBytesT(t, tablePath(dataDir, "BBB_FILE"))) {
		t.Error("BBB 没变，表文件内容不应改变")
	}
	bbbStatAfter, err := os.Stat(tablePath(dataDir, "BBB_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	if !bbbStatAfter.ModTime().Equal(bbbStatBefore.ModTime()) {
		t.Errorf("BBB 没变，不应重写（mtime %v → %v）",
			bbbStatBefore.ModTime(), bbbStatAfter.ModTime())
	}

	idx := decodeIndex(t, dataDir)
	ccc, ok := idx.Tables["CCC_FILE"]
	if !ok {
		t.Fatalf("CCC 不在新 CSV 里，应该保留；现有键：%v", keysOf(idx.Tables))
	}
	if ccc.Columns != 1 {
		t.Errorf("保留的 CCC 条目被改动了：%+v", ccc)
	}
	if got := decodeTable(t, dataDir, "CCC_FILE"); len(got.Columns) != 1 {
		t.Errorf("保留的 CCC 表文件被改动了：%+v", got)
	}
	// 索引的发布时间与来源要更新到本轮。
	if idx.Source != "two.csv" || idx.RowsTotal != 4 {
		t.Errorf("索引未更新：source=%q rowsTotal=%d", idx.Source, idx.RowsTotal)
	}
}

// TestImportOnlyNew 覆盖要求 3 的 -only-new：已有表一个字节都不动。
func TestImportOnlyNew(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	csv1 := filepath.Join(dir, "one.csv")
	writeFileT(t, csv1, headerMisspelled+
		"aaa_file,aaa01,编号,CHAR,10,10,0,0,Y,0\r\n"+
		"bbb_file,bbb01,编号,CHAR,5,5,0,0,N,0\r\n")
	mustImport(t, ImportOptions{CSVPath: csv1, DataDir: dataDir})

	aaaBefore := readBytesT(t, tablePath(dataDir, "AAA_FILE"))
	bbbBefore := readBytesT(t, tablePath(dataDir, "BBB_FILE"))
	idxBefore := readBytesT(t, filepath.Join(dataDir, IndexFileName))

	// 第二份：AAA 被改成 2 列（只该被忽略），再添一张新表 DDD。
	csv2 := filepath.Join(dir, "two.csv")
	writeFileT(t, csv2, headerMisspelled+
		"aaa_file,aaa01,编号,CHAR,10,10,0,0,Y,0\r\n"+
		"aaa_file,aaa99,人工修订过的说明,CHAR,1,1,0,0,Y,0\r\n"+
		"bbb_file,bbb01,编号,CHAR,5,5,0,0,N,0\r\n"+
		"ddd_file,ddd01,新增表,CHAR,7,7,0,0,N,0\r\n")
	res := mustImport(t, ImportOptions{CSVPath: csv2, DataDir: dataDir, OnlyNew: true})
	if res.TablesNew != 1 || res.TablesSame != 2 || res.TablesChanged != 0 {
		t.Errorf("统计不对：新增 %d 变更 %d 未变 %d，期望 1/0/2",
			res.TablesNew, res.TablesChanged, res.TablesSame)
	}
	if !bytes.Equal(aaaBefore, readBytesT(t, tablePath(dataDir, "AAA_FILE"))) {
		t.Error("-only-new 下已有的 AAA 表文件必须一个字节都不动")
	}
	if !bytes.Equal(bbbBefore, readBytesT(t, tablePath(dataDir, "BBB_FILE"))) {
		t.Error("-only-new 下已有的 BBB 表文件必须一个字节都不动")
	}
	if bytes.Equal(idxBefore, readBytesT(t, filepath.Join(dataDir, IndexFileName))) {
		t.Error("索引应记录本轮导入（importedAt/source 会变）")
	}
	if _, err := os.Stat(tablePath(dataDir, "DDD_FILE")); err != nil {
		t.Errorf("-only-new 下新表 DDD 应写出：%v", err)
	}
	if got := decodeTable(t, dataDir, "AAA_FILE"); len(got.Columns) != 1 {
		t.Errorf("已有的 AAA 表文件被覆盖了：%+v", got)
	}

	// 表文件被删掉（索引还残留）时，-only-new 应把它补写回来；
	// 该表不属于"已有表文件"，所以不算违反"已有表一律不动"。
	if err := os.Remove(tablePath(dataDir, "BBB_FILE")); err != nil {
		t.Fatal(err)
	}
	mustImport(t, ImportOptions{CSVPath: csv2, DataDir: dataDir, OnlyNew: true})
	if !bytes.Equal(bbbBefore, readBytesT(t, tablePath(dataDir, "BBB_FILE"))) {
		t.Error("-only-new 应把被删掉的表文件补写回来（内容与原来一致）")
	}
	if !bytes.Equal(aaaBefore, readBytesT(t, tablePath(dataDir, "AAA_FILE"))) {
		t.Error("-only-new 下已有的 AAA 表文件仍必须一个字节都不动")
	}
}

// TestImportTablesFilter 覆盖要求 4：-tables 只影响指定表。
func TestImportTablesFilter(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	csv1 := filepath.Join(dir, "one.csv")
	writeFileT(t, csv1, headerMisspelled+
		"aaa_file,aaa01,编号,CHAR,10,10,0,0,Y,0\r\n"+
		"bbb_file,bbb01,编号,CHAR,5,5,0,0,N,0\r\n"+
		"ccc_file,ccc01,备注,CHAR,30,30,0,0,Y,0\r\n")
	mustImport(t, ImportOptions{CSVPath: csv1, DataDir: dataDir})

	aaaBefore := readBytesT(t, tablePath(dataDir, "AAA_FILE"))
	bbbBefore := readBytesT(t, tablePath(dataDir, "BBB_FILE"))

	csv2 := filepath.Join(dir, "two.csv")
	writeFileT(t, csv2, headerMisspelled+
		"aaa_file,aaa01,编号,CHAR,10,10,0,0,Y,0\r\n"+
		"aaa_file,aaa02,新说明,CHAR,1,1,0,0,Y,0\r\n"+
		"bbb_file,bbb01,编号2,CHAR,5,5,0,0,N,0\r\n")
	res := mustImport(t, ImportOptions{CSVPath: csv2, DataDir: dataDir, OnlyTables: []string{"AAA_FILE"}})
	if res.TablesChanged != 1 || res.TablesSame != 0 || res.TablesNew != 0 || res.TablesKept != 2 {
		t.Errorf("统计不对：新增 %d 变更 %d 未变 %d 保留 %d，期望 0/1/0/2",
			res.TablesNew, res.TablesChanged, res.TablesSame, res.TablesKept)
	}
	if bytes.Equal(aaaBefore, readBytesT(t, tablePath(dataDir, "AAA_FILE"))) {
		t.Error("AAA 在 -tables 里，应被重写")
	}
	if !bytes.Equal(bbbBefore, readBytesT(t, tablePath(dataDir, "BBB_FILE"))) {
		t.Error("BBB 不在 -tables 里，应保持原样")
	}
}

// TestDryRunWritesNothing 覆盖要求 5：dry-run 不建目录、不写盘。
func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "a.csv")
	writeFileT(t, csvPath, headerMisspelled+"aaa_file,aaa01,编号,CHAR,10,10,0,0,Y,0\r\n")
	dataDir := filepath.Join(dir, "data")

	res := mustImport(t, ImportOptions{CSVPath: csvPath, DataDir: dataDir, DryRun: true})
	if res.TablesNew != 1 || res.Columns != 1 || res.RowsTotal != 1 {
		t.Errorf("dry-run 也要给出统计：%+v", res)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Errorf("dry-run 不应创建目录 %s（err=%v）", dataDir, err)
	}

	// 已有资料时 dry-run 不能改动任何文件。
	mustImport(t, ImportOptions{CSVPath: csvPath, DataDir: dataDir})
	idxBefore := readBytesT(t, filepath.Join(dataDir, IndexFileName))
	tblBefore := readBytesT(t, tablePath(dataDir, "AAA_FILE"))
	mustImport(t, ImportOptions{CSVPath: csvPath, DataDir: dataDir, DryRun: true})
	if !bytes.Equal(idxBefore, readBytesT(t, filepath.Join(dataDir, IndexFileName))) {
		t.Error("dry-run 改动了 tables.json")
	}
	if !bytes.Equal(tblBefore, readBytesT(t, tablePath(dataDir, "AAA_FILE"))) {
		t.Error("dry-run 改动了表文件")
	}
}

// TestCorruptIndexKeepsFile 覆盖要求 6：坏索引报错，且原文件仍在。
func TestCorruptIndexKeepsFile(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	const broken = "{ 这不是 JSON"
	writeFileT(t, filepath.Join(dataDir, IndexFileName), broken)
	csvPath := filepath.Join(dir, "a.csv")
	writeFileT(t, csvPath, headerMisspelled+"aaa_file,aaa01,编号,CHAR,10,10,0,0,Y,0\r\n")

	if _, err := Import(ImportOptions{CSVPath: csvPath, DataDir: dataDir}); err == nil {
		t.Fatal("坏索引应报错退出")
	}
	if got := string(readBytesT(t, filepath.Join(dataDir, IndexFileName))); got != broken {
		t.Errorf("坏索引原文件必须保留，实际 %q", got)
	}
	if _, err := os.Stat(filepath.Join(dataDir, TablesSubDir)); !os.IsNotExist(err) {
		t.Errorf("报错时不应写出任何表文件（err=%v）", err)
	}

	// CLI 也要以退出码 1 失败（而不是清空重写）。
	var out, errOut bytes.Buffer
	code := run([]string{"import", "-csv", csvPath, "-out", dataDir}, &out, &errOut)
	if code != 1 {
		t.Errorf("CLI 退出码 = %d，期望 1", code)
	}
	if got := string(readBytesT(t, filepath.Join(dataDir, IndexFileName))); got != broken {
		t.Errorf("CLI 报错后坏索引原文件必须保留，实际 %q", got)
	}
}

// TestCLIReportAndStatus 校验给插件看的中文报告形状与 status。
func TestCLIReportAndStatus(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "gal_schema.csv")
	writeFileT(t, csvPath, headerMisspelled+
		"ima_file,ima01,编号,CHAR,10,10,0,0,Y,0\r\n"+
		"ima_file,ima02,名称,CHAR,20,20,0,0,Y,0\r\n")

	// 没资料时 status 退出码 1，并给出提示。
	var out, errOut bytes.Buffer
	if code := run([]string{"status", "-root", dir}, &out, &errOut); code != 1 {
		t.Errorf("没有资料时 status 退出码 = %d，期望 1", code)
	}
	if !strings.Contains(out.String(), "没有资料") {
		t.Errorf("status 应说明没有资料：%q", out.String())
	}

	// 导入：默认数据目录 <root>/.project/schema（W6）。
	out.Reset()
	errOut.Reset()
	if code := run([]string{"import", "-csv", csvPath, "-root", dir}, &out, &errOut); code != 0 {
		t.Fatalf("import 退出码 = %d，stderr=%s", code, errOut.String())
	}
	s := out.String()
	for _, want := range []string{"== schema 导入 ==", "CSV 行数", "表", "列", "数据目录", "耗时"} {
		if !strings.Contains(s, want) {
			t.Errorf("报告缺少 %q：\n%s", want, s)
		}
	}
	if !strings.Contains(s, filepath.Join(".project", "schema")) {
		t.Errorf("报告里的数据目录应是 <root>/.project/schema：\n%s", s)
	}

	// status：表数/列数/导入时间/来源/数据目录。
	out.Reset()
	errOut.Reset()
	if code := run([]string{"status", "-root", dir}, &out, &errOut); code != 0 {
		t.Fatalf("status 退出码 = %d，stderr=%s", code, errOut.String())
	}
	s = out.String()
	for _, want := range []string{"表", "列", "导入时间", "来源", "gal_schema.csv", "数据目录"} {
		if !strings.Contains(s, want) {
			t.Errorf("status 缺少 %q：\n%s", want, s)
		}
	}
}

// TestCLITablesCaseInsensitiveAndDryRunLine：-tables 表名大小写不敏感，
// dry-run 报告末尾有约定的一行。
func TestCLITablesCaseInsensitiveAndDryRunLine(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	csv1 := filepath.Join(dir, "one.csv")
	writeFileT(t, csv1, headerMisspelled+
		"aaa_file,aaa01,编号,CHAR,10,10,0,0,Y,0\r\n"+
		"bbb_file,bbb01,编号,CHAR,5,5,0,0,N,0\r\n")
	var out, errOut bytes.Buffer
	if code := run([]string{"import", "-csv", csv1, "-out", dataDir}, &out, &errOut); code != 0 {
		t.Fatalf("首次 import 失败：%s", errOut.String())
	}
	aaaBefore := readBytesT(t, tablePath(dataDir, "AAA_FILE"))
	bbbBefore := readBytesT(t, tablePath(dataDir, "BBB_FILE"))

	// 小写表名 + -only-new：只在给定表里挑"本地不存在的"，所以两张都不动。
	csv2 := filepath.Join(dir, "two.csv")
	writeFileT(t, csv2, headerMisspelled+
		"aaa_file,aaa01,编号2,CHAR,10,10,0,0,Y,0\r\n"+
		"ccc_file,ccc01,新表,CHAR,1,1,0,0,Y,0\r\n")
	out.Reset()
	errOut.Reset()
	code := run([]string{"import", "-csv", csv2, "-out", dataDir,
		"-tables", "aaa_FILE, ccc_file", "-only-new"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("import 失败：%s", errOut.String())
	}
	if !bytes.Equal(aaaBefore, readBytesT(t, tablePath(dataDir, "AAA_FILE"))) {
		t.Error("-only-new 下 AAA 应保持原样")
	}
	if !bytes.Equal(bbbBefore, readBytesT(t, tablePath(dataDir, "BBB_FILE"))) {
		t.Error("-tables 之外的表应保持原样")
	}
	if _, err := os.Stat(tablePath(dataDir, "CCC_FILE")); err != nil {
		t.Errorf("-tables 里本地不存在的 CCC 应被写出：%v", err)
	}

	// dry-run 报告末尾一行。
	out.Reset()
	errOut.Reset()
	if code := run([]string{"import", "-csv", csv2, "-out", dataDir, "-dry-run"}, &out, &errOut); code != 0 {
		t.Fatalf("dry-run 失败：%s", errOut.String())
	}
	if !strings.Contains(out.String(), "（dry-run：未写盘）") {
		t.Errorf("dry-run 报告缺少结尾行：\n%s", out.String())
	}
}
