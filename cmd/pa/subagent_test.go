package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"personal-agent/internal/config"
	"personal-agent/internal/llm"
	"personal-agent/internal/prompt"
	"personal-agent/internal/session"
	"personal-agent/internal/tools"
)

// subagentStubLLM returns a fixed single-step stream (assistant answer, no
// tool calls) so a spawned child completes immediately.
type subagentStubLLM struct{}

func (subagentStubLLM) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	return &subagentStubReader{events: []llm.StreamEvent{
		{Kind: llm.StreamTextDelta, Text: "child answer"},
		{Kind: llm.StreamFinish, FinishReason: "stop"},
	}}, nil
}

type subagentStubReader struct {
	events []llm.StreamEvent
	i      int
}

func (r *subagentStubReader) Next() (llm.StreamEvent, error) {
	if r.i >= len(r.events) {
		return llm.StreamEvent{}, io.EOF
	}
	ev := r.events[r.i]
	r.i++
	return ev, nil
}

// makeSubagentApp builds a minimal app for registerSubagent tests: only the
// fields registerSubagent touches (cfg.Subagent/cfg.Model, reg, llm, prompt,
// log, currentID) are set.
func makeSubagentApp(enabled bool) *app {
	return &app{
		cfg: config.Config{
			Model:    "m",
			Subagent: config.SubagentConfig{Enabled: enabled, MaxDepth: 8, DefaultProvider: "spawn"},
		},
		reg:       tools.New(),
		log:       session.New(),
		llm:       subagentStubLLM{},
		prompt:    prompt.New("You are a subagent."),
		currentID: "s-test",
	}
}

// subagentPolicy whitelists the four subagent tools so registry Execute can
// run them (in production config.applyDefaults + PolicyFromConfig do this).
func subagentPolicy() tools.Policy {
	return tools.Policy{
		Enabled:     []string{"subagent_spawn", "subagent_status", "subagent_cancel", "subagent_list"},
		Timeout:     0, // no per-tool deadline in tests
		OutputLimit: 0,
	}
}

func hasEvent(log *session.Log, typ string) bool {
	for _, ev := range log.Events() {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

func countEvent(log *session.Log, typ string) int {
	n := 0
	for _, ev := range log.Events() {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// TestRegisterSubagentDisabledRegistersNothing verifies the D10 gate: with
// subagent.enabled=false the composition root creates no runtime and registers
// no subagent_* tool (dispatch-m5b-2 §4).
func TestRegisterSubagentDisabledRegistersNothing(t *testing.T) {
	app := makeSubagentApp(false)
	if err := app.registerSubagent(); err != nil {
		t.Fatalf("registerSubagent: %v", err)
	}
	if app.subagents != nil {
		t.Fatal("subagent runtime must be nil when subagent.enabled=false")
	}
	for _, spec := range app.reg.Specs() {
		if strings.HasPrefix(spec.Name, "subagent_") {
			t.Fatalf("subagent tool %q registered while subagent disabled", spec.Name)
		}
	}
}

// TestRegisterSubagentEnabledRegistersAndLogsEvents verifies the enabled path:
// the runtime is created, all four subagent_* tools are registered, D7 rejects
// bad arguments at the Execute gate, a valid subagent_spawn returns the child
// id and logs subagent/start, and observing the settled child through
// subagent_status logs subagent/end exactly once (D3 wiring on the serial tool
// path).
func TestRegisterSubagentEnabledRegistersAndLogsEvents(t *testing.T) {
	app := makeSubagentApp(true)
	app.reg.SetPolicy(subagentPolicy())
	if err := app.registerSubagent(); err != nil {
		t.Fatalf("registerSubagent: %v", err)
	}
	defer app.subagents.Close()
	if app.subagents == nil {
		t.Fatal("subagent runtime must be created when subagent.enabled=true")
	}
	specs := app.reg.Specs()
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Name)
	}
	for _, want := range []string{"subagent_spawn", "subagent_status", "subagent_cancel", "subagent_list"} {
		if !containsStr(names, want) {
			t.Fatalf("registered tools %v lack %q", names, want)
		}
	}

	// D7: bad arguments are rejected before any tool code runs.
	for _, tc := range []struct {
		name string
		args string
	}{
		{"subagent_spawn", `{}`},                       // missing required prompt
		{"subagent_spawn", `{"prompt":"x","extra":1}`}, // additional properties rejected
		{"subagent_status", `{}`},                      // missing required id
		{"subagent_status", `{"id":123}`},              // id must be a string
		{"subagent_cancel", `{"id":false}`},            // wrong id type
	} {
		if _, err := app.reg.Execute(context.Background(), tc.name, json.RawMessage(tc.args)); err == nil {
			t.Errorf("%s with args %s must be rejected (D7)", tc.name, tc.args)
		}
	}

	// A valid spawn flows through the registry and returns the child id.
	res, err := app.reg.Execute(context.Background(), "subagent_spawn", json.RawMessage(`{"prompt":"do research","label":"researcher"}`))
	if err != nil {
		t.Fatalf("subagent_spawn via registry: %v", err)
	}
	if !strings.Contains(res.Output, "started subagent spawn-1") {
		t.Fatalf("subagent_spawn output = %q, want started subagent spawn-1", res.Output)
	}
	if !hasEvent(app.log, session.EventSubagentStart) {
		t.Fatal("subagent/start event missing from the session log after subagent_spawn")
	}

	// Observe the settled child: subagent_status returns the result and
	// subagent/end lands in the log exactly once.
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := app.reg.Execute(context.Background(), "subagent_status", json.RawMessage(`{"id":"spawn-1"}`))
		if err == nil && strings.Contains(status.Output, "settled") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not settle within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := countEvent(app.log, session.EventSubagentEnd); got != 1 {
		t.Fatalf("subagent/end count = %d, want exactly 1", got)
	}
}
