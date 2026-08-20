// fssearch_test.go — the D-GAP-1 wiring tests (docs/dispatch-gap-1.md §5):
// registerFsSearch's D10 gate (disabled registers nothing), the enabled path
// (fs_search registered + a real search over a temp directory returns the
// matching line), and the config-layer whitelist rule (fs_search.enabled: true
// ⇒ tools.enabled carries fs_search). The makeFsSearchApp / fsSearchPolicy
// pattern mirrors the fs_test / eval_test harnesses.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"personal-agent/internal/config"
	"personal-agent/internal/fssearch"
	"personal-agent/internal/tools"
)

// makeFsSearchApp builds a minimal app for registerFsSearch tests: only the
// fields registerFsSearch touches (cfg.FsSearch, reg) are set.
func makeFsSearchApp(enabled bool) *app {
	return &app{
		cfg: config.Config{
			FsSearch: config.FsSearchConfig{Enabled: enabled},
		},
		reg: tools.New(),
	}
}

// fsSearchPolicy whitelists fs_search so the registry Execute gate can run it
// (in production config.applyDefaults + PolicyFromConfig do this).
func fsSearchPolicy() tools.Policy {
	return tools.Policy{
		Enabled:     []string{fssearch.FsSearchToolName},
		Timeout:     0, // no per-tool deadline in tests
		OutputLimit: 0,
	}
}

// TestRegisterFsSearchDisabledRegistersNothing verifies the D10 gate: with
// fs_search.enabled=false the composition root registers no fs_search tool
// (dispatch-gap-1 §5).
func TestRegisterFsSearchDisabledRegistersNothing(t *testing.T) {
	a := makeFsSearchApp(false)
	if err := a.registerFsSearch(); err != nil {
		t.Fatalf("registerFsSearch: %v", err)
	}
	for _, spec := range a.reg.Specs() {
		if spec.Name == fssearch.FsSearchToolName {
			t.Fatalf("%s registered while fs_search disabled", spec.Name)
		}
	}
}

// TestRegisterFsSearchEnabledRegistersAndSearches verifies the enabled path:
// fs_search is registered and a real search over a temp directory returns the
// matching file:line (dispatch-gap-1 §5 E2E).
func TestRegisterFsSearchEnabledRegistersAndSearches(t *testing.T) {
	a := makeFsSearchApp(true)
	a.reg.SetPolicy(fsSearchPolicy())
	if err := a.registerFsSearch(); err != nil {
		t.Fatalf("registerFsSearch: %v", err)
	}
	found := false
	for _, s := range a.reg.Specs() {
		if s.Name == fssearch.FsSearchToolName {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s not registered when fs_search.enabled=true", fssearch.FsSearchToolName)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), []byte("alpha\nneedle here\nomega\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	args, err := json.Marshal(map[string]any{"path": root, "query": "needle"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, err := a.reg.Execute(context.Background(), fssearch.FsSearchToolName, args)
	if err != nil {
		t.Fatalf("fs_search via registry: %v", err)
	}
	if !strings.Contains(res.Output, ":2: needle here") {
		t.Fatalf("fs_search output = %q, want the :2: needle here hit", res.Output)
	}
	if !strings.Contains(res.Output, "1 matches") {
		t.Fatalf("fs_search output = %q, want the match count", res.Output)
	}
}

// TestFsSearchWhitelist verifies the config-layer whitelist rule: fs_search.
// enabled: true makes config.applyDefaults append fs_search to tools.enabled
// (dispatch-gap-1 §4).
func TestFsSearchWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("fs_search:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.FsSearch.Enabled {
		t.Error("fs_search.enabled should be true")
	}
	if !containsStr(cfg.Tools.Enabled, fssearch.FsSearchToolName) {
		t.Errorf("whitelist %v lacks %q after fs_search.enabled", cfg.Tools.Enabled, fssearch.FsSearchToolName)
	}
}
