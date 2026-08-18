// Package config loads the runtime configuration from config.yaml (design.md
// §2). API keys are never part of configuration: they only ever come from the
// environment (design.md §6, Agent.md §5.6), so this file never contains them.
package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Defaults applied to fields that are empty or absent in config.yaml.
const (
	DefaultModel      = "deepseek-chat"
	DefaultBaseURL    = "" // empty => provider default (https://api.deepseek.com)
	DefaultDataDir    = "data"
	DefaultPromptsDir = "config/prompts"
)

// Config is the file-backed runtime configuration. Any field may be omitted in
// config.yaml; Load fills defaults for empty values, so callers never branch
// on field presence.
type Config struct {
	Model      string `yaml:"model"`       // chat model; default deepseek-chat
	BaseURL    string `yaml:"base_url"`    // optional OpenAI-compatible base URL; empty means the provider default
	DataDir    string `yaml:"data_dir"`    // directory for pa.db (and runtime data); default "data"
	PromptsDir string `yaml:"prompts_dir"` // directory of prompt section files; default "config/prompts"
}

// Load reads configuration from path. A missing file is not an error: the
// returned Config holds the defaults. A present-but-invalid file is an error.
func Load(path string) (Config, error) {
	cfg := Config{
		Model:      DefaultModel,
		BaseURL:    DefaultBaseURL,
		DataDir:    DefaultDataDir,
		PromptsDir: DefaultPromptsDir,
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir
	}
	if cfg.PromptsDir == "" {
		cfg.PromptsDir = DefaultPromptsDir
	}
	// BaseURL intentionally keeps an empty value to mean "provider default".
	return cfg, nil
}
