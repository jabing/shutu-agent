package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"personal-agent/internal/session"
)

// eventCapture records one emitted event's type and payload.
type eventCapture struct {
	typ  string
	data any
}

// TestWorkflowToolExecuteFormatsReport verifies Execute drives the engine,
// renders the per-task summary (header + completed tasks + bounded output),
// and emits the workflow/run event with the lean counts payload (D3).
func TestWorkflowToolExecuteFormatsReport(t *testing.T) {
	fs := &fakeSpawn{}
	eng := mustEngine(t, fs, 0)
	var events []eventCapture
	tool := NewWorkflowRunTool(eng, func(typ string, data any) {
		events = append(events, eventCapture{typ: typ, data: data})
	})
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":[
		{"id":"A","prompt":"任务A"},
		{"id":"B","prompt":"任务B"}
	]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"workflow_run: 2 tasks", "A: completed", "B: completed", "output: 默认输出"} {
		if !strings.Contains(out, want) {
			t.Errorf("report %q lacks %q", out, want)
		}
	}
	if len(events) != 1 || events[0].typ != session.EventWorkflowRun {
		t.Fatalf("events = %+v, want exactly one %s event", events, session.EventWorkflowRun)
	}
	raw, err := json.Marshal(events[0].data)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var p struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Total != 2 || p.Completed != 2 || p.Failed != 0 {
		t.Errorf("workflow/run payload = %+v, want total=2 completed=2 failed=0", p)
	}
}

// TestWorkflowToolExecuteCycleError: a cyclic DAG surfaces as an error
// containing the cycle marker (ErrCycle passes through, wrapped).
func TestWorkflowToolExecuteCycleError(t *testing.T) {
	fs := &fakeSpawn{}
	eng := mustEngine(t, fs, 0)
	tool := NewWorkflowRunTool(eng, nil)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":[
		{"id":"A","prompt":"p","depends_on":["B"]},
		{"id":"B","prompt":"p","depends_on":["A"]}
	]}`))
	if err == nil {
		t.Fatal("Execute: want an error for a cyclic DAG")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error = %v, want it to contain cycle", err)
	}
}

// TestWorkflowToolExecuteRejectsEmptyTasks: an absent or empty tasks array is
// rejected before any engine work.
func TestWorkflowToolExecuteRejectsEmptyTasks(t *testing.T) {
	fs := &fakeSpawn{}
	eng := mustEngine(t, fs, 0)
	tool := NewWorkflowRunTool(eng, nil)
	for _, args := range []string{`{}`, `{"tasks":[]}`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(args)); err == nil {
			t.Errorf("Execute(%s) must be rejected (empty tasks)", args)
		}
	}
}

// TestWorkflowToolExecuteFailureRendersError: a failed task renders the error
// line instead of the output line, and the workflow/run event counts it.
func TestWorkflowToolExecuteFailureRendersError(t *testing.T) {
	fs := &fakeSpawn{fn: func(ctx context.Context, prompt string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if strings.Contains(prompt, "任务B") {
			return "", errors.New("boom")
		}
		return "ok", nil
	}}
	eng := mustEngine(t, fs, 0)
	var events []eventCapture
	tool := NewWorkflowRunTool(eng, func(typ string, data any) {
		events = append(events, eventCapture{typ: typ, data: data})
	})
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":[
		{"id":"A","prompt":"任务A"},
		{"id":"B","prompt":"任务B"}
	]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "B: failed") || !strings.Contains(out, "error: boom") {
		t.Errorf("report %q must render B as failed with the error", out)
	}
	if strings.Contains(out, "A: failed") {
		t.Errorf("report %q marks A failed, want completed", out)
	}
	raw, err := json.Marshal(events[0].data)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var p struct {
		Total     int `json:"total"`
		Completed int `json:"completed"`
		Failed    int `json:"failed"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Total != 2 || p.Completed != 1 || p.Failed != 1 {
		t.Errorf("workflow/run payload = %+v, want total=2 completed=1 failed=1", p)
	}
}
