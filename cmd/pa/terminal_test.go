package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"personal-agent/internal/config"
	"personal-agent/internal/session"
	"personal-agent/internal/tools"
)

// makeTermApp builds a minimal app for registerTerminal / termCommand tests:
// only the fields those touch (cfg.Terminal, reg, log, currentID) are set.
// ReadIdleMS / ReadTimeoutMS are kept short so the real-session smoke stays
// fast.
func makeTermApp(enabled bool) *app {
	return &app{
		cfg: config.Config{Terminal: config.TerminalConfig{
			Enabled: enabled, ReadIdleMS: 100, ReadTimeoutMS: 2500,
		}},
		reg:       tools.New(),
		log:       session.New(),
		currentID: "s-term",
	}
}

// termPolicy whitelists the five terminal tools, mirroring what
// config.applyDefaults does when terminal.enabled is true. The wiring tests
// drive the tool objects directly (bypassing the registry), so this policy is
// only for cases that go through reg.Execute.
func termPolicy() tools.Policy {
	return tools.Policy{
		Enabled: []string{"terminal_start", "terminal_write", "terminal_read", "terminal_signal", "terminal_stop"},
	}
}

// execTerm executes one terminal tool with JSON args — the same serial tool
// path the model and /term share. It bypasses the registry policy on purpose.
func execTerm(t *testing.T, tool tools.Tool, args map[string]any) (string, error) {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Execute(context.Background(), b)
}

// TestRegisterTerminalDisabledRegistersNothing verifies the D10 gate: with
// terminal.enabled=false registerTerminal builds no tool bundle and registers
// no terminal_* tool.
func TestRegisterTerminalDisabledRegistersNothing(t *testing.T) {
	app := makeTermApp(false)
	if err := app.registerTerminal(); err != nil {
		t.Fatalf("registerTerminal: %v", err)
	}
	if app.termTools != nil {
		t.Fatal("termTools must stay nil when terminal.enabled=false")
	}
	for _, name := range specNames(app.reg) {
		if strings.HasPrefix(name, "terminal_") {
			t.Fatalf("terminal tool %q registered while terminal disabled", name)
		}
	}
}

// TestRegisterTerminalEnabledRegistersTools verifies the enabled path: the
// tool bundle is built and all five terminal_* tools land in the registry.
func TestRegisterTerminalEnabledRegistersTools(t *testing.T) {
	app := makeTermApp(true)
	if err := app.registerTerminal(); err != nil {
		t.Fatalf("registerTerminal: %v", err)
	}
	if app.termTools == nil {
		t.Fatal("termTools must be created when terminal.enabled=true")
	}
	names := specNames(app.reg)
	for _, want := range []string{"terminal_start", "terminal_write", "terminal_read", "terminal_signal", "terminal_stop"} {
		if !containsStr(names, want) {
			t.Fatalf("registered tools %v lack %q", names, want)
		}
	}
}

// TestTerminalLifecycleE2E drives a real shell session through the composed
// app's terminalAccess: start (with an initial command), write, stop, then a
// write that must fail because no session is active. It also asserts the D3
// event sink appended terminal/start and terminal/stop to the session log.
func TestTerminalLifecycleE2E(t *testing.T) {
	app := makeTermApp(true)
	if err := app.registerTerminal(); err != nil {
		t.Fatalf("registerTerminal: %v", err)
	}
	defer func() {
		if app.termSess != nil {
			app.termSess.Close()
		}
	}()

	out, err := execTerm(t, app.termTools.Start(), map[string]any{"command": "echo hi"})
	if err != nil {
		t.Fatalf("terminal_start: %v", err)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("terminal_start output = %q, want contains %q", out, "hi")
	}
	if app.termSess == nil {
		t.Fatal("start must leave an active session")
	}
	if !hasEvent(app.log, session.EventTerminalStart) {
		t.Fatal("terminal/start event missing from the session log after start")
	}

	out, err = execTerm(t, app.termTools.Write(), map[string]any{"text": "echo second"})
	if err != nil {
		t.Fatalf("terminal_write: %v", err)
	}
	if !strings.Contains(out, "second") {
		t.Fatalf("terminal_write output = %q, want contains %q", out, "second")
	}

	out, err = execTerm(t, app.termTools.Stop(), map[string]any{})
	if err != nil {
		t.Fatalf("terminal_stop: %v", err)
	}
	if !strings.Contains(out, "stopped") {
		t.Fatalf("terminal_stop output = %q, want contains %q", out, "stopped")
	}
	if app.termSess != nil {
		t.Fatal("stop must detach the active session")
	}
	if !hasEvent(app.log, session.EventTerminalStop) {
		t.Fatal("terminal/stop event missing from the session log after stop")
	}

	if _, err = execTerm(t, app.termTools.Write(), map[string]any{"text": "echo x"}); err == nil {
		t.Fatal("write after stop must fail")
	} else if !strings.Contains(err.Error(), "no active terminal session") {
		t.Fatalf("write-after-stop error = %v, want contains %q", err, "no active terminal session")
	}
}

// TestTerminalOwnerFence verifies the D5 owner fence: switching the current
// session id makes the composed terminalAccess refuse access to a session the
// new owner does not hold, and switching back restores access.
func TestTerminalOwnerFence(t *testing.T) {
	app := makeTermApp(true)
	if err := app.registerTerminal(); err != nil {
		t.Fatalf("registerTerminal: %v", err)
	}
	if _, err := execTerm(t, app.termTools.Start(), map[string]any{}); err != nil {
		t.Fatalf("terminal_start: %v", err)
	}
	defer func() {
		if app.termSess != nil {
			app.termSess.Close()
		}
	}()

	app.currentID = "s-other"
	if _, err := execTerm(t, app.termTools.Write(), map[string]any{"text": "echo x"}); err == nil {
		t.Fatal("write from a non-owner session must fail")
	} else if !strings.Contains(err.Error(), "another session") {
		t.Fatalf("owner-fence error = %v, want contains %q", err, "another session")
	}

	app.currentID = "s-term"
	out, err := execTerm(t, app.termTools.Write(), map[string]any{"text": "echo ok"})
	if err != nil {
		t.Fatalf("write after restoring owner: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("write output = %q, want contains %q", out, "ok")
	}
}

// TestTermCommandDisabled verifies /term is unavailable when the terminal is
// disabled: no tools were registered, so termCommand reports disabled.
func TestTermCommandDisabled(t *testing.T) {
	app := makeTermApp(false)
	if err := app.registerTerminal(); err != nil {
		t.Fatalf("registerTerminal: %v", err)
	}
	err := app.termCommand(context.Background(), []string{"start"})
	if err == nil {
		t.Fatal("termCommand must fail when terminal.enabled=false")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err = %v, want contains %q", err, "disabled")
	}
}
