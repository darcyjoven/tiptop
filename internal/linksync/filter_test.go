package linksync

// P1-3：局部同步的 SQL 条件下推（只在默认 SQL 上用）。

import (
	"strings"
	"testing"
)

func TestFilterClause(t *testing.T) {
	cases := []struct {
		name    string
		modules []string
		specs   []string
		want    string
	}{
		{"空条件", nil, nil, ""},
		{"只有模组", []string{"lib", "aap"}, nil,
			" AND (UPPER(gal02) IN ('LIB','AAP'))"},
		{"只有文件", nil, []string{"aap/4gl/saapt310.4gl"},
			" AND ((UPPER(gal02),LOWER(gal03)) IN (('AAP','saapt310')))"},
		{"模组与文件并集", []string{"axc"}, []string{"aap/4gl/saapt310.4gl"},
			" AND (UPPER(gal02) IN ('AXC') OR (UPPER(gal02),LOWER(gal03)) IN (('AAP','saapt310')))"},
		{"裸文件名（无模组）", nil, []string{"saapt310.4gl"},
			" AND ((UPPER(gal02),LOWER(gal03)) IN (('','saapt310')))"},
		{"空串忽略", []string{"", "  "}, []string{""}, ""},
		{"单引号转义", []string{"a'b"}, nil,
			" AND (UPPER(gal02) IN ('A''B'))"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FilterClause(tc.modules, tc.specs); got != tc.want {
				t.Errorf("FilterClause = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// TestFilterClauseOnlyOnDefaultQuery 下推只在默认 SQL 上生效：
// 自定义 SQL 原样使用（不知道用户的列名，硬拼条件会拼坏）。
func TestFilterClauseOnlyOnDefaultQuery(t *testing.T) {
	custom := "SELECT gal01, gal02, gal03, gal04 FROM MY_VIEW WHERE 1=1"
	src, err := Config{
		Source:      "golang",
		OnlyModules: []string{"lib"},
		DB:          DBConfig{Host: "db.example.com", Service: "PROD", User: "u", Query: custom},
	}.NewSource(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, ok := src.(*dbSource)
	if !ok {
		t.Fatalf("期望 dbSource，实际 %T", src)
	}
	if db.cfg.Query != custom {
		t.Errorf("自定义 SQL 不应被改写，实际 %q", db.cfg.Query)
	}

	src2, err := Config{
		Source:      "golang",
		OnlyModules: []string{"lib"},
		DB:          DBConfig{Host: "db.example.com", Service: "PROD", User: "u"},
	}.NewSource(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db2 := src2.(*dbSource)
	if !strings.Contains(db2.cfg.Query, "UPPER(gal02) IN ('LIB')") {
		t.Errorf("默认 SQL 应带上模组条件，实际 %q", db2.cfg.Query)
	}
}

// TestFilterClauseNilSpecsSafety 只给模组时不产生空的 OR 子句。
func TestFilterClauseNilSpecsSafety(t *testing.T) {
	got := FilterClause([]string{"lib"}, []string{"", "  "})
	if strings.Contains(got, "gal03") {
		t.Errorf("空文件条件不应产生 gal03 子句：%q", got)
	}
	if !strings.Contains(got, "gal02") {
		t.Errorf("应保留模组条件：%q", got)
	}
}
