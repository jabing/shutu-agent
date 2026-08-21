// Package kb defines the knowledge-base capability seam (design.md §8, D2/D9).
//
// The KB interface is the Service of the seam's three-piece structure
// (Service / Provider / Tool): consumers depend only on this interface, never
// on a concrete backend, so swapping the backend never touches consumer code.
// M4a ships the Service plus two Providers — the default SQLite FTS5 backend
// and a tiny in-memory backend used to prove the seam boundary. M4b adds the
// kb_* consumer tools and the recall orchestration lives in cmd/pa. M4c adds
// the reserved Extract method (post-answer extraction writeback) implemented
// by both providers through the shared pipeline in extract.go.
package kb

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jabing/shutu-agent/internal/llm"
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

// Extract statuses (dispatch-m4c §1). Extract never writes through an invalid
// or unusable model output (fail-closed) and always reports an outcome; the
// caller (cmd/pa) records created | skipped | failed as a kb/extract event.
const (
	ExtractCreated   = "created"
	ExtractSkipped   = "skipped"
	ExtractDuplicate = "duplicate"
	ExtractFailed    = "failed"
)

// ExtractOpts describes one post-answer extraction job (dispatch-m4c §1). The
// current session LLM and model are carried here — the extraction model always
// follows the session model (no separate extraction config), and the job key is
// SessionID:Turn (the idempotency key into extraction_jobs).
type ExtractOpts struct {
	// LLM is the current model adapter (internal/llm). The extraction calls
	// the same model that answered the turn.
	LLM llm.LLM
	// Model is the session model name (advisory: the adapter owns the
	// effective model, deepseek.go uses its configured Model).
	Model string
	// SessionID is the session part of the job key.
	SessionID string
	// Turn is the turn part of the job key (>= 1).
	Turn int
	// UserText is this turn's user input.
	UserText string
	// AssistantText is this turn's final (non-empty) answer.
	AssistantText string
}

// ExtractWrite is one entry created by an extraction run — a bounded summary
// carried in ExtractResult so the caller can log the kb/extract event.
type ExtractWrite struct {
	ID    string
	Title string
	Type  string
}

// ExtractResult is the outcome of one extraction job. Status is one of the
// Extract* constants; Reason explains a skip or failure; Created lists the
// entries written by a successful run.
type ExtractResult struct {
	Status  string
	Reason  string
	Created []ExtractWrite
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
//   - Extract runs the post-answer extraction writeback for one session:turn
//     (M4c): idempotent claim, search context, strict-JSON model extraction
//     (fail-closed), direct write. It returns a result, not a crash, for every
//     model-output problem; a non-nil error is reserved for fatal provider
//     failures only.
//   - Stats reports entry count / database size / recent writes for
//     /kb-status (M4b).
//
// Close is part of the interface so a swapped provider never leaks its backend
// (DB file, handles).
type KB interface {
	Search(ctx context.Context, query string, opts SearchOpts) ([]Hit, error)
	Get(ctx context.Context, id string) (Entry, error)
	Add(ctx context.Context, draft Entry) (Entry, error)
	Recall(ctx context.Context, query string, limit int) ([]Hit, error)
	Extract(ctx context.Context, opts ExtractOpts) (ExtractResult, error)
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
