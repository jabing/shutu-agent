package spill

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func bg() context.Context { return context.Background() }

func TestSpillAndList(t *testing.T) {
	eng := NewEngine(nil)
	defer eng.Close()

	m, err := eng.Spill(bg(), "The user prefers Go for new projects", "session:1")
	if err != nil {
		t.Fatalf("Spill: %v", err)
	}
	if !strings.HasPrefix(m.ID, "memo-") {
		t.Fatalf("memo id = %q, want a memo- prefix", m.ID)
	}
	if m.Content != "The user prefers Go for new projects" {
		t.Fatalf("content = %q", m.Content)
	}
	if m.Source != "session:1" {
		t.Fatalf("source = %q, want session:1", m.Source)
	}
	if m.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not set")
	}

	all, err := eng.List(bg())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].ID != m.ID {
		t.Fatalf("List = %+v, want one memo with id %s", all, m.ID)
	}
}

func TestSpillDedupIdempotent(t *testing.T) {
	eng := NewEngine(nil)
	defer eng.Close()

	first, err := eng.Spill(bg(), "Dedup this content", "session:1")
	if err != nil {
		t.Fatalf("first Spill: %v", err)
	}
	second, err := eng.Spill(bg(), "Dedup this content", "session:2")
	if err != nil {
		t.Fatalf("second Spill: %v", err)
	}
	// Same content → same id, and the existing memo is returned unchanged
	// (first-seen source and CreatedAt are preserved).
	if first.ID != second.ID {
		t.Fatalf("ids differ: %q vs %q, want identical (content hash)", first.ID, second.ID)
	}
	if second.Source != "session:1" {
		t.Fatalf("re-spill source = %q, want the original session:1", second.Source)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("re-spill changed CreatedAt: %v vs %v", first.CreatedAt, second.CreatedAt)
	}
	all, err := eng.List(bg())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("dedup failed: %d memos stored, want 1", len(all))
	}
}

func TestSpillTrimsAndRejectsBlank(t *testing.T) {
	eng := NewEngine(nil)
	defer eng.Close()

	if _, err := eng.Spill(bg(), "   ", "session:1"); err == nil {
		t.Fatal("expected an error for blank content")
	}
	// Whitespace-only differences collapse to the same memo.
	a, err := eng.Spill(bg(), "same content", "session:1")
	if err != nil {
		t.Fatalf("Spill: %v", err)
	}
	b, err := eng.Spill(bg(), "  same content  ", "session:1")
	if err != nil {
		t.Fatalf("Spill trimmed: %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("trimmed ids differ: %q vs %q", a.ID, b.ID)
	}
	all, _ := eng.List(bg())
	if len(all) != 1 {
		t.Fatalf("stored %d memos, want 1", len(all))
	}
}

func TestRecallSearchAndLimit(t *testing.T) {
	eng := NewEngine(nil)
	defer eng.Close()

	for i, c := range []string{
		"alpha fact about cats",
		"beta fact about dogs",
		"gamma fact about cats and dogs",
		"delta unrelated",
	} {
		if _, err := eng.Spill(bg(), c, "session:1"); err != nil {
			t.Fatalf("spill %d: %v", i, err)
		}
	}

	// Case-insensitive substring recall.
	got, err := eng.Recall(bg(), "CATS", 0)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Recall(cats) = %d hits, want 2: %+v", len(got), got)
	}

	// More than the default limit: recall is truncated to defaultRecallLimit.
	for i := 0; i < 6; i++ {
		if _, err := eng.Spill(bg(), fmt.Sprintf("common topic item %d", i), "session:2"); err != nil {
			t.Fatalf("spill common %d: %v", i, err)
		}
	}
	got, err = eng.Recall(bg(), "common", 0)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != defaultRecallLimit {
		t.Fatalf("default-limit Recall = %d, want %d", len(got), defaultRecallLimit)
	}

	// Explicit positive limit.
	got, err = eng.Recall(bg(), "common", 3)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("limited Recall = %d, want 3", len(got))
	}

	// Empty query matches everything (bounded by the default limit).
	got, err = eng.Recall(bg(), "", 0)
	if err != nil {
		t.Fatalf("Recall empty: %v", err)
	}
	if len(got) != defaultRecallLimit {
		t.Fatalf("empty-query Recall = %d, want %d", len(got), defaultRecallLimit)
	}
}

func TestProviderSearchContains(t *testing.T) {
	p := newMemProvider()
	defer p.Close()
	ctx := bg()

	now := time.Now().UTC()
	for _, m := range []Memo{
		{ID: "memo-a", Content: "The quick brown fox", Source: "s", CreatedAt: now},
		{ID: "memo-b", Content: "the QUICK red fox", Source: "s", CreatedAt: now},
		{ID: "memo-c", Content: "lazy dog sleeps", Source: "s", CreatedAt: now},
	} {
		if _, err := p.Add(ctx, m); err != nil {
			t.Fatalf("Add %s: %v", m.ID, err)
		}
	}

	// Case-insensitive substring matching.
	for _, q := range []string{"quick", "QUICK", "Quick"} {
		got, err := p.Search(ctx, q, 10)
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		if len(got) != 2 {
			t.Fatalf("Search(%q) = %d hits, want 2", q, len(got))
		}
	}

	// limit truncation.
	got, err := p.Search(ctx, "fox", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search(fox, limit=1) = %d hits, want 1", len(got))
	}

	// No match.
	got, err = p.Search(ctx, "zebra", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Search(zebra) = %d hits, want 0", len(got))
	}

	// Empty query matches everything.
	got, err = p.Search(ctx, "", 10)
	if err != nil {
		t.Fatalf("Search empty: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Search(empty) = %d hits, want 3", len(got))
	}
}

func TestRemove(t *testing.T) {
	eng := NewEngine(nil)
	defer eng.Close()

	m, err := eng.Spill(bg(), "remove me", "session:1")
	if err != nil {
		t.Fatalf("Spill: %v", err)
	}
	if err := eng.Remove(bg(), m.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	all, _ := eng.List(bg())
	if len(all) != 0 {
		t.Fatalf("after Remove = %d memos, want 0", len(all))
	}
	if err := eng.Remove(bg(), "memo-nope"); !errors.Is(err, ErrUnknownMemo) {
		t.Fatalf("Remove unknown err = %v, want ErrUnknownMemo", err)
	}
}

func TestProviderGetUnknown(t *testing.T) {
	p := newMemProvider()
	defer p.Close()
	if _, err := p.Get(bg(), "memo-nope"); !errors.Is(err, ErrUnknownMemo) {
		t.Fatalf("Get unknown err = %v, want ErrUnknownMemo", err)
	}
	if err := p.Delete(bg(), "memo-nope"); !errors.Is(err, ErrUnknownMemo) {
		t.Fatalf("Delete unknown err = %v, want ErrUnknownMemo", err)
	}
	if _, err := p.Add(bg(), Memo{}); err == nil {
		t.Fatal("Add with empty id should be rejected")
	}
}

func TestCloseIdempotent(t *testing.T) {
	eng := NewEngine(nil)
	if err := eng.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if _, err := eng.Spill(bg(), "x", "s"); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("Spill after Close err = %v, want ErrEngineClosed", err)
	}
	if _, err := eng.List(bg()); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("List after Close err = %v, want ErrEngineClosed", err)
	}
	if _, err := eng.Recall(bg(), "x", 5); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("Recall after Close err = %v, want ErrEngineClosed", err)
	}
	if _, err := eng.AutoSpill(bg(), nil); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("AutoSpill after Close err = %v, want ErrEngineClosed", err)
	}
	if err := eng.Remove(bg(), "memo-x"); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("Remove after Close err = %v, want ErrEngineClosed", err)
	}
}

func TestProviderClosed(t *testing.T) {
	p := newMemProvider()
	p.Close()
	if _, err := p.List(bg()); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("List after Close err = %v, want ErrProviderClosed", err)
	}
	if _, err := p.Add(bg(), Memo{ID: "memo-x", Content: "x"}); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("Add after Close err = %v, want ErrProviderClosed", err)
	}
	if _, err := p.Get(bg(), "memo-x"); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("Get after Close err = %v, want ErrProviderClosed", err)
	}
	if _, err := p.Search(bg(), "x", 5); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("Search after Close err = %v, want ErrProviderClosed", err)
	}
	if err := p.Delete(bg(), "memo-x"); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("Delete after Close err = %v, want ErrProviderClosed", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("provider Close is idempotent, got: %v", err)
	}
}

func TestNewEngineUsesDefaultProvider(t *testing.T) {
	eng := NewEngine(nil)
	defer eng.Close()
	if got := eng.prov.Name(); got != "memory" {
		t.Fatalf("default provider name = %q, want memory", got)
	}
}
