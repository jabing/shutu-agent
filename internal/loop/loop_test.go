package loop

import (
	"context"
	"io"
	"strings"
	"testing"

	"personal-agent/internal/llm"
	"personal-agent/internal/prompt"
	"personal-agent/internal/session"
	"personal-agent/internal/tools"
)

// scriptedLLM returns a fixed per-step sequence of stream events, one Stream
// call per step, then EOF.
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

func newTestLoop(t *testing.T, llm llm.LLM) (*Loop, *session.Log, *tools.Registry) {
	t.Helper()
	reg := tools.New()
	if err := reg.Register(tools.GetTime{}); err != nil {
		t.Fatalf("register get_time: %v", err)
	}
	log := session.New()
	loop := New(Config{
		LLM:    llm,
		Log:    log,
		Tools:  reg,
		Prompt: prompt.New("You are helpful."),
		Model:  "deepseek-chat",
	})
	return loop, log, reg
}

func TestRunSimpleAnswerNoTools(t *testing.T) {
	model := &scriptedLLM{steps: [][]llm.StreamEvent{{
		{Kind: llm.StreamTextDelta, Text: "Hi "},
		{Kind: llm.StreamTextDelta, Text: "there"},
		{Kind: llm.StreamFinish, FinishReason: "stop"},
	}}}
	loop, log, _ := newTestLoop(t, model)

	var streamed strings.Builder
	loop.onText = func(delta string) { streamed.WriteString(delta) }

	if err := loop.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if streamed.String() != "Hi there" {
		t.Fatalf("streamed = %q", streamed.String())
	}

	events := log.Events()
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4 (user, 2 chunks, assistant)", len(events))
	}
	types := []string{}
	for _, ev := range events {
		types = append(types, ev.Type)
	}
	want := []string{
		session.EventUserMessage,
		session.EventAssistantChunk,
		session.EventAssistantChunk,
		session.EventAssistantMessage,
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event %d = %q, want %q (all: %v)", i, types[i], want[i], types)
		}
	}
	// The model must receive the user message inside a system-prefixed request.
	if len(model.calls) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(model.calls))
	}
	msgs := model.calls[0].Messages
	if msgs[0].Role != llm.RoleSystem {
		t.Fatalf("first message role = %v, want system", msgs[0].Role)
	}
	if len(msgs) < 2 || msgs[1].Role != llm.RoleUser || msgs[1].Content != "hello" {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestRunToolCallExecutesAndLogs(t *testing.T) {
	model := &scriptedLLM{steps: [][]llm.StreamEvent{
		{ // step 1: model asks for get_time
			{Kind: llm.StreamFinish, FinishReason: "tool_calls", ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "get_time", Arguments: "{}"},
			}},
		},
		{ // step 2: model answers
			{Kind: llm.StreamTextDelta, Text: "It is now."},
			{Kind: llm.StreamFinish, FinishReason: "stop"},
		},
	}}
	loop, log, _ := newTestLoop(t, model)

	if err := loop.Run(context.Background(), "what time is it"); err != nil {
		t.Fatalf("run: %v", err)
	}

	events := log.Events()
	var types []string
	for _, ev := range events {
		types = append(types, ev.Type)
	}
	want := []string{
		session.EventUserMessage,
		session.EventAssistantMessage,
		session.EventToolResult,
		session.EventAssistantChunk,
		session.EventAssistantMessage,
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event %d = %q, want %q (all: %v)", i, types[i], want[i], types)
		}
	}

	// Tool result must reference the call id.
	ev := events[2]
	if !strings.Contains(string(ev.Data), "call_1") {
		t.Fatalf("tool result data = %s", ev.Data)
	}

	// Two model requests: step 2 must include the tool result as a tool-role
	// message so the model sees it (D3).
	if len(model.calls) != 2 {
		t.Fatalf("llm calls = %d, want 2", len(model.calls))
	}
	last := model.calls[1].Messages
	foundTool := false
	for _, m := range last {
		if m.Role == llm.RoleTool && m.ToolCallID == "call_1" {
			foundTool = true
		}
	}
	if !foundTool {
		t.Fatalf("step 2 messages lack tool result: %+v", last)
	}
}

func TestRunUnknownToolLogsErrorAndContinues(t *testing.T) {
	model := &scriptedLLM{steps: [][]llm.StreamEvent{
		{ // step 1: model calls a tool that does not exist
			{Kind: llm.StreamFinish, FinishReason: "tool_calls", ToolCalls: []llm.ToolCall{
				{ID: "call_x", Name: "nonexistent", Arguments: "{}"},
			}},
		},
		{ // step 2: model gives up
			{Kind: llm.StreamFinish, FinishReason: "stop"},
		},
	}}
	loop, log, _ := newTestLoop(t, model)

	if err := loop.Run(context.Background(), "call something"); err != nil {
		t.Fatalf("run: %v", err)
	}
	events := log.Events()
	types := []string{}
	for _, ev := range events {
		types = append(types, ev.Type)
	}
	if len(types) < 3 || types[2] != session.EventToolError {
		t.Fatalf("expected tool/error at index 2, got %v", types)
	}
}

func TestRunCancelContext(t *testing.T) {
	model := &scriptedLLM{}
	loop, _, _ := newTestLoop(t, model)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before any step
	if err := loop.Run(ctx, "nope"); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestRunMaxSteps(t *testing.T) {
	// A model that never stops calling tools must hit the step cap.
	var steps [][]llm.StreamEvent
	for i := 0; i < maxSteps+1; i++ {
		steps = append(steps, []llm.StreamEvent{{
			Kind: llm.StreamFinish, FinishReason: "tool_calls",
			ToolCalls: []llm.ToolCall{{ID: "c", Name: "get_time", Arguments: "{}"}},
		}})
	}
	model := &scriptedLLM{steps: steps}
	loop, _, _ := newTestLoop(t, model)

	err := loop.Run(context.Background(), "loop")
	if err == nil {
		t.Fatal("expected max-steps error")
	}
	if !strings.Contains(err.Error(), "steps") {
		t.Fatalf("error = %v", err)
	}
}
