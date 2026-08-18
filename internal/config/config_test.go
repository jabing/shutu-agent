package config

import (
	"os"
	"path/filepath"
	"testing"
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
