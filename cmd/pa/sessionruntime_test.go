// sessionruntime_test.go — Phase 2 (按会话切换): applySessionRuntime resolves a
// session's per-turn provider/model/effort, per-mode prompt builder and
// permission-tier policy (session override ?? global), and restores the base
// policy afterwards. llmFor covers the dsh ModelSelection routing (a session
// pinned to a provider talks to that provider's adapter).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jabing/shutu-agent/internal/config"
	"github.com/jabing/shutu-agent/internal/llm"
	"github.com/jabing/shutu-agent/internal/prompt"
	"github.com/jabing/shutu-agent/internal/store"
	"github.com/jabing/shutu-agent/internal/tools"
)

// stubProvider is a minimal llm.Provider for routing tests; Stream always
// errors so a turn can never accidentally use it.
type stubProvider struct{ id string }

func (s stubProvider) ID() string      { return s.id }
func (s stubProvider) Available() bool { return true }
func (s stubProvider) Stream(context.Context, llm.ChatRequest) (llm.StreamReader, error) {
	return nil, errors.New("stub provider")
}

// TestLLMForRouting verifies llmFor (dsh ModelSelection 对齐): a session's
// provider override resolves to that provider's adapter; an empty id and an
// unknown id fall back to the global LLM (fail-open).
func TestLLMForRouting(t *testing.T) {
	reg := llm.NewRegistry()
	global := stubProvider{id: "global"}
	openai := stubProvider{id: "openai"}
	if err := reg.Register(stubProvider{id: "deepseek-official"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(openai); err != nil {
		t.Fatal(err)
	}
	a := &app{llm: global, llmReg: reg}

	if got := a.llmFor(""); got != global {
		t.Fatalf("llmFor(\"\") = %v, want the global LLM", got)
	}
	if got := a.llmFor("openai"); got != openai {
		t.Fatalf("llmFor(openai) = %v, want the openai adapter", got)
	}
	if got := a.llmFor("nope"); got != global {
		t.Fatalf("llmFor(unknown) = %v, want the global LLM (fail-open)", got)
	}
	// No registry at all → global.
	a2 := &app{llm: global}
	if got := a2.llmFor("openai"); got != global {
		t.Fatalf("llmFor without registry = %v, want the global LLM", got)
	}
}

func TestApplySessionRuntime(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pa.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// A registry holding exactly get_time; base policy rejects everything so a
	// "full" tier demonstrably opens the whitelist and a restore re-arms it.
	a := &app{
		cfg:    config.Config{Model: "global-model", Mode: "standard", ReasoningEffort: "high"},
		reg:    tools.New(),
		prompt: prompt.New("You are a standard agent."),
		store:  st,
	}
	a.basePolicy = tools.Policy{Enabled: []string{}} // empty → reject-all
	if err := a.reg.Register(tools.GetTime{}); err != nil {
		t.Fatal(err)
	}
	a.reg.SetPolicy(a.basePolicy)

	mustSession := func(id, provider, model, mode, effort, perm string) string {
		t.Helper()
		if err := st.CreateSession(ctx, id, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		cfg := store.SessionConfig{Provider: provider, Model: model, AgentPreset: mode, ReasoningEffort: effort, Permission: perm}
		if err := st.SetSessionConfig(ctx, id, cfg); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// 1. A bare session (no override) falls back to the globals and swaps nothing.
	mustSession("s-global", "", "", "", "", "")
	rt, restore := a.applySessionRuntime("s-global")
	if rt.model != "global-model" || rt.provider != "" || rt.effort != "high" || rt.prompt != a.prompt {
		t.Fatalf("global session runtime = (%q, %q, %q, %p), want (global-model, '', high, %p)", rt.model, rt.provider, rt.effort, rt.prompt, a.prompt)
	}
	restore()

	// 2. A model override is honoured.
	mustSession("s-model", "", "per-session-model", "", "", "")
	rt, restore = a.applySessionRuntime("s-model")
	if rt.model != "per-session-model" {
		t.Fatalf("model override runtime = %q, want per-session-model", rt.model)
	}
	restore()

	// 2b. A provider + effort override (dsh ModelSelection) routes the session.
	mustSession("s-prov", "openai", "gpt-4o", "", "max", "")
	rt, restore = a.applySessionRuntime("s-prov")
	if rt.provider != "openai" || rt.model != "gpt-4o" || rt.effort != "max" {
		t.Fatalf("provider session runtime = (%q, %q, %q), want (openai, gpt-4o, max)", rt.provider, rt.model, rt.effort)
	}
	restore()

	// 3. A minimal-mode session picks a different prompt builder (the mode's).
	mustSession("s-min", "", "", "minimal", "", "")
	rt, restore = a.applySessionRuntime("s-min")
	if rt.prompt == a.prompt {
		t.Fatalf("minimal session prompt must differ from the global builder")
	}
	restore()

	// 4. A full-permission session opens the whitelist, then restores it.
	mustSession("s-full", "", "", "", "", "full")
	rt, restore = a.applySessionRuntime("s-full")
	if _, err := a.reg.Execute(ctx, "get_time", json.RawMessage("{}")); err != nil {
		t.Fatalf("full tier should allow get_time: %v", err)
	}
	restore()
	if _, err := a.reg.Execute(ctx, "get_time", json.RawMessage("{}")); err == nil {
		t.Fatalf("get_time must be rejected again after restore (base whitelist empty)")
	}
	_ = rt

	// 5. readonly permission narrows the whitelist to the read-only tools.
	mustSession("s-ro", "", "", "", "", "readonly")
	_, restore = a.applySessionRuntime("s-ro")
	if _, err := a.reg.Execute(ctx, "get_time", json.RawMessage("{}")); err != nil {
		t.Fatalf("readonly tier should allow get_time: %v", err)
	}
	restore()
}
