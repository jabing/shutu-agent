package spill

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// memProvider is the default in-memory spill backend (ADR 决策 M6c). Every memo
// lives in memory only — nothing is persisted and no files are touched — so a
// process restart clears the memory by construction (persisting to the store
// layer is deferred; see the package doc). It is safe for concurrent use and
// performs no validation: the Engine is the validation boundary (Add/Get/
// Delete/Search are called through by the Engine).
type memProvider struct {
	mu     sync.Mutex
	memos  map[string]Memo
	closed bool
}

// NewMemProvider returns a fresh in-memory Provider — the default backend for
// NewEngine. It is exported so wiring and tests can inject it explicitly.
func NewMemProvider() Provider {
	return newMemProvider()
}

func newMemProvider() *memProvider {
	return &memProvider{memos: map[string]Memo{}}
}

// Name identifies the provider in the registry ("memory").
func (m *memProvider) Name() string { return "memory" }

// List returns every current memo, sorted by id.
func (m *memProvider) List(ctx context.Context) ([]Memo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrProviderClosed
	}
	out := make([]Memo, 0, len(m.memos))
	for _, memo := range m.memos {
		out = append(out, memo)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Add is an idempotent upsert by ID (an empty id is rejected). A memo re-added
// under the same id keeps its first-seen CreatedAt and overwrites Content and
// Source.
func (m *memProvider) Add(ctx context.Context, memo Memo) (Memo, error) {
	if err := ctx.Err(); err != nil {
		return Memo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Memo{}, ErrProviderClosed
	}
	if memo.ID == "" {
		return Memo{}, fmt.Errorf("spill: memo id required")
	}
	if old, ok := m.memos[memo.ID]; ok {
		old.Content = memo.Content
		old.Source = memo.Source
		m.memos[memo.ID] = old
		return old, nil
	}
	m.memos[memo.ID] = memo
	return memo, nil
}

// Get returns the memo with id; an unknown id is rejected.
func (m *memProvider) Get(ctx context.Context, id string) (Memo, error) {
	if err := ctx.Err(); err != nil {
		return Memo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Memo{}, ErrProviderClosed
	}
	memo, ok := m.memos[id]
	if !ok {
		return Memo{}, fmt.Errorf("%w: %s", ErrUnknownMemo, id)
	}
	return memo, nil
}

// Delete removes the memo with id; an unknown id is rejected.
func (m *memProvider) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrProviderClosed
	}
	if _, ok := m.memos[id]; !ok {
		return fmt.Errorf("%w: %s", ErrUnknownMemo, id)
	}
	delete(m.memos, id)
	return nil
}

// Search returns up to limit memos whose content matches query. v1 uses
// case-insensitive substring matching with zero dependencies. An empty query
// matches every memo. limit <= 0 means no truncation. Results are sorted by id.
func (m *memProvider) Search(ctx context.Context, query string, limit int) ([]Memo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrProviderClosed
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var out []Memo
	for _, memo := range m.memos {
		if q != "" && !strings.Contains(strings.ToLower(memo.Content), q) {
			continue
		}
		out = append(out, memo)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Close marks the provider closed so no further operations are accepted. It is
// idempotent and releases nothing else (no goroutines live here).
func (m *memProvider) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}
