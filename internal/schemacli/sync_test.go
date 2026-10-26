package schemacli

// P2-1 补：`schema sync`（直连数据库 / 读 CSV）与 `-prune`。
//
// 真实 Oracle 连接在 CI 里无法验证（沙箱没有库），所以这里覆盖：
//   - SQL 生成（owner/表过滤/单引号转义/排序）；
//   - "DB 查出来的字节"与"CSV 文件"走同一条 Import 路径（用 CSVData 直接喂）；
//   - `schema sync -source file` 的 CLI 全链路；
//   - `-prune` 的删除语义与非法组合。
// 真正连库那段（oradb.QueryCSV）由 internal/oradb 的单元测试覆盖参数与密码来源，
// 查询本身是标准 ALL_TAB_COLUMNS/ALL_COL_COMMENTS 的两表左连接。

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const syncCSV = `table_name,column_name,column_desc,data_type,data_length,char_length,data_precicison,data_scale,nullable,default_legth
aaa_file,aaa01,编号,VARCHAR2,40,40,,,N,0
aaa_file,aaa02,名称,VARCHAR2,120,120,,,Y,0
bbb_file,bbb01,数量,NUMBER,22,0,5,2,N,4
`

func TestDefaultSchemaQuery(t *testing.T) {
	q := DefaultSchemaQuery(nil)
	for _, want := range []string{
		"ds.zta_file", "ds.gaq_file", "user_tab_columns",
		"gaq02 = '2'", "zta02 = 'ds'", "table_name = UPPER(zta01)",
		"LOWER(column_name)", "NVL(gaq03, '')",
		"ORDER BY table_name, column_name",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("TiTop 字典 SQL 缺少 %q：\n%s", want, q)
		}
	}
	// 不能再出现 Oracle 备注来源（用户环境里没维护）
	for _, bad := range []string{"all_col_comments", "all_tab_columns", "cc.comments"} {
		if strings.Contains(q, bad) {
			t.Errorf("默认 SQL 不应再用 %q：\n%s", bad, q)
		}
	}
	// 分批：zta01 用清单里的原始写法（TiTop 是小写），单引号要转义
	q2 := DefaultSchemaQuery([]string{"aaa_file", "bbb_file"})
	if !strings.Contains(q2, "AND zta01 IN ('aaa_file', 'bbb_file')") {
		t.Errorf("分批条件不对：\n%s", q2)
	}
	q3 := DefaultSchemaQuery([]string{"o'brien"})
	if !strings.Contains(q3, "'o''brien'") {
		t.Errorf("单引号应转义：\n%s", q3)
	}
	// 列名/顺序必须与导出 CSV 一致（下游 parseCSV 靠它）
	for _, col := range []string{"table_name", "column_name", "column_desc", "data_type",
		"data_length", "char_length", "data_precision", "data_scale", "nullable", "default_length"} {
		if !strings.Contains(q, col) {
			t.Errorf("SQL 应选出 %q（与导出 CSV 对齐）", col)
		}
	}
}

// TestDictMissingHint 字典表缺失（ORA-00942）要说人话：提示改用 CSV 导入。
func TestDictMissingHint(t *testing.T) {
	if got := dictMissingHint(nil); got != "" {
		t.Errorf("nil 错误不该给提示：%q", got)
	}
	if got := dictMissingHint(errors.New("ORA-00900: invalid SQL")); got != "" {
		t.Errorf("其它 ORA 错误不该给 CSV 提示：%q", got)
	}
	got := dictMissingHint(errors.New("查询失败: ORA-00942: table or view does not exist"))
	if !strings.Contains(got, "CSV 导入") {
		t.Errorf("ORA-00942 应提示改用 CSV 导入：%q", got)
	}
}

func TestImportFromCSVDataEqualsFile(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "gal_schema.csv")
	writeFileT(t, csvPath, syncCSV)

	// 1) 从文件
	dirA := t.TempDir()
	resA, err := Import(ImportOptions{CSVPath: csvPath, DataDir: dirA, Now: fixedNow, Source: "file.csv"})
	if err != nil {
		t.Fatal(err)
	}
	// 2) 从内存字节（DB 采集走的就是这条）
	dirB := t.TempDir()
	resB, err := Import(ImportOptions{CSVData: []byte(syncCSV), DataDir: dirB, Now: fixedNow, Source: "golang@db:1521/svc owner=APP"})
	if err != nil {
		t.Fatal(err)
	}
	if resA.RowsTotal != resB.RowsTotal || resA.TablesNew != resB.TablesNew || resA.Columns != resB.Columns {
		t.Errorf("两种输入统计应一致：%+v vs %+v", resA, resB)
	}
	for _, name := range []string{"AAA_FILE.json", "BBB_FILE.json"} {
		a := readBytesT(t, filepath.Join(dirA, TablesSubDir, name))
		b := readBytesT(t, filepath.Join(dirB, TablesSubDir, name))
		if !bytes.Equal(a, b) {
			t.Errorf("%s 内容应逐字节一致", name)
		}
	}
	// 来源写进索引
	if src := decodeIndex(t, dirB).Source; !strings.Contains(src, "golang@db") {
		t.Errorf("索引里的来源应是 DB 描述，实际 %q", src)
	}
}

func TestPruneSemantics(t *testing.T) {
	dir := t.TempDir()
	// 先导三张表（含 ccc_file）
	first := syncCSV + "ccc_file,ccc01,备注,VARCHAR2,10,10,,,Y,0\n"
	if _, err := Import(ImportOptions{CSVData: []byte(first), DataDir: dir, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	if got := decodeIndex(t, dir).Tables; len(got) != 3 {
		t.Fatalf("应有 3 张表，实际 %d", len(got))
	}

	// 不 prune：ccc_file 保留
	res, err := Import(ImportOptions{CSVData: []byte(syncCSV), DataDir: dir, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if res.TablesKept != 1 || res.TablesPruned != 0 {
		t.Errorf("默认应保留 1 张、删除 0 张，实际 保留 %d 删除 %d", res.TablesKept, res.TablesPruned)
	}
	if _, err := os.Stat(filepath.Join(dir, TablesSubDir, "CCC_FILE.json")); err != nil {
		t.Errorf("不 prune 时表文件应还在：%v", err)
	}

	// prune：ccc_file 被删（文件 + 索引条目）
	res, err = Import(ImportOptions{CSVData: []byte(syncCSV), DataDir: dir, Now: fixedNow, Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.TablesPruned != 1 || res.TablesKept != 0 {
		t.Errorf("prune 应删除 1 张，实际 删除 %d 保留 %d", res.TablesPruned, res.TablesKept)
	}
	if _, err := os.Stat(filepath.Join(dir, TablesSubDir, "CCC_FILE.json")); !os.IsNotExist(err) {
		t.Errorf("prune 后表文件应被删除，实际 err=%v", err)
	}
	if _, ok := decodeIndex(t, dir).Tables["CCC_FILE"]; ok {
		t.Error("prune 后索引里不该再有 CCC_FILE")
	}

	// dry-run 不删
	if _, err := Import(ImportOptions{CSVData: []byte(first), DataDir: dir, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	res, err = Import(ImportOptions{CSVData: []byte(syncCSV), DataDir: dir, Now: fixedNow, Prune: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.TablesPruned != 1 {
		t.Errorf("dry-run 报告里应显示将删除 1 张，实际 %d", res.TablesPruned)
	}
	if _, err := os.Stat(filepath.Join(dir, TablesSubDir, "CCC_FILE.json")); err != nil {
		t.Errorf("dry-run 不该真删：%v", err)
	}
}

func TestPruneRejectedWithFilters(t *testing.T) {
	dir := t.TempDir()
	for _, opts := range []ImportOptions{
		{CSVData: []byte(syncCSV), DataDir: dir, Prune: true, OnlyTables: []string{"AAA_FILE"}},
		{CSVData: []byte(syncCSV), DataDir: dir, Prune: true, OnlyNew: true},
	} {
		if _, err := Import(opts); err == nil {
			t.Error("-prune 与 -tables/-only-new 同时给出应报错（否则会删掉范围外的表）")
		}
	}
}

// TestSyncFromFileCLI `schema sync -source file` 全链路（不连库）。
func TestSyncFromFileCLI(t *testing.T) {
	root := t.TempDir()
	csvPath := filepath.Join(root, ".project", "gal_schema.csv") // 与插件落点一致
	writeFileT(t, csvPath, syncCSV)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"sync", "-source", "file", "-root", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("退出码 %d，stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "== schema 同步 ==") || !strings.Contains(out, "来源            gal_schema.csv") {
		t.Errorf("报告缺少同步标题/来源：\n%s", out)
	}
	// 数据目录是默认的 .project/.schema
	dir := filepath.Join(root, DefaultDataDir, DefaultSchemaDir)
	if _, err := os.Stat(filepath.Join(dir, IndexFileName)); err != nil {
		t.Fatalf("应写出索引：%v", err)
	}
	if n := len(decodeIndex(t, dir).Tables); n != 2 {
		t.Errorf("应有 2 张表，实际 %d", n)
	}

	// -tables：只更新指定表（其余保留）
	var out2, err2 bytes.Buffer
	writeFileT(t, csvPath, syncCSV+"ccc_file,ccc01,备注,VARCHAR2,10,10,,,Y,0\n")
	if code := run([]string{"sync", "-source", "file", "-root", root}, &out2, &err2); code != 0 {
		t.Fatalf("第二次退出码 %d：%s", code, err2.String())
	}
	var out3, err3 bytes.Buffer
	if code := run([]string{"sync", "-source", "file", "-root", root, "-tables", "ccc_file"}, &out3, &err3); code != 0 {
		t.Fatalf("按表同步退出码 %d：%s", code, err3.String())
	}
	if _, err := os.Stat(filepath.Join(dir, TablesSubDir, "AAA_FILE.json")); err != nil {
		t.Errorf("AAA_FILE 不在 -tables 里，应保留：%v", err)
	}
}

// TestSyncRejectsMissingSource sync 缺少 -source=file 的文件时要明确报错。
func TestSyncRejectsMissingSource(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"sync", "-source", "file", "-file", filepath.Join(root, "nope.csv"), "-root", root}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("缺文件应退出码 1，实际 %d", code)
	}
	if !strings.Contains(stderr.String(), "nope.csv") {
		t.Errorf("错误信息应带路径，实际：%s", stderr.String())
	}
}

// TestSyncGolangRequiresUser 直连模式没有 -user/-owner 时不能瞎猜（直接报错）。
func TestSyncGolangRequiresUser(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"sync", "-source", "golang", "-root", root}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("缺 -user 应退出码 2（用法错误），实际 %d（stderr=%s）", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "-user") {
		t.Errorf("错误信息应提到 -user：%s", stderr.String())
	}
}
