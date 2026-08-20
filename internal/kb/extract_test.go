// Black-box tests for the M4c extraction writeback (dispatch-m4c §1/§2): the
// post-answer pipeline runs identically on both providers, is idempotent over
// session:turn, writes nothing through invalid model output (fail-closed), and
// never surfaces a model problem as an error (fail-open — the caller records a
// failed kb/extract event instead).
package kb_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"personal-agent/internal/kb"
	"personal-agent/internal/llm"
)

// stubLLM is a scriptable llm.LLM for extraction tests: it streams one fixed
// output (or fails immediately) and records every request so tests can assert
// the extraction framing reached the current model.
type stubLLM struct {
	output string
	err    error
	calls  []llm.ChatRequest
}

func (l *stubLLM) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	l.calls = append(l.calls, req)
	if l.err != nil {
		return nil, l.err
	}
	return &stubReader{events: []llm.StreamEvent{
		{Kind: llm.StreamTextDelta, Text: l.output},
		{Kind: llm.StreamFinish, FinishReason: "stop"},
	}}, nil
}

type stubReader struct {
	events []llm.StreamEvent
	i      int
}

func (r *stubReader) Next() (llm.StreamEvent, error) {
	if r.i >= len(r.events) {
		return llm.StreamEvent{}, io.EOF
	}
	ev := r.events[r.i]
	r.i++
	return ev, nil
}

// bothProviders runs a subtest per backend, exercising the exact same consumer
// code against SQLite and memory (the seam's interface-boundary test style).
func bothProviders(t *testing.T, fn func(t *testing.T, k kb.KB)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, openSQLite(t)) })
	t.Run("in-memory", func(t *testing.T) { fn(t, kb.NewMemProvider()) })
}

// countEntries reads the entry count through Stats (proves "nothing written").
func countEntries(t *testing.T, k kb.KB) int {
	t.Helper()
	st, err := k.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	return st.EntryCount
}

// extractOpts is a helper building a default extraction job.
func extractOpts(model llm.LLM, sessionID string, turn int) kb.ExtractOpts {
	return kb.ExtractOpts{
		LLM:           model,
		Model:         "deepseek-chat",
		SessionID:     sessionID,
		Turn:          turn,
		UserText:      "请记住我周末不工作",
		AssistantText: "好的，我记住了：你偏好周末不工作。",
	}
}

// TestExtractCreatesAndIsSearchable verifies a successful strict-JSON run
// writes the entry and it is immediately retrievable via Search (the same
// retrieval kb_search uses), and that the extraction request carried the
// system prompt + conversation frame to the current model.
func TestExtractCreatesAndIsSearchable(t *testing.T) {
	bothProviders(t, func(t *testing.T, k kb.KB) {
		model := &stubLLM{output: `{"candidates":[{"action":"create","title":"记住的偏好","body":"unique-term-kbq 用户偏好周末不用工作","type":"preference","tags":["偏好","工作"],"confidence":0.9,"reason":"user stated a durable preference"}]}`}
		res, err := k.Extract(context.Background(), extractOpts(model, "s1", 1))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if res.Status != kb.ExtractCreated {
			t.Fatalf("status = %q, want created (reason=%q)", res.Status, res.Reason)
		}
		if len(res.Created) != 1 || res.Created[0].Type != "preference" || res.Created[0].ID == "" {
			t.Fatalf("created = %+v, want one preference entry with an id", res.Created)
		}
		if countEntries(t, k) != 1 {
			t.Fatalf("entry count = %d, want 1", countEntries(t, k))
		}
		hits, err := k.Search(context.Background(), "unique-term-kbq", kb.SearchOpts{TopK: 5})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(hits) != 1 || hits[0].Entry.Title != "记住的偏好" || hits[0].Entry.Type != "preference" {
			t.Fatalf("hits = %+v, want the extracted entry", hits)
		}
		// The current model received exactly one request: system prompt + the
		// JSON frame carrying the conversation.
		if len(model.calls) != 1 || len(model.calls[0].Messages) != 2 ||
			model.calls[0].Messages[0].Role != llm.RoleSystem ||
			model.calls[0].Messages[1].Role != llm.RoleUser {
			t.Fatalf("extraction call = %+v", model.calls)
		}
		if !strings.Contains(model.calls[0].Messages[1].Text(), "周末不工作") ||
			!strings.Contains(model.calls[0].Messages[1].Text(), "conversation") {
			t.Fatalf("extraction frame lacks the conversation: %s", model.calls[0].Messages[1].Text())
		}
	})
}

// TestExtractIdempotent verifies the extraction_jobs claim: re-running the same
// session:turn is a duplicate outcome, calls the model again for nothing, and
// never writes a second entry (重放不重复写, dispatch-m4c §1).
func TestExtractIdempotent(t *testing.T) {
	bothProviders(t, func(t *testing.T, k kb.KB) {
		model := &stubLLM{output: `{"candidates":[{"action":"create","title":"记住的偏好","body":"unique-term-idy 用户偏好周末不用工作","type":"preference","confidence":0.9}]}`}
		opts := extractOpts(model, "s1", 1)
		first, err := k.Extract(context.Background(), opts)
		if err != nil || first.Status != kb.ExtractCreated {
			t.Fatalf("first extract = %+v err=%v, want created", first, err)
		}
		second, err := k.Extract(context.Background(), opts)
		if err != nil {
			t.Fatalf("second extract: %v", err)
		}
		if second.Status != kb.ExtractDuplicate {
			t.Fatalf("second status = %q, want duplicate", second.Status)
		}
		if len(model.calls) != 1 {
			t.Fatalf("model called %d times, want 1 (replay must not re-extract)", len(model.calls))
		}
		if countEntries(t, k) != 1 {
			t.Fatalf("entry count = %d, want 1 (no duplicate write)", countEntries(t, k))
		}
		hits, err := k.Search(context.Background(), "unique-term-idy", kb.SearchOpts{TopK: 5})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(hits) != 1 {
			t.Fatalf("hits = %d, want 1 (no duplicated entry)", len(hits))
		}
	})
}

// TestExtractInvalidJSONFailClosed verifies non-JSON model output is rejected
// (fail-closed): status failed with an "invalid JSON" reason, nothing written,
// and no error propagated.
func TestExtractInvalidJSONFailClosed(t *testing.T) {
	bothProviders(t, func(t *testing.T, k kb.KB) {
		model := &stubLLM{output: "not json at all"}
		res, err := k.Extract(context.Background(), extractOpts(model, "s1", 1))
		if err != nil {
			t.Fatalf("Extract: %v (must not be an error — fail-open)", err)
		}
		if res.Status != kb.ExtractFailed || !strings.Contains(res.Reason, "invalid JSON") {
			t.Fatalf("status/reason = %q/%q, want failed with invalid JSON", res.Status, res.Reason)
		}
		if countEntries(t, k) != 0 {
			t.Fatalf("entry count = %d, want 0 (fail-closed)", countEntries(t, k))
		}
	})
}

// TestExtractRejectsInvalidCandidates verifies per-candidate fail-closed
// rejection: an unknown type and an out-of-scope field each reject the whole
// job (nothing written, status failed), and a mixed batch writes only the
// valid candidate while recording the rejection in the reason.
func TestExtractRejectsInvalidCandidates(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{"unknown type", `{"candidates":[{"action":"create","title":"x","body":"y","type":"recipe","confidence":0.8}]}`},
		{"out-of-scope field", `{"candidates":[{"action":"create","title":"x","body":"y","type":"fact","scope":"project-x"}]}`},
		{"unknown action", `{"candidates":[{"action":"explode","title":"x","body":"y","type":"fact"}]}`},
		{"missing title", `{"candidates":[{"action":"create","body":"y","type":"fact"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bothProviders(t, func(t *testing.T, k kb.KB) {
				model := &stubLLM{output: tc.output}
				res, err := k.Extract(context.Background(), extractOpts(model, "s1", 1))
				if err != nil {
					t.Fatalf("Extract: %v", err)
				}
				if res.Status != kb.ExtractFailed {
					t.Fatalf("status = %q, want failed (reason=%q)", res.Status, res.Reason)
				}
				if countEntries(t, k) != 0 {
					t.Fatalf("entry count = %d, want 0 (fail-closed)", countEntries(t, k))
				}
			})
		})
	}

	// Mixed batch: the valid candidate is written, the invalid one is not, and
	// the reason records the rejection.
	t.Run("partial write keeps valid candidate", func(t *testing.T) {
		bothProviders(t, func(t *testing.T, k kb.KB) {
			model := &stubLLM{output: `{"candidates":[
				{"action":"create","title":"有效条目","body":"unique-term-partial 有效内容","type":"fact","confidence":0.8},
				{"action":"create","title":"坏条目","body":"bad","type":"recipe"}
			]}`}
			res, err := k.Extract(context.Background(), extractOpts(model, "s1", 1))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if res.Status != kb.ExtractCreated || len(res.Created) != 1 || res.Created[0].Title != "有效条目" {
				t.Fatalf("result = %+v, want created with the one valid entry", res)
			}
			if !strings.Contains(res.Reason, "rejected 1 invalid") {
				t.Fatalf("reason = %q, want a rejection note", res.Reason)
			}
			if countEntries(t, k) != 1 {
				t.Fatalf("entry count = %d, want 1", countEntries(t, k))
			}
		})
	})
}

// TestExtractSkipNoCandidates verifies the model choosing not to extract is a
// normal skipped outcome: empty candidates, a skip candidate, and a top-level
// {"skip":true} all write nothing and report skipped.
func TestExtractSkipNoCandidates(t *testing.T) {
	outputs := []string{
		`{"candidates":[]}`,
		`{"candidates":[{"action":"skip","title":"","body":"","type":"fact"}]}`,
		`{"skip":true}`,
	}
	for _, out := range outputs {
		t.Run(strings.ReplaceAll(out, " ", "_"), func(t *testing.T) {
			bothProviders(t, func(t *testing.T, k kb.KB) {
				model := &stubLLM{output: out}
				res, err := k.Extract(context.Background(), extractOpts(model, "s1", 1))
				if err != nil {
					t.Fatalf("Extract: %v", err)
				}
				if res.Status != kb.ExtractSkipped {
					t.Fatalf("status = %q, want skipped (reason=%q)", res.Status, res.Reason)
				}
				if countEntries(t, k) != 0 {
					t.Fatalf("entry count = %d, want 0", countEntries(t, k))
				}
			})
		})
	}
}

// TestExtractModelErrorFailOpen verifies an extraction model failure is a soft
// failed outcome (status failed + reason), never an error — the caller logs the
// kb/extract event and the next answer is unaffected.
func TestExtractModelErrorFailOpen(t *testing.T) {
	bothProviders(t, func(t *testing.T, k kb.KB) {
		model := &stubLLM{err: errors.New("network down")}
		res, err := k.Extract(context.Background(), extractOpts(model, "s1", 1))
		if err != nil {
			t.Fatalf("Extract: %v (must not be an error — fail-open)", err)
		}
		if res.Status != kb.ExtractFailed || !strings.Contains(res.Reason, "network down") {
			t.Fatalf("status/reason = %q/%q, want failed with model error", res.Status, res.Reason)
		}
		if countEntries(t, k) != 0 {
			t.Fatalf("entry count = %d, want 0", countEntries(t, k))
		}
	})
}

// TestExtractSkipsMissingTurnText verifies a turn with no usable user input or
// final answer is skipped without touching the model.
func TestExtractSkipsMissingTurnText(t *testing.T) {
	bothProviders(t, func(t *testing.T, k kb.KB) {
		model := &stubLLM{output: `{"candidates":[{"action":"create","title":"x","body":"y","type":"fact"}]}`}
		opts := extractOpts(model, "s1", 1)
		opts.UserText = ""
		res, err := k.Extract(context.Background(), opts)
		if err != nil || res.Status != kb.ExtractSkipped {
			t.Fatalf("empty user: %+v err=%v, want skipped", res, err)
		}
		opts = extractOpts(model, "s1", 2)
		opts.AssistantText = ""
		res, err = k.Extract(context.Background(), opts)
		if err != nil || res.Status != kb.ExtractSkipped {
			t.Fatalf("empty assistant: %+v err=%v, want skipped", res, err)
		}
		if len(model.calls) != 0 {
			t.Fatalf("model called %d times, want 0 (nothing to extract)", len(model.calls))
		}
	})
}
