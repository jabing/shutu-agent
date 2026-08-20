package subagent

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"personal-agent/internal/llm"
	"personal-agent/internal/prompt"
	"personal-agent/internal/session"
	"personal-agent/internal/tools"
)

// scriptedLLM returns a fixed per-step sequence of stream events (one Stream
// call per step), then EOF — the subagent-test counterpart of the loop's
// scriptedLLM.
type scriptedLLM struct {
	steps [][]llm.StreamEvent
	calls []llm.ChatRequest
}

func (s *scriptedLLM) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	s.calls = append(s.calls, req)
	if len(s.steps) == 0 {
		return &scriptedReader{}, nil
	}
	events := s.steps[0]
	s.steps = s.steps[1:]
	return &scriptedReader{events: events}, nil
}

type scriptedReader struct {
	events []llm.StreamEvent
	i      int
}

func (r *scriptedReader) Next() (llm.StreamEvent, error) {
	if r.i >= len(r.events) {
		return llm.StreamEvent{}, io.EOF
	}
	ev := r.events[r.i]
	r.i++
	return ev, nil
}

// blockingLLM returns a reader that blocks on Next until ctx is done, standing
// in for a live child that honors cancellation.
type blockingLLM struct {
	started chan struct{}
	once    sync.Once
}

func (m *blockingLLM) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	m.once.Do(func() { close(m.started) })
	return &blockingReader{ctx: ctx}, nil
}

type blockingReader struct{ ctx context.Context }

func (r *blockingReader) Next() (llm.StreamEvent, error) {
	<-r.ctx.Done()
	return llm.StreamEvent{}, r.ctx.Err()
}

// mixedLLM answers the first Stream immediately and blocks on the second
// (honoring ctx), letting one provider own one completed + one live child.
type mixedLLM struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	first   []llm.StreamEvent
}

func (m *mixedLLM) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		return &scriptedReader{events: m.first}, nil
	}
	close(m.started)
	return &blockingReader{ctx: ctx}, nil
}

// errorLLM fails its Stream immediately, standing in for a model/transport
// failure during the child run.
type errorLLM struct{ err error }

func (m *errorLLM) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	return nil, m.err
}

// TestSpawnFullRound runs one complete child-agent round with a fake LLM and
// verifies the child owns an independent, replayable session (the parent log is
// never polluted), the terminal result is returned, and ListChildren reflects
// the settled child under its parent.
func TestSpawnFullRound(t *testing.T) {
	parentLog := session.New()
	model := &scriptedLLM{steps: [][]llm.StreamEvent{{
		{Kind: llm.StreamTextDelta, Text: "child "},
		{Kind: llm.StreamTextDelta, Text: "answer"},
		{Kind: llm.StreamFinish, FinishReason: "stop"},
	}}}
	prov := NewSpawnProvider(Deps{
		Log:    parentLog,
		LLM:    model,
		Tools:  tools.New(),
		Prompt: prompt.New("You are a subagent."),
		Model:  "deepseek-chat",
	})
	ctx := context.Background()

	run, err := prov.Start(ctx, StartRequest{
		Label: "researcher", Prompt: "summarize the docs", ParentSessionID: "parent-1",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if run.ID == "" {
		t.Fatal("run id must be non-empty")
	}
	res, err := run.Result(ctx)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.Output != "child answer" {
		t.Fatalf("output = %q, want the child's last non-empty assistant text", res.Output)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("stopReason = %q, want %q", res.StopReason, StopCompleted)
	}

	// The parent session log is never polluted: the child owns an independent
	// session (ADR 残余风险: 子代理不串扰父会话日志).
	if n := len(parentLog.Events()); n != 0 {
		t.Fatalf("parent log has %d events, want 0 (child must be independent)", n)
	}

	// The child session is complete and replayable: user/message first, the
	// assistant/message answer last, and DeriveHistory reproduces the turn.
	childLog, ok := prov.ChildLog(run.ID)
	if !ok {
		t.Fatalf("child log for %s not found", run.ID)
	}
	events := childLog.Events()
	if len(events) != 4 {
		t.Fatalf("child events = %d, want 4 (user, 2 chunks, assistant)", len(events))
	}
	if events[0].Type != session.EventUserMessage {
		t.Fatalf("first child event = %q, want user/message", events[0].Type)
	}
	if events[len(events)-1].Type != session.EventAssistantMessage {
		t.Fatalf("last child event = %q, want assistant/message", events[len(events)-1].Type)
	}
	hist := childLog.DeriveHistory()
	if len(hist) != 2 || hist[0].Role != llm.RoleUser || hist[0].Text() != "summarize the docs" ||
		hist[1].Role != llm.RoleAssistant || hist[1].Text() != "child answer" {
		t.Fatalf("derived child history = %+v, want user prompt + assistant answer", hist)
	}
	if len(model.calls) != 1 {
		t.Fatalf("child llm calls = %d, want 1", len(model.calls))
	}

	// ListChildren: the settled child is listed under its parent, not under a
	// different parent, and no longer running.
	children, err := prov.ListChildren(ctx, "parent-1")
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(children) != 1 || children[0].ID != run.ID || children[0].Label != "researcher" || children[0].Running {
		t.Fatalf("children = %+v, want one settled child %s", children, run.ID)
	}
	if other, _ := prov.ListChildren(ctx, "other-parent"); len(other) != 0 {
		t.Fatalf("children under a different parent = %+v, want none", other)
	}
	if err := prov.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestSpawnDepthExceeded verifies the delegation-depth enforcement: a child of
// a depth-1 child is depth 2 and is rejected when MaxDepth=1; MaxDepth=0 means
// no limit.
func TestSpawnDepthExceeded(t *testing.T) {
	prov := NewSpawnProvider(Deps{
		LLM:    &scriptedLLM{},
		Tools:  tools.New(),
		Prompt: prompt.New("x"),
		Model:  "m",
	})
	ctx := context.Background()

	run, err := prov.Start(ctx, StartRequest{Label: "r", Prompt: "go", ParentSessionID: "root", MaxDepth: 1})
	if err != nil {
		t.Fatalf("start depth-1 child: %v", err)
	}
	if _, err := prov.Start(ctx, StartRequest{Label: "g", Prompt: "go", ParentSessionID: run.ID, MaxDepth: 1}); !errors.Is(err, ErrDepthExceeded) {
		t.Fatalf("grandchild err = %v, want ErrDepthExceeded", err)
	}
	// MaxDepth 0 removes the limit.
	if _, err := prov.Start(ctx, StartRequest{Label: "g2", Prompt: "go", ParentSessionID: run.ID, MaxDepth: 0}); err != nil {
		t.Fatalf("no-limit grandchild: %v", err)
	}
	if err := prov.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestSpawnCancelAborts verifies Cancel cancels a live child's current turn and
// the terminal result maps to aborted; cancelling an already-finished child
// fails.
func TestSpawnCancelAborts(t *testing.T) {
	started := make(chan struct{})
	prov := NewSpawnProvider(Deps{
		LLM:    &blockingLLM{started: started},
		Tools:  tools.New(),
		Prompt: prompt.New("x"),
		Model:  "m",
	})
	ctx := context.Background()

	run, err := prov.Start(ctx, StartRequest{Label: "slow", Prompt: "work", ParentSessionID: "p"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	<-started // the child is inside its first model request
	if err := run.Cancel("user interrupt"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	res, err := run.Result(ctx)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.StopReason != StopAborted {
		t.Fatalf("stopReason = %q, want %q", res.StopReason, StopAborted)
	}
	if err := run.Cancel("again"); err == nil {
		t.Fatal("cancelling a finished child must fail")
	}
	if err := prov.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestSpawnStopReasonMapping covers the finish-reason → StopReason mapping for
// clean completions (max-tokens and refusal included).
func TestSpawnStopReasonMapping(t *testing.T) {
	cases := []struct {
		finish string
		want   string
	}{
		{"stop", StopCompleted},
		{"", StopCompleted},
		{"length", StopMaxTokens},
		{"max_tokens", StopMaxTokens},
		{"content_filter", StopRefusal},
	}
	for _, tc := range cases {
		model := &scriptedLLM{steps: [][]llm.StreamEvent{{
			{Kind: llm.StreamFinish, FinishReason: tc.finish},
		}}}
		prov := NewSpawnProvider(Deps{
			LLM:    model,
			Tools:  tools.New(),
			Prompt: prompt.New("x"),
			Model:  "m",
		})
		run, err := prov.Start(context.Background(), StartRequest{Prompt: "go", ParentSessionID: "p"})
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		res, err := run.Result(context.Background())
		if err != nil {
			t.Fatalf("result: %v", err)
		}
		if res.StopReason != tc.want {
			t.Fatalf("finish %q: stopReason = %q, want %q", tc.finish, res.StopReason, tc.want)
		}
		if err := prov.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
}

// TestSpawnStopError verifies a failed child run maps to StopReason error.
func TestSpawnStopError(t *testing.T) {
	prov := NewSpawnProvider(Deps{
		LLM:    &errorLLM{err: errors.New("model boom")},
		Tools:  tools.New(),
		Prompt: prompt.New("x"),
		Model:  "m",
	})
	run, err := prov.Start(context.Background(), StartRequest{Prompt: "go", ParentSessionID: "p"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res, err := run.Result(context.Background())
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.StopReason != StopError {
		t.Fatalf("stopReason = %q, want %q", res.StopReason, StopError)
	}
	if err := prov.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestSpawnCloseNoLeak verifies Close cancels a live child and awaits every
// child (one completed + one blocking), returns promptly, and rejects Start
// afterwards — so no background goroutine leaks.
func TestSpawnCloseNoLeak(t *testing.T) {
	started := make(chan struct{})
	mixed := &mixedLLM{
		started: started,
		first:   []llm.StreamEvent{{Kind: llm.StreamFinish, FinishReason: "stop"}},
	}
	prov := NewSpawnProvider(Deps{
		LLM:    mixed,
		Tools:  tools.New(),
		Prompt: prompt.New("x"),
		Model:  "m",
	})
	ctx := context.Background()

	run1, err := prov.Start(ctx, StartRequest{Label: "fast", Prompt: "a", ParentSessionID: "p"})
	if err != nil {
		t.Fatalf("start fast: %v", err)
	}
	if _, err := run1.Result(ctx); err != nil {
		t.Fatalf("fast result: %v", err)
	}
	if _, err := prov.Start(ctx, StartRequest{Label: "slow", Prompt: "b", ParentSessionID: "p"}); err != nil {
		t.Fatalf("start slow: %v", err)
	}
	<-started // the slow child is live and blocking

	closed := make(chan error, 1)
	go func() { closed <- prov.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung on a live child")
	}
	if _, err := prov.Start(ctx, StartRequest{Prompt: "c", ParentSessionID: "p"}); !errors.Is(err, ErrProviderClosed) {
		t.Fatalf("start after close err = %v, want ErrProviderClosed", err)
	}
}

// userMessageText returns the text of the first RoleUser message of a model
// request — the child's prompt as the model actually saw it (the loop builds
// the request as [system, ...injected context, ...history], so the user prompt
// is not at index 0).
func userMessageText(req llm.ChatRequest) string {
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser {
			return m.Text()
		}
	}
	return ""
}

// TestWithAcceptance verifies the pure withAcceptance helper: empty criteria
// leave the prompt untouched; non-empty criteria append the
// "验收标准（交付自检）" section with one "- <criterion>" line per criterion;
// blank criteria are skipped; and the original prompt is preserved.
func TestWithAcceptance(t *testing.T) {
	if got := withAcceptance("do X", nil); got != "do X" {
		t.Fatalf("withAcceptance(nil) = %q, want prompt unchanged", got)
	}
	if got := withAcceptance("do X", []string{}); got != "do X" {
		t.Fatalf("withAcceptance(empty) = %q, want prompt unchanged", got)
	}

	prompt := "do X"
	out := withAcceptance(prompt, []string{"contains:输出含报告", "llm:结论合理", "  "})
	if !strings.HasPrefix(out, prompt) {
		t.Fatalf("withAcceptance output = %q, want the original prompt preserved", out)
	}
	if !strings.Contains(out, "验收标准（交付自检）") {
		t.Fatalf("withAcceptance output = %q, want the acceptance section header", out)
	}
	for _, c := range []string{"contains:输出含报告", "llm:结论合理"} {
		if !strings.Contains(out, "- "+c) {
			t.Fatalf("withAcceptance output = %q, want criterion line %q", out, "- "+c)
		}
	}
	if n := strings.Count(out, "\n- "); n != 2 {
		t.Fatalf("withAcceptance output = %q, want 2 criterion bullets, got %d", out, n)
	}
}

// TestSpawnInjectsAcceptance verifies SpawnProvider injects the acceptance
// criteria into the child's prompt (the model's first request carries the
// "验收标准" section and every criterion) and the run completes normally.
func TestSpawnInjectsAcceptance(t *testing.T) {
	model := &scriptedLLM{steps: [][]llm.StreamEvent{{
		{Kind: llm.StreamFinish, FinishReason: "stop"},
	}}}
	prov := NewSpawnProvider(Deps{
		Log:    session.New(),
		LLM:    model,
		Tools:  tools.New(),
		Prompt: prompt.New("You are a subagent."),
		Model:  "deepseek-chat",
	})
	ctx := context.Background()

	run, err := prov.Start(ctx, StartRequest{
		Prompt:             "do X",
		AcceptanceCriteria: []string{"contains:输出含报告", "llm:结论合理"},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res, err := run.Result(ctx)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("stopReason = %q, want %q", res.StopReason, StopCompleted)
	}
	if len(model.calls) != 1 {
		t.Fatalf("child llm calls = %d, want 1", len(model.calls))
	}
	user := userMessageText(model.calls[0])
	if !strings.Contains(user, "验收标准") || !strings.Contains(user, "contains:输出含报告") || !strings.Contains(user, "llm:结论合理") {
		t.Fatalf("child user message = %q, want the acceptance section with both criteria", user)
	}
	if err := prov.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestSpawnNoCriteriaNoInjection verifies that without AcceptanceCriteria the
// child's user message is exactly the original prompt — no acceptance section
// is injected.
func TestSpawnNoCriteriaNoInjection(t *testing.T) {
	model := &scriptedLLM{steps: [][]llm.StreamEvent{{
		{Kind: llm.StreamFinish, FinishReason: "stop"},
	}}}
	prov := NewSpawnProvider(Deps{
		LLM:    model,
		Tools:  tools.New(),
		Prompt: prompt.New("x"),
		Model:  "m",
	})
	ctx := context.Background()

	run, err := prov.Start(ctx, StartRequest{Prompt: "do X", ParentSessionID: "p"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := run.Result(ctx); err != nil {
		t.Fatalf("result: %v", err)
	}
	if len(model.calls) != 1 {
		t.Fatalf("child llm calls = %d, want 1", len(model.calls))
	}
	user := userMessageText(model.calls[0])
	if user != "do X" {
		t.Fatalf("child user message = %q, want the original prompt unchanged", user)
	}
	if strings.Contains(user, "验收标准") {
		t.Fatalf("child user message = %q, want no acceptance section", user)
	}
	if err := prov.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestRuntimeWithSpawnProvider integrates the spawn provider into a Runtime:
// register, Start through the runtime (with capability validation passing for
// MaxDepth against the depth-limit provider), ListChildren aggregation, and
// Close releasing the provider.
func TestRuntimeWithSpawnProvider(t *testing.T) {
	rt := NewRuntime()
	parentLog := session.New()
	model := &scriptedLLM{steps: [][]llm.StreamEvent{{
		{Kind: llm.StreamTextDelta, Text: "done"},
		{Kind: llm.StreamFinish, FinishReason: "stop"},
	}}}
	prov := NewSpawnProvider(Deps{
		Log:    parentLog,
		LLM:    model,
		Tools:  tools.New(),
		Prompt: prompt.New("x"),
		Model:  "m",
	})
	if err := rt.RegisterProvider(prov); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx := context.Background()

	run, err := rt.Start(ctx, "spawn", StartRequest{Label: "r", Prompt: "go", ParentSessionID: "p"})
	if err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	if run.ID == "" {
		t.Fatal("run id must be non-empty")
	}
	res, err := run.Result(ctx)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.StopReason != StopCompleted {
		t.Fatalf("stopReason = %q, want %q", res.StopReason, StopCompleted)
	}
	// MaxDepth passes the capability gate because spawn declares DepthLimit.
	if _, err := rt.Start(ctx, "spawn", StartRequest{Prompt: "x", ParentSessionID: "p", MaxDepth: 5}); err != nil {
		t.Fatalf("runtime start with max_depth: %v", err)
	}
	children, err := rt.ListChildren(ctx, "p")
	if err != nil {
		t.Fatalf("runtime list children: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("children = %+v, want 2 spawned under p", children)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("runtime close: %v", err)
	}
}
