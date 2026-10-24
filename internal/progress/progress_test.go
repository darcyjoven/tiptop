package progress

import (
	"bytes"
	"testing"
)

func TestNoteAndStep(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf)
	r.Note("采集", "连接 db:1521/PROD…")
	r.Step("解析", "ima_file", 12, 100)
	got := buf.String()
	want := "进度\t采集\t连接 db:1521/PROD…\n进度\t解析\tima_file\t12\t100\n"
	if got != want {
		t.Errorf("进度行格式不对：\n got %q\nwant %q", got, want)
	}
}

func TestStepDegradesWithoutCounts(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf)
	r.Step("写入", "开始", 0, 0)
	if got := buf.String(); got != "进度\t写入\t开始\n" {
		t.Errorf("没有计数时应退化成 Note：%q", got)
	}
}

func TestNewNilAndNilReporter(t *testing.T) {
	r := New(nil)
	r.Note("采集", "x") // 不能 panic
	r.Step("解析", "y", 1, 2)
	var nilR *Reporter
	nilR.Note("采集", "x")
	nilR.Step("解析", "y", 1, 2)
}

func TestEvery(t *testing.T) {
	cases := []struct {
		i, n, max int
		want      bool
	}{
		{1, 10, 100, true}, // 总数小：全报
		{5, 10, 100, true},
		{1, 2648, 100, false}, // 节流：每 26 条一次
		{26, 2648, 100, true},
		{2648, 2648, 100, true}, // 最后一条必报
		{0, 10, 100, false},
		{1, 0, 100, false},
	}
	for _, c := range cases {
		if got := Every(c.i, c.n, c.max); got != c.want {
			t.Errorf("Every(%d,%d,%d) = %v，期望 %v", c.i, c.n, c.max, got, c.want)
		}
	}
}
