package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"personal-agent/internal/config"
	"personal-agent/internal/session"
	"personal-agent/internal/tools"
)

// makeFsApp builds a minimal app for fs wiring tests: only the fields
// registerFs touches (cfg.Fs, reg, log) are set.
func makeFsApp(fsEnabled bool, root string) *app {
	return &app{
		cfg: config.Config{
			Fs: config.FsConfig{Enabled: fsEnabled, Root: root},
		},
		reg: tools.New(),
		log: session.New(),
	}
}

// fsPolicy whitelists the three fs tools so the registry Execute gate can run
// them (in production config.applyDefaults + PolicyFromConfig do this).
func fsPolicy() tools.Policy {
	return tools.Policy{
		Enabled:     []string{"fs_read", "fs_write", "fs_list"},
		Timeout:     0,
		OutputLimit: 0,
	}
}

// TestRegisterFsDisabledRegistersNothing verifies the D10 gate: with
// fs.enabled=false the composition root creates no FileService and registers
// no fs_* tool (dispatch-m6f-3 §5 / 自测: enabled=false 不注册).
func TestRegisterFsDisabledRegistersNothing(t *testing.T) {
	a := makeFsApp(false, "")
	if err := a.registerFs(); err != nil {
		t.Fatalf("registerFs: %v", err)
	}
	if a.fs != nil {
		t.Fatal("fs FileService must be nil when fs.enabled=false")
	}
	for _, spec := range a.reg.Specs() {
		switch spec.Name {
		case "fs_read", "fs_write", "fs_list":
			t.Fatalf("%s registered while fs disabled", spec.Name)
		}
	}
}

// TestRegisterFsEnabledRegistersAndValidates verifies the enabled path: the
// local FileService is created (root pinned to the test dir), the three fs_*
// tools are registered, D7 rejects bad arguments at the Execute gate, a valid
// write/read/list flow through and land fs/write, fs/read and fs/list in the
// session log (D3) without deriving into history (log-only), and an
// out-of-bounds path returns an error message.
func TestRegisterFsEnabledRegistersAndValidates(t *testing.T) {
	root := t.TempDir()
	a := makeFsApp(true, root)
	a.reg.SetPolicy(fsPolicy())
	if err := a.registerFs(); err != nil {
		t.Fatalf("registerFs: %v", err)
	}
	defer a.fs.Close()
	if a.fs == nil {
		t.Fatal("fs FileService must be created when fs.enabled=true")
	}
	if a.fs.Root() != root {
		t.Fatalf("fs root = %q, want %q", a.fs.Root(), root)
	}
	found := map[string]bool{}
	for _, s := range a.reg.Specs() {
		found[s.Name] = true
	}
	for _, name := range []string{"fs_read", "fs_write", "fs_list"} {
		if !found[name] {
			t.Fatalf("%s not registered when fs.enabled=true", name)
		}
	}

	// D7: bad arguments are rejected before any tool code runs.
	for _, tc := range []struct{ name, args string }{
		{"fs_read", `{}`},                                // missing required path
		{"fs_read", `{"path":123}`},                      // path must be a string
		{"fs_read", `{"path":"x","extra":1}`},            // additional properties rejected
		{"fs_write", `{"path":"x.txt"}`},                 // missing required content
		{"fs_write", `{"path":"x.txt","content":123}`},   // content must be a string
		{"fs_write", `{"path":"x","content":"x","e":1}`}, // additional properties rejected
		{"fs_list", `{}`},                                // missing required dir
		{"fs_list", `{"dir":1}`},                         // dir must be a string
		{"fs_list", `{"dir":"x","extra":1}`},             // additional properties rejected
	} {
		if _, err := a.reg.Execute(context.Background(), tc.name, json.RawMessage(tc.args)); err == nil {
			t.Errorf("%s with args %s must be rejected (D7)", tc.name, tc.args)
		}
	}

	// A valid write/read/list flow through the registry.
	if _, err := a.reg.Execute(context.Background(), "fs_write", json.RawMessage(`{"path":"notes.txt","content":"hello fs"}`)); err != nil {
		t.Fatalf("fs_write via registry: %v", err)
	}
	if !hasEvent(a.log, session.EventFsWrite) {
		t.Fatal("fs/write event missing from the session log after fs_write")
	}
	res, err := a.reg.Execute(context.Background(), "fs_read", json.RawMessage(`{"path":"notes.txt"}`))
	if err != nil {
		t.Fatalf("fs_read via registry: %v", err)
	}
	if res.Output != "hello fs" {
		t.Fatalf("fs_read output = %q, want hello fs", res.Output)
	}
	if !hasEvent(a.log, session.EventFsRead) {
		t.Fatal("fs/read event missing from the session log after fs_read")
	}
	res2, err := a.reg.Execute(context.Background(), "fs_list", json.RawMessage(`{"dir":"."}`))
	if err != nil {
		t.Fatalf("fs_list via registry: %v", err)
	}
	if !strings.Contains(res2.Output, "notes.txt") {
		t.Fatalf("fs_list output = %q, want it to list notes.txt", res2.Output)
	}
	if !hasEvent(a.log, session.EventFsList) {
		t.Fatal("fs/list event missing from the session log after fs_list")
	}
	// The events are log-only: nothing derives into model-visible messages.
	if msgs := a.log.DeriveHistory(); len(msgs) != 0 {
		t.Fatalf("fs/* events must not derive into messages: %+v", msgs)
	}

	// An out-of-bounds path returns an error message, never a panic.
	if _, err := a.reg.Execute(context.Background(), "fs_read", json.RawMessage(`{"path":"../escape.txt"}`)); err == nil {
		t.Fatal("fs_read of an escaping path must error at the registry")
	}
	before := len(a.log.Events())
	if _, err := a.reg.Execute(context.Background(), "fs_write", json.RawMessage(`{"path":"../../x.txt","content":"x"}`)); err == nil {
		t.Fatal("fs_write of an escaping path must error at the registry")
	}
	if after := len(a.log.Events()); after != before {
		t.Fatalf("failed fs_write logged %d events, want none (only successes log fs/* facts)", after-before)
	}
	// The rejected write must not have created anything outside the root.
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("rejected fs_write must not create %s", filepath.Join(filepath.Dir(root), "x.txt"))
	}
}
