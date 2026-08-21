// Black-box tests for the M4c composition-root wiring (dispatch-m4c §1/§2):
// extractTurn runs after a completed turn, records the kb/extract event
// (created / skipped / failed + reason), is idempotent via the extraction job
// claim, is skipped when kb.extraction is false, and is fail-open — an
// extraction failure never blocks and never returns an error.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jabing/shutu-agent/internal/config"
	"github.com/jabing/shutu-agent/internal/kb"
	"github.com/jabing/shutu-agent/internal/llm"
	"github.com/jabing/shutu-agent/internal/session"
)

// extractStubLLM is a scriptable llm.LLM used by extractTurn tests.
type extractStubLLM struct {
	output string
	err    error
	calls  int
}

func (l *extractStubLLM) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	l.calls++
	if l.err != nil {
		return nil, l.err
	}
	return &extractStubReader{events: []llm.StreamEvent{
		{Kind: llm.StreamTextDelta, Text: l.output},
		{Kind: llm.StreamFinish, FinishReason: "stop"},
	}}, nil
}

type extractStubReader struct {
	events []llm.StreamEvent
	i      int
}

func (r *extractStubReader) Next() (llm.StreamEvent, error) {
	if r.i >= len(r.events) {
		return llm.StreamEvent{}, io.EOF
	}
	ev := r.events[r.i]
	r.i++
	return ev, nil
}

// newExtractTestApp builds an app with a fresh in-memory kb and a completed
// turn (user/message + assistant/message) already in the log.
func newExtractTestApp(kbCfg config.KBConfig, model llm.LLM) *app {
	a := &app{
		cfg:       config.Config{KB: kbCfg},
		llm:       model,
		kb:        kb.NewMemProvider(),
		currentID: "s1",
		log:       session.New(),
	}
	a.log.Append(session.EventUserMessage, session.NewUserMessage("请记住我周末不工作"))
	a.log.Append(session.EventAssistantMessage, session.NewAssistantMessage("好的，我记住了：你偏好周末不工作。", nil, "stop"))
	return a
}

// extractEvents returns the kb/extract events in the log.
func extractEvents(t *testing.T, a *app) []struct {
	Status  string   `json:"status"`
	Session string   `json:"session"`
	Turn    int      `json:"turn"`
	Reason  string   `json:"reason"`
	IDs     []string `json:"ids"`
} {
	t.Helper()
	var out []struct {
		Status  string   `json:"status"`
		Session string   `json:"session"`
		Turn    int      `json:"turn"`
		Reason  string   `json:"reason"`
		IDs     []string `json:"ids"`
	}
	for _, ev := range a.log.Events() {
		if ev.Type != session.EventKBExtract {
			continue
		}
		var d struct {
			Status  string   `json:"status"`
			Session string   `json:"session"`
			Turn    int      `json:"turn"`
			Reason  string   `json:"reason"`
			IDs     []string `json:"ids"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			t.Fatalf("unmarshal kb/extract: %v", err)
		}
		out = append(out, d)
	}
	return out
}

// TestExtractTurnCreatedLogsEventAndWrites verifies a successful run records a
// created kb/extract event carrying the written entry id, and the entry is
// immediately retrievable through the kb (as kb_search would find it).
func TestExtractTurnCreatedLogsEventAndWrites(t *testing.T) {
	model := &extractStubLLM{output: `{"candidates":[{"action":"create","title":"记住的偏好","body":"unique-term-turn 用户偏好周末不用工作","type":"preference","confidence":0.9}]}`}
	a := newExtractTestApp(config.KBConfig{Enabled: true}, model)

	a.extractTurn(context.Background(), "请记住我周末不工作")

	evs := extractEvents(t, a)
	if len(evs) != 1 {
		t.Fatalf("kb/extract events = %d, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Status != "created" || ev.Session != "s1" || ev.Turn != 1 || len(ev.IDs) != 1 || ev.IDs[0] == "" {
		t.Fatalf("kb/extract payload = %+v", ev)
	}
	hits, err := a.kb.Search(context.Background(), "unique-term-turn", kb.SearchOpts{TopK: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Entry.Title != "记住的偏好" {
		t.Fatalf("hits = %+v, want the extracted entry", hits)
	}
}

// TestExtractTurnFailureIsFailOpen verifies an extraction failure is recorded
// as a failed event (with the reason) and never returned as an error — the next
// answer is unaffected.
func TestExtractTurnFailureIsFailOpen(t *testing.T) {
	model := &extractStubLLM{err: errors.New("network down")}
	a := newExtractTestApp(config.KBConfig{Enabled: true}, model)

	a.extractTurn(context.Background(), "请记住我周末不工作") // must not panic or error

	evs := extractEvents(t, a)
	if len(evs) != 1 || evs[0].Status != "failed" || !strings.Contains(evs[0].Reason, "network down") {
		t.Fatalf("kb/extract events = %+v, want one failed with the model error", evs)
	}
	if st, err := a.kb.Stats(context.Background()); err != nil || st.EntryCount != 0 {
		t.Fatalf("kb stats = %+v err=%v, want 0 entries", st, err)
	}
}

// TestExtractTurnSkippedWhenDisabled verifies kb.extraction: false skips the
// extraction entirely — no kb/extract event, no writes, no model call.
func TestExtractTurnSkippedWhenDisabled(t *testing.T) {
	falsePtr := false
	model := &extractStubLLM{output: `{"candidates":[{"action":"create","title":"x","body":"y","type":"fact"}]}`}
	a := newExtractTestApp(config.KBConfig{Enabled: true, Extraction: &falsePtr}, model)

	a.extractTurn(context.Background(), "请记住我周末不工作")

	if evs := extractEvents(t, a); len(evs) != 0 {
		t.Fatalf("kb/extract events = %+v, want none when extraction is disabled", evs)
	}
	if st, err := a.kb.Stats(context.Background()); err != nil || st.EntryCount != 0 {
		t.Fatalf("kb stats = %+v err=%v, want 0 entries", st, err)
	}
	if model.calls != 0 {
		t.Fatalf("model called %d times, want 0", model.calls)
	}
}

// TestExtractTurnSkippedWhenNoAnswer verifies a turn without a final assistant
// message is a skipped outcome with a reason, not a crash.
func TestExtractTurnSkippedWhenNoAnswer(t *testing.T) {
	model := &extractStubLLM{}
	a := newExtractTestApp(config.KBConfig{Enabled: true}, model)
	a.log = session.New() // replace with a log that has no assistant message
	a.log.Append(session.EventUserMessage, session.NewUserMessage("只有问题没有回答"))

	a.extractTurn(context.Background(), "只有问题没有回答")

	evs := extractEvents(t, a)
	if len(evs) != 1 || evs[0].Status != "skipped" || !strings.Contains(evs[0].Reason, "no final assistant message") {
		t.Fatalf("kb/extract events = %+v, want one skipped with a reason", evs)
	}
	if model.calls != 0 {
		t.Fatalf("model called %d times, want 0 (no answer to extract from)", model.calls)
	}
}

// TestExtractTurnDuplicateMapsToSkipped verifies the idempotency contract at
// the composition root: re-running the same turn's extraction (replay) yields a
// skipped "already extracted" event and never re-writes.
func TestExtractTurnDuplicateMapsToSkipped(t *testing.T) {
	model := &extractStubLLM{output: `{"candidates":[{"action":"create","title":"记住的偏好","body":"unique-term-replay 用户偏好周末不用工作","type":"preference"}]}`}
	a := newExtractTestApp(config.KBConfig{Enabled: true}, model)

	a.extractTurn(context.Background(), "请记住我周末不工作")
	a.extractTurn(context.Background(), "请记住我周末不工作") // replay of the same turn

	evs := extractEvents(t, a)
	if len(evs) != 2 {
		t.Fatalf("kb/extract events = %d, want 2 (created then skipped)", len(evs))
	}
	if evs[0].Status != "created" || evs[1].Status != "skipped" || !strings.Contains(evs[1].Reason, "already extracted") {
		t.Fatalf("kb/extract payloads = %+v, want created then skipped(already extracted)", evs)
	}
	if model.calls != 1 {
		t.Fatalf("model called %d times, want 1 (replay must not re-extract)", model.calls)
	}
	hits, err := a.kb.Search(context.Background(), "unique-term-replay", kb.SearchOpts{TopK: 5})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits = %+v err=%v, want exactly one entry", hits, err)
	}
}

// TestExtractTurnIgnoredWhenKBDisabled verifies a nil kb (kb.enabled=false,
// D10) makes extractTurn a silent no-op.
func TestExtractTurnIgnoredWhenKBDisabled(t *testing.T) {
	model := &extractStubLLM{output: `{"candidates":[{"action":"create","title":"x","body":"y","type":"fact"}]}`}
	a := newExtractTestApp(config.KBConfig{Enabled: false}, model)
	a.kb = nil // matches the runtime state when kb is disabled (D10)

	a.extractTurn(context.Background(), "请记住我周末不工作")

	if evs := extractEvents(t, a); len(evs) != 0 {
		t.Fatalf("kb/extract events = %+v, want none when kb is disabled", evs)
	}
	if model.calls != 0 {
		t.Fatalf("model called %d times, want 0", model.calls)
	}
}
