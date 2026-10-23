package schemacli

import (
	"reflect"
	"testing"
)

// demoRows 是一段接近实测导出的小数据：含 CRLF、引号包裹的逗号、
// data_length 为空、nullable 的空值，以及同表内重复列名。
const demoRows = "ima_file,ima01,\"物料,编号\",CHAR,0,10,0,0,Y,0\r\n" +
	"ima_file,ima02,数量,NUMBER,22,0,5,2,NO,4\r\n" +
	"ima_file,ima03,,VARCHAR2,,,0,0,,0\r\n" +
	"ima_file,ima01,重复列,CHAR,1,1,0,0,N,0\r\n"

// 实测表头：data_precicison / default_legth 两个拼写错误。
const headerMisspelled = "table_name,column_name,column_desc,data_type,data_length," +
	"char_length,data_precicison,data_scale,nullable,default_legth\r\n"

// 正确拼写的表头。
const headerCorrect = "table_name,column_name,column_desc,data_type,data_length," +
	"char_length,data_precision,data_scale,nullable,default_length\r\n"

func mustParse(t *testing.T, s string) *parseResult {
	t.Helper()
	got, err := parseCSV([]byte(s))
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}
	return got
}

// TestParseHeaderTolerance 覆盖要求 1：带拼写错误的表头与正确拼写、
// 以及完全认不出的表头（按位置回退）必须解析出同样的结果。
func TestParseHeaderTolerance(t *testing.T) {
	// 带 BOM + 拼写错误的表头。
	gotA := mustParse(t, "\ufeff"+headerMisspelled+demoRows)
	// 正确拼写的表头。
	gotB := mustParse(t, headerCorrect+demoRows)
	// 一个列名都认不出：只能按位置匹配。
	gotC := mustParse(t, "c1,c2,c3,c4,c5,c6,c7,c8,c9,c10\r\n"+demoRows)

	for name, got := range map[string]*parseResult{
		"拼写错误表头": gotA, "正确表头": gotB, "按位置回退": gotC,
	} {
		if len(got.Tables) != 1 {
			t.Fatalf("%s: 表数 = %d，期望 1", name, len(got.Tables))
		}
		td := got.Tables[0]
		if td.Key != "IMA_FILE" || td.Name != "ima_file" {
			t.Errorf("%s: 表键/名 = %q/%q，期望 IMA_FILE/ima_file", name, td.Key, td.Name)
		}
		if got.RowsTotal != 4 || got.RowsSkipped != 0 {
			t.Errorf("%s: 行数 = %d / 跳过 %d，期望 4/0", name, got.RowsTotal, got.RowsSkipped)
		}
		// 同表内重复列名（ima01）只保留第一条。
		if len(td.Columns) != 3 {
			t.Fatalf("%s: 列数 = %d，期望 3（重复列已去重）", name, len(td.Columns))
		}
	}

	want := []Column{
		// data_length=0 → 回退 char_length=10；desc 含逗号；Y → true。
		{Name: "ima01", Desc: "物料,编号", Type: "CHAR", Length: 10, CharLength: 10, Nullable: true, DefaultLength: "0"},
		// NUMBER 的 data_length=22、char_length=0；NO → false。
		{Name: "ima02", Desc: "数量", Type: "NUMBER", Length: 22, CharLength: 0, Precision: 5, Scale: 2, Nullable: false, DefaultLength: "4"},
		// 数字列全空 → 0；nullable 空 → false；desc 空。
		{Name: "ima03", Desc: "", Type: "VARCHAR2", Length: 0, CharLength: 0, Nullable: false, DefaultLength: "0"},
	}
	if !reflect.DeepEqual(gotA.Tables[0].Columns, want) {
		t.Errorf("拼写错误表头解析结果不符：\n得到 %+v\n期望 %+v", gotA.Tables[0].Columns, want)
	}
	if !reflect.DeepEqual(gotA.Tables[0].Columns, gotB.Tables[0].Columns) {
		t.Errorf("拼写错误表头与正确表头结果不一致：\n%+v\n%+v",
			gotA.Tables[0].Columns, gotB.Tables[0].Columns)
	}
	if !reflect.DeepEqual(gotA.Tables[0].Columns, gotC.Tables[0].Columns) {
		t.Errorf("按位置回退与按别名匹配结果不一致：\n%+v\n%+v",
			gotA.Tables[0].Columns, gotC.Tables[0].Columns)
	}
}

// TestParseNullableAndBadRows 覆盖要求里的容错：YES/NO 大小写、非数字长度、
// 列数不足/空表名的行跳过并计数（不中断），空行由 encoding/csv 直接丢弃。
func TestParseNullableAndBadRows(t *testing.T) {
	csv := headerMisspelled +
		"ima_file,ima01,说明,CHAR,5,5,0,0,YES,0\r\n" + // YES → true
		"ima_file,ima02,说明2,CHAR,5,5,0,0,no,0\r\n" + // no → false（大小写不敏感）
		"ima_file,ima03\r\n" + // 列数不足（2 < 10）
		"ima_file\r\n" + // 只有 1 列
		",ima04,说明4\r\n" + // 表名为空
		"\r\n" + // 空行：csv.Reader 不返回记录
		"ima_file,ima05,说明5,CHAR,abc,9,0,0,N,0\r\n" + // data_length 非数字 → 回退 char_length
		"ima_file,ima06,说明6,CHAR,8,8,0,0,Y,0\r\n"

	got := mustParse(t, csv)
	if got.RowsTotal != 7 {
		t.Errorf("数据行数 = %d，期望 7（空行不算数据行）", got.RowsTotal)
	}
	if got.RowsSkipped != 3 {
		t.Errorf("跳过行数 = %d，期望 3", got.RowsSkipped)
	}
	if len(got.Problems) != 3 {
		t.Errorf("问题条数 = %d，期望 3", len(got.Problems))
	}
	if len(got.Tables[0].Columns) != 4 {
		t.Fatalf("列数 = %d，期望 4", len(got.Tables[0].Columns))
	}
	cols := got.Tables[0].Columns
	if !cols[0].Nullable {
		t.Errorf("YES 应解析为 true：%+v", cols[0])
	}
	if cols[1].Nullable {
		t.Errorf("no 应解析为 false：%+v", cols[1])
	}
	if cols[2].Length != 9 || cols[2].CharLength != 9 {
		t.Errorf("非数字 data_length 应回退 char_length：%+v", cols[2])
	}
}

// TestParseEmptyCSV：没有表头要明确报错，而不是产出一份空资料。
func TestParseEmptyCSV(t *testing.T) {
	if _, err := parseCSV(nil); err == nil {
		t.Error("空 CSV 应报错")
	}
}
