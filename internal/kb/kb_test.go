// Black-box tests for the kb seam: the same consumer battery runs unchanged
// against both the SQLite and the in-memory provider, proving the interface
// boundary (design.md D2/D9: 换 Provider 消费方代码不变).
package kb_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"personal-agent/internal/config"
	"personal-agent/internal/kb"
)

// sample entries (E1..E6) used by the consumer battery. Sources are distinct
// except where a test deliberately reuses one for a same-source update.
func seed(t *testing.T, k kb.KB) {
	t.Helper()
	entries := []kb.Entry{
		{Title: "架构决策记录", Body: "我们决定采用 SQLite FTS5 作为知识库检索方案", Type: kb.TypeDecision, Tags: []string{"架构", "决策"}, Source: "session:s1:turn:1", Confidence: 0.9},
		{Title: "中文分词测试", Body: "连续中文默认被 FTS5 当成一个 token，需要二元组兜底", Type: kb.TypeLesson, Tags: []string{"中文", "检索"}, Source: "session:s1:turn:2", Confidence: 0.8},
		{Title: "SQLite full text search", Body: "FTS5 provides BM25 ranking for English text", Type: kb.TypeFact, Tags: []string{"sqlite", "fts5"}, Source: "session:s1:turn:3", Confidence: 1.0},
		{Title: "混合检索验证", Body: "FTS5 配合中文二元组 LIKE 兜底", Type: kb.TypeProcedure, Tags: []string{"混合"}, Source: "session:s1:turn:4", Confidence: 0.7},
		{Title: "提取回写机制", Body: "知识来源通过回答后模型提取回写写入条目", Type: kb.TypeProcedure, Tags: []string{"提取", "回写"}, Source: "session:s1:turn:5", Confidence: 0.75},
		{Title: "项目内知识", Body: "scoped entry body 项目内部", Type: kb.TypeFact, Tags: []string{"scope"}, Scope: "project-x", Source: "manual:6", Confidence: 0.5},
	}
	for _, e := range entries {
		if err := k.Add(context.Background(), e); err != nil {
			t.Fatalf("Add %q: %v", e.Title, err)
		}
	}
}

// searchTitles runs Search and returns the sorted hit titles.
func searchTitles(t *testing.T, k kb.KB, query string, opts kb.SearchOpts) []string {
	t.Helper()
	hits, err := k.Search(context.Background(), query, opts)
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	titles := make([]string, 0, len(hits))
	for _, h := range hits {
		titles = append(titles, h.Entry.Title)
	}
	sort.Strings(titles)
	return titles
}

func assertTitles(t *testing.T, k kb.KB, query string, opts kb.SearchOpts, want []string) {
	t.Helper()
	got := searchTitles(t, k, query, opts)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("Search(%q) titles = %v, want %v", query, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("Search(%q) titles = %v, want %v", query, got, want)
		}
	}
}

// exerciseConsumer is the same consumer code that must run unchanged against
// every provider (design.md D2/D9). The acceptance criterion "换 Provider 消费
// 方代码不变" is verified by running it against both the SQLite and the
// in-memory provider.
func exerciseConsumer(t *testing.T, k kb.KB) {
	seed(t, k)

	// 中文检索（走二元组 LIKE 兜底）：单字 / 词 / 短语。
	assertTitles(t, k, "架构", kb.SearchOpts{TopK: 5}, []string{"架构决策记录"})
	assertTitles(t, k, "架", kb.SearchOpts{TopK: 5}, []string{"架构决策记录"})
	assertTitles(t, k, "决策", kb.SearchOpts{TopK: 5}, []string{"架构决策记录"})
	assertTitles(t, k, "中文", kb.SearchOpts{TopK: 5}, []string{"中文分词测试", "混合检索验证"})
	assertTitles(t, k, "分词", kb.SearchOpts{TopK: 5}, []string{"中文分词测试"})
	// 短语：二元组 LIKE 兜底同样覆盖多字短语。
	assertTitles(t, k, "中文分词测试", kb.SearchOpts{TopK: 5}, []string{"中文分词测试", "混合检索验证"})

	// 英文检索（走 FTS5 BM25）。
	assertTitles(t, k, "sqlite", kb.SearchOpts{TopK: 5}, []string{"架构决策记录", "SQLite full text search"})
	assertTitles(t, k, "full text", kb.SearchOpts{TopK: 5}, []string{"SQLite full text search"})

	// 混合检索：FTS5 覆盖英文，二元组 LIKE 补充中文。
	assertTitles(t, k, "BM25 提取", kb.SearchOpts{TopK: 5}, []string{"SQLite full text search", "提取回写机制"})
	assertTitles(t, k, "提取", kb.SearchOpts{TopK: 5}, []string{"提取回写机制"})

	// Add 后能检索到（seed 已隐式覆盖）；再验证一次显式 Add + 检索。查询词用
	// 全局唯一的串，避免与其他条目的二元组撞车。
	if err := k.Add(context.Background(), kb.Entry{
		Title: "新显式写入", Body: "专属标记词 xyz，验证显式写入可检索", Type: kb.TypeFact,
		Tags: []string{"显式"}, Source: "manual:new", Confidence: 0.6,
	}); err != nil {
		t.Fatalf("Add explicit: %v", err)
	}
	assertTitles(t, k, "专属标记词", kb.SearchOpts{TopK: 5}, []string{"新显式写入"})

	// 同 source 更新版本递增：两次 Add 同一 Source ⇒ 同 ID、版本 +1、旧内容不可再检索。
	const src = "session:s1:turn:9"
	first := kb.Entry{Title: "第一版方案", Body: "第一版内容", Type: kb.TypeDecision, Source: src, Confidence: 0.6}
	if err := k.Add(context.Background(), first); err != nil {
		t.Fatalf("Add v1: %v", err)
	}
	hits, err := k.Search(context.Background(), "第一版", kb.SearchOpts{TopK: 5})
	if err != nil {
		t.Fatalf("Search v1: %v", err)
	}
	if len(hits) != 1 || hits[0].Entry.Version != 1 {
		t.Fatalf("after v1 add: hits=%+v, want 1 hit at version 1", hits)
	}
	firstID := hits[0].Entry.ID
	if firstID == "" {
		t.Fatal("provider must assign a non-empty entry id")
	}
	second := kb.Entry{Title: "第二版方案", Body: "第二版内容", Type: kb.TypeDecision, Source: src, Confidence: 0.6}
	if err := k.Add(context.Background(), second); err != nil {
		t.Fatalf("Add v2: %v", err)
	}
	hits, err = k.Search(context.Background(), "第二版", kb.SearchOpts{TopK: 5})
	if err != nil {
		t.Fatalf("Search v2: %v", err)
	}
	if len(hits) != 1 || hits[0].Entry.Version != 2 {
		t.Fatalf("after v2 add: hits=%+v, want 1 hit at version 2", hits)
	}
	if hits[0].Entry.ID != firstID {
		t.Errorf("same-source update changed id: got %q, want %q", hits[0].Entry.ID, firstID)
	}
	if got := searchTitles(t, k, "第一版", kb.SearchOpts{TopK: 5}); len(got) != 0 {
		t.Errorf("old content still retrievable after same-source update: %v", got)
	}

	// 不同 source ⇒ 独立新条目（version 1，不同 ID）。检索词用全局唯一串。
	other := kb.Entry{Title: "另一来源方案", Body: "anode 特殊令牌 99 独立条目内容", Type: kb.TypeDecision, Source: "session:s9:turn:9", Confidence: 0.6}
	if err := k.Add(context.Background(), other); err != nil {
		t.Fatalf("Add other source: %v", err)
	}
	hits, err = k.Search(context.Background(), "特殊令牌", kb.SearchOpts{TopK: 5})
	if err != nil {
		t.Fatalf("Search other: %v", err)
	}
	if len(hits) != 1 || hits[0].Entry.Version != 1 || hits[0].Entry.ID == firstID {
		t.Fatalf("different source should be a fresh entry: hits=%+v", hits)
	}

	// Recall 是有界检索：limit=1 只回 1 条。
	recalls, err := k.Recall(context.Background(), "架构", 1)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(recalls) != 1 || recalls[0].Entry.Title != "架构决策记录" {
		t.Fatalf("Recall(架构, 1) = %+v, want exactly 架构决策记录", recalls)
	}

	// Scope 过滤：同词在不同 scope 下返回不同集合。
	assertTitles(t, k, "知识", kb.SearchOpts{TopK: 5}, []string{"架构决策记录", "提取回写机制", "项目内知识"})
	assertTitles(t, k, "知识", kb.SearchOpts{TopK: 5, Scope: "project-x"}, []string{"项目内知识"})
	assertTitles(t, k, "知识", kb.SearchOpts{TopK: 5, Scope: "global"}, []string{"架构决策记录", "提取回写机制"})

	// TopK 上限：请求 1 条只回 1 条。
	if got := searchTitles(t, k, "架构", kb.SearchOpts{TopK: 1}); len(got) != 1 {
		t.Errorf("TopK=1 returned %d hits: %v", len(got), got)
	}
}

func openSQLite(t *testing.T) kb.KB {
	t.Helper()
	k, err := kb.OpenSQLite(filepath.Join(t.TempDir(), "kb.sqlite"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

// TestProviderSwapConsumerUnchanged runs the exact same consumer code against
// the SQLite and the in-memory provider — the interface-boundary acceptance
// ("同一服务代码对两个 Provider 跑通，换 Provider 消费方代码不变").
func TestProviderSwapConsumerUnchanged(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { exerciseConsumer(t, openSQLite(t)) })
	t.Run("in-memory", func(t *testing.T) { exerciseConsumer(t, kb.NewMemProvider()) })
}

// TestSQLiteDatabaseCreated verifies the provider materializes a real database
// file at the requested path (data/kb, gitignored).
func TestSQLiteDatabaseCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kb", "knowledge.sqlite")
	k, err := kb.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer k.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file not created at %s: %v", path, err)
	}
}

// TestNewFromConfigDisabled proves "kb 默认关闭，不初始化": when enabled is
// false no provider is constructed and no database file is opened.
func TestNewFromConfigDisabled(t *testing.T) {
	k, err := kb.NewFromConfig(config.KBConfig{Enabled: false})
	if err != nil {
		t.Fatalf("NewFromConfig(disabled) = %v, want nil", err)
	}
	if k != nil {
		t.Errorf("NewFromConfig(disabled) returned a provider %v, want nil (不初始化)", k)
	}
}

// TestNewFromConfigEnabled opens the provider at the configured path when
// enabled.
func TestNewFromConfigEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kb.sqlite")
	k, err := kb.NewFromConfig(config.KBConfig{Enabled: true, DBPath: path})
	if err != nil {
		t.Fatalf("NewFromConfig(enabled): %v", err)
	}
	if k == nil {
		t.Fatal("NewFromConfig(enabled) returned nil provider")
	}
	defer k.Close()
	if err := k.Add(context.Background(), kb.Entry{Title: "t", Body: "b", Type: kb.TypeFact, Confidence: 0.5}); err != nil {
		t.Fatalf("Add after NewFromConfig: %v", err)
	}
}
