// Package kb defines the knowledge-base capability seam (design.md §8, D2/D9).
//
// The KB interface is the Service of the seam's three-piece structure
// (Service / Provider / Tool): consumers depend only on this interface, never
// on a concrete backend, so swapping the backend never touches consumer code.
// M4a ships the Service plus two Providers — the default SQLite FTS5 backend
// and a tiny in-memory backend used to prove the seam boundary. The kb_* Tools
// and the recall orchestration arrive in M4b; Extract is reserved for M4c and
// is deliberately not declared here (a declared method would force stubs).
package kb

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Knowledge entry types (design.md §8, dsh-knowledge 同款数据模型).
const (
	TypePreference = "preference"
	TypeFact       = "fact"
	TypeDecision   = "decision"
	TypeProcedure  = "procedure"
	TypeLesson     = "lesson"
)

// validTypes is the fixed set of knowledge entry types.
var validTypes = map[string]bool{
	TypePreference: true,
	TypeFact:       true,
	TypeDecision:   true,
	TypeProcedure:  true,
	TypeLesson:     true,
}

// DefaultTopK is the result count used when SearchOpts.TopK or Recall's limit
// is <= 0 (kb.top_k config default).
const DefaultTopK = 5

// MaxTopK caps any requested result count so a query can never flood the
// model context (recall is bounded, design.md §8).
const MaxTopK = 100

// Entry is one knowledge entry (dsh-knowledge 同款数据模型, design.md §8).
// Version starts at 1 and is incremented on every same-source update. The
// caller-provided Version is an output: Add assigns it, never trusts it.
type Entry struct {
	ID         string // entry id; empty on Add means the provider assigns one
	Title      string
	Body       string
	Type       string // one of Type*
	Tags       []string
	Scope      string  // "" = global; otherwise an exact named scope
	Source     string  // origin (session/turn or explicit add); same source updates version+1
	Confidence float64 // in [0,1]
	Version    int     // starts at 1; +1 per same-source update
}

// Hit is one ranked search/recall result. Score is 1/(1+rank), higher is
// better (dsh-knowledge rankToScore).
type Hit struct {
	Entry Entry
	Score float64
}

// SearchOpts bounds and scopes a Search.
type SearchOpts struct {
	TopK  int    // max results; <=0 means the default (DefaultTopK)
	Scope string // optional exact scope filter; "" = all scopes
}

// RecentWrite is one entry in Stats.Recent — the title/type/timestamp of a
// recently written entry, without the (possibly large) body.
type RecentWrite struct {
	Title     string
	Type      string
	UpdatedAt time.Time
}

// Stats is a read-only snapshot of a knowledge base, used by the /kb-status
// CLI command (dispatch-m4b §4). DBSize is the on-disk database size in bytes,
// or 0 for backends without a file (in-memory).
type Stats struct {
	EntryCount int
	DBPath     string
	DBSize     int64
	Recent     []RecentWrite // most recently written first
}

// KB is the knowledge-base Service.
//
//   - Search runs FTS5 BM25 and, when that under-fills topK, supplements with
//     a Chinese bigram LIKE fallback (design.md §8).
//   - Get returns one full entry by id (kb_read, M4b).
//   - Add writes one entry, syncs the FTS index, and returns the entry with
//     the provider-assigned ID and Version (kb_add reports them so the model
//     can open the entry with kb_read). A draft whose Source matches an
//     existing entry updates it with version+1 instead of inserting
//     (dispatch-m4a §2).
//   - Recall is a bounded search (its injection orchestration lands in M4b;
//     this segment implements the retrieval logic only).
//   - Stats reports entry count / database size / recent writes for
//     /kb-status (M4b).
//
// Extract is reserved for M4c and intentionally absent. Close is part of the
// interface so a swapped provider never leaks its backend (DB file, handles).
type KB interface {
	Search(ctx context.Context, query string, opts SearchOpts) ([]Hit, error)
	Get(ctx context.Context, id string) (Entry, error)
	Add(ctx context.Context, draft Entry) (Entry, error)
	Recall(ctx context.Context, query string, limit int) ([]Hit, error)
	Stats(ctx context.Context) (Stats, error)
	Close() error
}

// normalizeDraft validates and canonicalizes an entry draft. Every provider
// shares it so the seam behaves identically regardless of backend
// (title/body bounds, type set, confidence, tags — dsh-knowledge normalizeDraft).
func normalizeDraft(d Entry) (Entry, error) {
	title := strings.TrimSpace(d.Title)
	body := strings.TrimSpace(d.Body)
	typ := strings.TrimSpace(d.Type)
	scope := strings.TrimSpace(d.Scope)
	source := strings.TrimSpace(d.Source)

	if n := len([]rune(title)); n == 0 || n > 200 {
		return Entry{}, fmt.Errorf("kb: title must contain 1-200 characters (got %d)", n)
	}
	if n := len([]rune(body)); n == 0 || n > 50_000 {
		return Entry{}, fmt.Errorf("kb: body must contain 1-50000 characters (got %d)", n)
	}
	if !validTypes[typ] {
		return Entry{}, fmt.Errorf("kb: unsupported type %q (want preference|fact|decision|procedure|lesson)", typ)
	}
	if math.IsNaN(d.Confidence) || d.Confidence < 0 || d.Confidence > 1 {
		return Entry{}, fmt.Errorf("kb: confidence must be between 0 and 1 (got %v)", d.Confidence)
	}
	return Entry{
		ID:         strings.TrimSpace(d.ID),
		Title:      title,
		Body:       body,
		Type:       typ,
		Tags:       normalizeTags(d.Tags),
		Scope:      scope,
		Source:     source,
		Confidence: d.Confidence,
	}, nil
}

// normalizeTags lowercases, trims, dedupes, sorts and caps tags at 32
// (dsh-knowledge normalizeTags).
func normalizeTags(tags []string) []string {
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}
