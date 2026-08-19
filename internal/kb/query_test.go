package kb

import (
	"sort"
	"testing"
)

func TestToFtsQuery(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"sqlite", `"sqlite"`},
		{"full text", `"full" OR "text"`},
		{`say "hi" there`, `"say" OR """hi""" OR "there"`},
		{"  spaced   out  ", `"spaced" OR "out"`},
		{"", ""},
		{"FTS5 提取", `"FTS5" OR "提取"`},
	}
	for _, c := range cases {
		if got := toFtsQuery(c.in); got != c.want {
			t.Errorf("toFtsQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFallbackTerms(t *testing.T) {
	// English words (lowercased) + Chinese adjacent bigrams (single Han char
	// run kept whole). The result is a deterministic sorted set; compare as a
	// set since order carries no semantics (the LIKE clause ORs the terms).
	cases := []struct {
		in   string
		want []string
	}{
		{"架构", []string{"架构"}},
		{"架构决策", []string{"架构", "构决", "决策"}},
		{"架", []string{"架"}},
		{"SQLite", []string{"sqlite"}},
		{"sqlite.db", []string{"sqlite.db"}},
		{"FTS5 提取", []string{"fts5", "提取"}},
		// mixed: English words + Han bigrams coexist
		{"SQLite 全文检索", []string{"sqlite", "检索", "全文", "文检"}},
	}
	for _, c := range cases {
		got := fallbackTerms(c.in)
		if !sameTerms(got, c.want) {
			t.Errorf("fallbackTerms(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// sameTerms compares two term slices as sets (duplicates ignored, order free).
func sameTerms(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	gs, ws := append([]string(nil), got...), append([]string(nil), want...)
	sort.Strings(gs)
	sort.Strings(ws)
	for i := range gs {
		if gs[i] != ws[i] {
			return false
		}
	}
	return true
}

func TestFallbackTermsCapsAtTwenty(t *testing.T) {
	// More than 20 distinct terms → capped (deterministic subset).
	long := "架构 决策 中文 分词 测试 检索 知识 库 方案 语义 混合 兜底 全文 索引 权重 排序 命中 版本 更新 写入 额外"
	got := fallbackTerms(long)
	if len(got) != 20 {
		t.Fatalf("fallbackTerms capped length = %d, want 20", len(got))
	}
}

func TestRankToScore(t *testing.T) {
	cases := []struct {
		rank float64
		want float64
	}{
		{0, 1},
		{1, 0.5},
		{3, 0.25},
		{-1, 1}, // negative ranks are clamped to 0
	}
	for _, c := range cases {
		if got := rankToScore(c.rank); got != c.want {
			t.Errorf("rankToScore(%v) = %v, want %v", c.rank, got, c.want)
		}
	}
}

func TestEscapeLike(t *testing.T) {
	if got := escapeLike(`50%_off\`); got != `50\%\_off\\` {
		t.Errorf("escapeLike = %q", got)
	}
	if got := escapeLike("架构"); got != "架构" {
		t.Errorf("escapeLike chinese = %q", got)
	}
}
