// MemProvider is a tiny in-memory KB backend. It exists to prove the seam
// boundary — the same consumer code runs unchanged against SQLite and memory
// (design.md D2/D9) — and to double as a lightweight reference implementation.
// It is not a production backend: search is a simplified term-containment
// match over the same fallbackTerms vocabulary, and it is not durable.
package kb

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemProvider implements KB entirely in memory. Safe for concurrent use.
type MemProvider struct {
	mu      sync.Mutex
	entries []memEntry
	seq     int
	closed  bool
}

type memEntry struct {
	Entry
	updatedAt time.Time
}

// NewMemProvider returns an empty in-memory KB provider.
func NewMemProvider() *MemProvider {
	return &MemProvider{}
}

// Search matches every active entry whose title/body/tags contain at least one
// fallback term and ranks by the number of distinct terms matched, higher
// first, capped at topK.
func (p *MemProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Hit, error) {
	topK := normalizeTopK(opts.TopK)
	text := strings.TrimSpace(query)
	if text == "" {
		return []Hit{}, nil
	}
	scope := strings.TrimSpace(opts.Scope)
	terms := fallbackTerms(text)

	p.mu.Lock()
	defer p.mu.Unlock()
	var hits []Hit
	for _, e := range p.entries {
		if !matchesScope(e.Scope, scope) {
			continue
		}
		if score := matchScore(terms, e); score > 0 {
			hits = append(hits, Hit{Entry: e.Entry, Score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

// Add writes one entry; a draft whose Source matches an existing entry updates
// it with version+1 (same semantics as the SQLite backend).
func (p *MemProvider) Add(ctx context.Context, draft Entry) error {
	e, err := normalizeDraft(draft)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("kb: provider is closed")
	}
	if e.Source != "" {
		for i := range p.entries {
			if p.entries[i].Source == e.Source {
				p.entries[i].Entry.Version++
				p.entries[i].Entry.Title = e.Title
				p.entries[i].Entry.Body = e.Body
				p.entries[i].Entry.Type = e.Type
				p.entries[i].Entry.Tags = e.Tags
				p.entries[i].Entry.Scope = e.Scope
				p.entries[i].Entry.Confidence = e.Confidence
				p.entries[i].updatedAt = time.Now().UTC()
				return nil
			}
		}
	}
	p.seq++
	e.ID = fmt.Sprintf("mem-%d", p.seq)
	e.Version = 1
	p.entries = append(p.entries, memEntry{Entry: e, updatedAt: time.Now().UTC()})
	return nil
}

// Recall is a bounded search (orchestration lands in M4b).
func (p *MemProvider) Recall(ctx context.Context, query string, limit int) ([]Hit, error) {
	return p.Search(ctx, query, SearchOpts{TopK: limit})
}

// Close releases nothing but marks the provider unusable (mirrors SQLite's
// lifecycle).
func (p *MemProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

// matchesScope applies the search scope filter: empty entry scope means global,
// so a "global" filter matches both the empty scope and the literal 'global'.
func matchesScope(entryScope, filter string) bool {
	if filter == "" {
		return true
	}
	if filter == "global" {
		return entryScope == "" || entryScope == "global"
	}
	return entryScope == filter
}

// matchScore counts how many distinct fallback terms appear in the entry's
// title/body/tags (case-insensitive), with title hits weighted double so they
// outrank body-only hits like the FTS title weight.
func matchScore(terms []string, e memEntry) float64 {
	haystack := strings.ToLower(e.Title) + "\n" + strings.ToLower(e.Body) + "\n" + strings.ToLower(strings.Join(e.Tags, " "))
	title := strings.ToLower(e.Title)
	count := 0.0
	for _, t := range terms {
		if !strings.Contains(haystack, t) {
			continue
		}
		count++
		if strings.Contains(title, t) {
			count += 0.5
		}
	}
	return count
}
