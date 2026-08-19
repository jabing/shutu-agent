package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != DefaultModel {
		t.Errorf("model = %q, want %q", cfg.Model, DefaultModel)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Errorf("base_url = %q, want %q", cfg.BaseURL, DefaultBaseURL)
	}
	if cfg.DataDir != DefaultDataDir {
		t.Errorf("data_dir = %q, want %q", cfg.DataDir, DefaultDataDir)
	}
	if cfg.PromptsDir != DefaultPromptsDir {
		t.Errorf("prompts_dir = %q, want %q", cfg.PromptsDir, DefaultPromptsDir)
	}
}

func TestLoadParsesFileAndFillsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "model: deepseek-reasoner\nbase_url: https://example.com\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "deepseek-reasoner" {
		t.Errorf("model = %q", cfg.Model)
	}
	if cfg.BaseURL != "https://example.com" {
		t.Errorf("base_url = %q", cfg.BaseURL)
	}
	// Omitted fields fall back to defaults.
	if cfg.DataDir != DefaultDataDir {
		t.Errorf("data_dir = %q, want %q", cfg.DataDir, DefaultDataDir)
	}
	if cfg.PromptsDir != DefaultPromptsDir {
		t.Errorf("prompts_dir = %q, want %q", cfg.PromptsDir, DefaultPromptsDir)
	}
}

func TestLoadRejectsInvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("model: [unclosed"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid yaml")
	}
}

func TestLoadEmptyBaseURLMeansProviderDefault(t *testing.T) {
	// An explicitly-empty base_url must stay empty (=> provider default),
	// not be silently replaced.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("base_url: \"\""), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BaseURL != "" {
		t.Errorf("base_url = %q, want empty", cfg.BaseURL)
	}
}

// M3: a config without a tools section must carry the safe defaults — the
// read-only whitelist, a 30s deadline, and a 64KB output limit (dispatch-m3).
func TestLoadToolsDefaultsWhenAbsent(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"get_time", "read_file"}
	if !reflect.DeepEqual(cfg.Tools.Enabled, want) {
		t.Errorf("enabled = %v, want %v", cfg.Tools.Enabled, want)
	}
	if cfg.Tools.Timeout.Duration != DefaultToolTimeout {
		t.Errorf("timeout = %v, want %v", cfg.Tools.Timeout, DefaultToolTimeout)
	}
	if cfg.Tools.OutputLimit != DefaultOutputLimit {
		t.Errorf("output_limit = %d, want %d", cfg.Tools.OutputLimit, DefaultOutputLimit)
	}
	if cfg.Tools.RunCommand.Enabled {
		t.Error("run_command must be disabled by default")
	}
}

func TestLoadParsesToolsSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
tools:
  enabled: [read_file]
  timeout: 5s
  output_limit: 4096
  run_command:
    enabled: true
    timeout: 1m
    workdir: C:\work
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Tools.Enabled, []string{"read_file", "run_command"}) {
		t.Errorf("enabled = %v", cfg.Tools.Enabled)
	}
	if cfg.Tools.Timeout.Duration != 5*time.Second {
		t.Errorf("timeout = %v", cfg.Tools.Timeout)
	}
	if cfg.Tools.OutputLimit != 4096 {
		t.Errorf("output_limit = %d", cfg.Tools.OutputLimit)
	}
	if !cfg.Tools.RunCommand.Enabled {
		t.Error("run_command.enabled should be true")
	}
	if cfg.Tools.RunCommand.Timeout.Duration != time.Minute {
		t.Errorf("run_command.timeout = %v", cfg.Tools.RunCommand.Timeout)
	}
	if cfg.Tools.RunCommand.Workdir != `C:\work` {
		t.Errorf("run_command.workdir = %q", cfg.Tools.RunCommand.Workdir)
	}
}

func TestLoadRunCommandEnabledAppendsToWhitelist(t *testing.T) {
	// tools.run_command.enabled: true is the single switch that turns the
	// execution tool on: it must also become whitelisted (design.md §5).
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("tools:\n  run_command:\n    enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range []string{"get_time", "read_file", "run_command"} {
		if !contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v lacks %q", cfg.Tools.Enabled, name)
		}
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("tools:\n  timeout: not-a-duration\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestLoadAcceptsEmptyRunCommandTimeoutMeansGlobal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("tools:\n  run_command:\n    enabled: true\n    timeout: \"\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tools.RunCommand.Timeout.Duration != 0 {
		t.Errorf("run_command.timeout = %v, want 0 (use global)", cfg.Tools.RunCommand.Timeout)
	}
	if cfg.Tools.Timeout.Duration != DefaultToolTimeout {
		t.Errorf("global timeout = %v, want %v", cfg.Tools.Timeout, DefaultToolTimeout)
	}
	if !strings.Contains(strings.Join(cfg.Tools.Enabled, ","), "run_command") {
		t.Errorf("whitelist = %v, want run_command present", cfg.Tools.Enabled)
	}
}
