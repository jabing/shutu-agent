package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"personal-agent/internal/compaction"
	"personal-agent/internal/config"
	"personal-agent/internal/llm"
	"personal-agent/internal/session"
)

// byteTokens is a deterministic 1-token-per-byte surface estimator for tests
// (mirrors compaction's own test estimator, used to drive pressure exactly).
func byteTokens(log *session.Log) int {
	total := 0
	for _, m := range log.DeriveHistory() {
		total += len(m.Content)
		for _, tc := range m.ToolCalls {
			total += len(tc.Name) + len(tc.Arguments)
		}
	}
	return total
}

// compactStubLLM answers every summary request with a fixed text (or an error).
type compactStubLLM struct {
	text string
	err  error
}

func (f *compactStubLLM) Stream(_ context.Context, _ llm.ChatRequest) (llm.StreamReader, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &compactStubReader{text: f.text}, nil
}

type compactStubReader struct {
	done bool
	text string
}

func (r *compactStubReader) Next() (llm.StreamEvent, error) {
	if !r.done {
		r.done = true
		return llm.StreamEvent{Kind: llm.StreamTextDelta, Text: r.text}, nil
	}
	return llm.StreamEvent{}, io.EOF
}

// threeTurnLog builds u1 a1 u2 a2 u3 a3 (seqs 1..6).
func threeTurnLog(t *testing.T) *session.Log {
	t.Helper()
	l := session.New()
	pairs := []struct {
		typ  string
		data any
	}{
		{session.EventUserMessage, session.NewUserMessage("q1")},
		{session.EventAssistantMessage, session.NewAssistantMessage("a1", nil, "stop")},
		{session.EventUserMessage, session.NewUserMessage("q2")},
		{session.EventAssistantMessage, session.NewAssistantMessage("a2", nil, "stop")},
		{session.EventUserMessage, session.NewUserMessage("q3")},
		{session.EventAssistantMessage, session.NewAssistantMessage("a3", nil, "stop")},
	}
	for _, p := range pairs {
		if _, err := l.Append(p.typ, p.data); err != nil {
			t.Fatalf("append %s: %v", p.typ, err)
		}
	}
	return l
}

// eventTypes returns the event types of a log in order.
func eventTypes(events []session.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Type)
	}
	return out
}

// indexOf returns the first index of typ in types, or -1.
func indexOf(types []string, typ string) int {
	for i, t := range types {
		if t == typ {
			return i
		}
	}
	return -1
}

// captureStdout runs f while capturing everything printed to os.Stdout and
// returns it. os.Stdout is restored before returning.
func captureStdout(f func()) string {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	f()
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// makeCompactApp builds a minimal app for compaction tests: a compaction
// config with a small pressure threshold (5) and retained tail (1), and a fresh
// log. The engine and estimator are wired by each test.
func makeCompactApp(enabled bool) *app {
	return &app{
		cfg: config.Config{
			Compaction: config.CompactionConfig{
				Enabled:        enabled,
				TokenThreshold: 5,
				RetainTurns:    1,
			},
		},
		log: session.New(),
	}
}

// basicEngine builds a BasicEngine matching the app's small threshold/tail with
// the given tokenizer and stub LLM (consistent with the byteTokens estimator).
func basicEngine(est compaction.TokenEstimator, llm llm.LLM) compaction.Engine {
	return compaction.NewBasic(compaction.BasicOpts{
		Tokenizer:      est,
		LLM:            llm,
		Model:          "m",
		TokenThreshold: 5,
		RetainTurns:    1,
	})
}

// markerRange decodes the shadowed [start, end] of the summary marker
// user/message in the log (surfaceOp.replace), or returns ok=false when none
// exists.
func markerRange(t *testing.T, log *session.Log) ([2]int64, bool) {
	t.Helper()
	for _, ev := range log.Events() {
		if ev.Type != session.EventUserMessage {
			continue
		}
		var d struct {
			Text      string                 `json:"text"`
			SurfaceOp *session.SurfaceReplace `json:"surfaceOp,omitempty"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			continue
		}
		if d.SurfaceOp != nil && d.SurfaceOp.Op == "replace" {
			return [2]int64{d.SurfaceOp.Start, d.SurfaceOp.End}, true
		}
	}
	return [2]int64{}, false
}

// TestRegisterCompactionDisabledCreatesNothing verifies the D10 gate: with
// compaction.enabled=false the composition root creates no engine (dispatch
// -m5c-2b §2; the no-injector assertion lives in the Pass-2 PreStep test).
func TestRegisterCompactionDisabledCreatesNothing(t *testing.T) {
	app := makeCompactApp(false)
	if err := app.registerCompaction(); err != nil {
		t.Fatalf("registerCompaction: %v", err)
	}
	if app.compaction != nil {
		t.Fatal("compaction engine must be nil when compaction.enabled=false")
	}
}

// TestRegisterCompactionEnabledCreatesEngine verifies the enabled path: the
// BasicEngine is created (the "compaction" pre-step injector registration is
// asserted by the Pass-2 PreStep test).
func TestRegisterCompactionEnabledCreatesEngine(t *testing.T) {
	app := makeCompactApp(true)
	app.llm = &compactStubLLM{text: "S"}
	if err := app.registerCompaction(); err != nil {
		t.Fatalf("registerCompaction: %v", err)
	}
	if app.compaction == nil {
		t.Fatal("compaction engine must be created when compaction.enabled=true")
	}
}

// TestCompactCommandDisabledReportsUnavailable verifies /compact with
// compaction.enabled=false prints the unavailable message (D10) and never
// touches the log.
func TestCompactCommandDisabledReportsUnavailable(t *testing.T) {
	app := makeCompactApp(false)
	app.log = threeTurnLog(t)
	out := captureStdout(func() {
		if err := app.compactCommand(context.Background(), nil); err != nil {
			t.Errorf("compactCommand: %v", err)
		}
	})
	if !strings.Contains(out, "disabled") {
		t.Fatalf("output = %q, want a disabled message", out)
	}
	if got := len(app.log.Events()); got != 6 {
		t.Fatalf("disabled /compact must not touch the log, got %d events", got)
	}
}

// TestCompactCommandEnabledCompactsAndLogs verifies /compact with the engine
// wired: it performs one manual compaction (summary marker + fold), appends
// compaction/start → compaction/summary → compaction/end exactly once in order
// (D3, serial command path), and prints the summary, shadowed range and tokens
// saved.
func TestCompactCommandEnabledCompactsAndLogs(t *testing.T) {
	app := makeCompactApp(true)
	app.log = threeTurnLog(t)
	app.compaction = basicEngine(nil, &compactStubLLM{text: "S"})

	out := captureStdout(func() {
		if err := app.compactCommand(context.Background(), nil); err != nil {
			t.Errorf("compactCommand: %v", err)
		}
	})
	if !strings.Contains(out, "compacted") {
		t.Fatalf("output = %q, want a compacted report", out)
	}
	if !strings.Contains(out, "summary: S") {
		t.Fatalf("output = %q, want the summary printed", out)
	}
	// The summary marker landed and shadows the foldable prefix [1,4]
	// (RetainTurns=1 keeps q3/a3).
	r, ok := markerRange(t, app.log)
	if !ok {
		t.Fatal("summary marker user/message missing after /compact")
	}
	if r != [2]int64{1, 4} {
		t.Fatalf("shadowed range = %v, want [1 4]", r)
	}
	// compaction/start, compaction/summary, compaction/end each exactly once,
	// and start → summary → end in that order.
	types := eventTypes(app.log.Events())
	if n := countEvent(app.log, session.EventCompactionStart); n != 1 {
		t.Fatalf("compaction/start count = %d, want exactly 1 (%v)", n, types)
	}
	if n := countEvent(app.log, session.EventCompactionSummary); n != 1 {
		t.Fatalf("compaction/summary count = %d, want exactly 1 (%v)", n, types)
	}
	if n := countEvent(app.log, session.EventCompactionEnd); n != 1 {
		t.Fatalf("compaction/end count = %d, want exactly 1 (%v)", n, types)
	}
	if si, mi, ei := indexOf(types, session.EventCompactionStart), indexOf(types, session.EventCompactionSummary), indexOf(types, session.EventCompactionEnd); !(si < mi && mi < ei) {
		t.Fatalf("event order = start(%d) summary(%d) end(%d), want start<summary<end", si, mi, ei)
	}
	// The folded history substitutes the summary for the shadowed prefix.
	msgs := app.log.DeriveHistory()
	if len(msgs) != 3 || msgs[0].Content != "S" || msgs[1].Content != "q3" || msgs[2].Content != "a3" {
		t.Fatalf("derived = %+v, want [S q3 a3]", msgs)
	}
}

// TestCompactCommandRegion verifies /compact region <start> <end> routes to
// CompactRegion and logs the event chain once; bad arguments are rejected.
func TestCompactCommandRegion(t *testing.T) {
	app := makeCompactApp(true)
	app.log = threeTurnLog(t)
	app.compaction = basicEngine(nil, &compactStubLLM{text: "S"})

	out := captureStdout(func() {
		if err := app.compactCommand(context.Background(), []string{"region", "1", "4"}); err != nil {
			t.Errorf("compactCommand region: %v", err)
		}
	})
	if !strings.Contains(out, "compacted") {
		t.Fatalf("output = %q, want a compacted report", out)
	}
	r, ok := markerRange(t, app.log)
	if !ok || r != [2]int64{1, 4} {
		t.Fatalf("region shadowed range = %v (ok=%v), want [1 4]", r, ok)
	}
	if n := countEvent(app.log, session.EventCompactionStart); n != 1 {
		t.Fatalf("compaction/start count = %d, want exactly 1", n)
	}

	// Bad region args are rejected with a usage error.
	if err := app.compactCommand(context.Background(), []string{"region", "abc", "4"}); err == nil {
		t.Fatal("region with non-integer seqs must be rejected")
	}
	if err := app.compactCommand(context.Background(), []string{"nonsense"}); err == nil {
		t.Fatal("unknown /compact args must be rejected")
	}
}
