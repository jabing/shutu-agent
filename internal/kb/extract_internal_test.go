// White-box tests for the shared extraction pipeline (extract.go) edge cases
// that need to inject storage failures: a failing context search is a soft
// failed outcome (检索失败 fail-open, design.md §8) and a failing claim is a
// fatal provider error.
package kb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jabing/shutu-agent/internal/llm"
)

// fakeStore is a scriptable extractStore standing in for a provider.
type fakeStore struct {
	searchErr  error
	claimTaken bool
	claimErr   error
	entries    int
}

func (f *fakeStore) Search(ctx context.Context, query string, opts SearchOpts) ([]Hit, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return nil, nil
}

func (f *fakeStore) Add(ctx context.Context, draft Entry) (Entry, error) {
	f.entries++
	draft.ID = "kb-test"
	draft.Version = 1
	return draft, nil
}

func (f *fakeStore) claimExtraction(ctx context.Context, sessionID string, turn int) (bool, error) {
	if f.claimErr != nil {
		return false, f.claimErr
	}
	if f.claimTaken {
		return false, nil
	}
	f.claimTaken = true
	return true, nil
}

func (f *fakeStore) completeExtraction(ctx context.Context, sessionID string, turn int, status, reason string) error {
	return nil
}

// stubLLMForWhitebox is the minimal streaming LLM used by these pipeline tests.
type stubLLMForWhitebox struct {
	output string
}

func (l stubLLMForWhitebox) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	return &stubReaderForWhitebox{chunks: []string{l.output}}, nil
}

type stubReaderForWhitebox struct {
	chunks []string
	i      int
}

func (r *stubReaderForWhitebox) Next() (llm.StreamEvent, error) {
	if r.i >= len(r.chunks) {
		return llm.StreamEvent{}, errEOF
	}
	s := r.chunks[r.i]
	r.i++
	return llm.StreamEvent{Kind: llm.StreamTextDelta, Text: s}, nil
}

var errEOF = errors.New("eof")

// TestRunExtractionSearchFailureIsFailOpen verifies a failing context Search
// produces a soft failed outcome (no model call, nothing written, no error) —
// the fail-open contract for retrieval.
func TestRunExtractionSearchFailureIsFailOpen(t *testing.T) {
	store := &fakeStore{searchErr: errors.New("fts exploded")}
	model := stubLLMForWhitebox{output: `{"candidates":[{"action":"create","title":"x","body":"y","type":"fact"}]}`}
	res, err := runExtraction(context.Background(), store, ExtractOpts{
		LLM: model, Model: "m", SessionID: "s1", Turn: 1,
		UserText: "hello", AssistantText: "hi",
	})
	if err != nil {
		t.Fatalf("runExtraction: %v (search failure must be fail-open, not an error)", err)
	}
	if res.Status != ExtractFailed || !strings.Contains(res.Reason, "search failed") {
		t.Fatalf("status/reason = %q/%q, want failed with search failure", res.Status, res.Reason)
	}
	if store.entries != 0 {
		t.Fatalf("entries = %d, want 0", store.entries)
	}
}

// TestRunExtractionClaimErrorIsFatal verifies a storage failure on the
// idempotency claim is a fatal error (the caller turns it into a failed event).
func TestRunExtractionClaimErrorIsFatal(t *testing.T) {
	store := &fakeStore{claimErr: errors.New("db locked")}
	res, err := runExtraction(context.Background(), store, ExtractOpts{
		LLM: stubLLMForWhitebox{}, Model: "m", SessionID: "s1", Turn: 1,
		UserText: "hello", AssistantText: "hi",
	})
	if err == nil {
		t.Fatalf("runExtraction = %+v, want a fatal claim error", res)
	}
}

// TestRunExtractionDuplicateSkipsModel verifies a pre-claimed job key returns
// duplicate without calling the model.
func TestRunExtractionDuplicateSkipsModel(t *testing.T) {
	store := &fakeStore{claimTaken: true}
	res, err := runExtraction(context.Background(), store, ExtractOpts{
		LLM: stubLLMForWhitebox{}, Model: "m", SessionID: "s1", Turn: 1,
		UserText: "hello", AssistantText: "hi",
	})
	if err != nil || res.Status != ExtractDuplicate {
		t.Fatalf("duplicate = %+v err=%v, want duplicate", res, err)
	}
	if store.entries != 0 {
		t.Fatalf("entries = %d, want 0", store.entries)
	}
}
