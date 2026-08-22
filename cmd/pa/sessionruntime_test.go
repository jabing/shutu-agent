// sessionruntime_test.go — Phase 2 (按会话切换): applySessionRuntime resolves a
// session's per-turn model, per-mode prompt builder and permission-tier policy
// (session override ?? global), and restores the base policy afterwards.
package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jabing/shutu-agent/internal/config"
	"github.com/jabing/shutu-agent/internal/prompt"
	"github.com/jabing/shutu-agent/internal/store"
	"github.com/jabing/shutu-agent/internal/tools"
)

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
		cfg:    config.Config{Model: "global-model", Mode: "standard"},
		reg:    tools.New(),
		prompt: prompt.New("You are a standard agent."),
		store:  st,
	}
	a.basePolicy = tools.Policy{Enabled: []string{}} // empty → reject-all
	if err := a.reg.Register(tools.GetTime{}); err != nil {
		t.Fatal(err)
	}
	a.reg.SetPolicy(a.basePolicy)

	mustSession := func(id, model, mode, perm string) string {
		t.Helper()
		if err := st.CreateSession(ctx, id, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		cfg := store.SessionConfig{Model: model, AgentPreset: mode, Permission: perm}
		if err := st.SetSessionConfig(ctx, id, cfg); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// 1. A bare session (no override) falls back to the globals and swaps nothing.
	mustSession("s-global", "", "", "")
	rt, restore := a.applySessionRuntime("s-global")
	if rt.model != "global-model" || rt.prompt != a.prompt {
		t.Fatalf("global session runtime = (%q, %p), want (%q, %p)", rt.model, rt.prompt, "global-model", a.prompt)
	}
	restore()

	// 2. A model override is honoured.
	mustSession("s-model", "per-session-model", "", "")
	rt, restore = a.applySessionRuntime("s-model")
	if rt.model != "per-session-model" {
		t.Fatalf("model override runtime = %q, want per-session-model", rt.model)
	}
	restore()

	// 3. A minimal-mode session picks a different prompt builder (the mode's).
	mustSession("s-min", "", "minimal", "")
	rt, restore = a.applySessionRuntime("s-min")
	if rt.prompt == a.prompt {
		t.Fatalf("minimal session prompt must differ from the global builder")
	}
	restore()

	// 4. A full-permission session opens the whitelist, then restores it.
	mustSession("s-full", "", "", "full")
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
	mustSession("s-ro", "", "", "readonly")
	_, restore = a.applySessionRuntime("s-ro")
	if _, err := a.reg.Execute(ctx, "get_time", json.RawMessage("{}")); err != nil {
		t.Fatalf("readonly tier should allow get_time: %v", err)
	}
	restore()
}
