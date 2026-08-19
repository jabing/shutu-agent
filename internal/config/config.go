// Package config loads the runtime configuration from config.yaml (design.md
// §2). API keys are never part of configuration: they only ever come from the
// environment (design.md §6, Agent.md §5.6), so this file never contains them.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults applied to fields that are empty or absent in config.yaml.
const (
	DefaultModel      = "deepseek-chat"
	DefaultBaseURL    = "" // empty => provider default (https://api.deepseek.com)
	DefaultDataDir    = "data"
	DefaultPromptsDir = "config/prompts"

	// M3 tool-execution defaults (design.md §5 / dispatch-m3): the default
	// whitelist holds only the read-only tools; the per-tool execute deadline
	// is 30s; tool output over 64KB is truncated and spilled.
	DefaultToolTimeout = 30 * time.Second
	DefaultOutputLimit = 64 * 1024
)

// defaultEnabledTools is the whitelist applied when tools.enabled is absent.
// It intentionally contains only the read-only tools (D10: 白名单先行).
var defaultEnabledTools = []string{"get_time", "read_file"}

// Config is the file-backed runtime configuration. Any field may be omitted in
// config.yaml; Load fills defaults for empty values, so callers never branch
// on field presence.
type Config struct {
	Model      string      `yaml:"model"`       // chat model; default deepseek-chat
	BaseURL    string      `yaml:"base_url"`    // optional OpenAI-compatible base URL; empty means the provider default
	DataDir    string      `yaml:"data_dir"`    // directory for pa.db (and runtime data); default "data"
	PromptsDir string      `yaml:"prompts_dir"` // directory of prompt section files; default "config/prompts"
	Tools      ToolsConfig `yaml:"tools"`       // tool-execution policy (M3)
}

// ToolsConfig is the M3 tool-execution policy: the whitelist, the per-tool
// execute deadline, the output limit, and the optional run_command policy.
type ToolsConfig struct {
	// Enabled is the tool whitelist (design.md §5): only these names may
	// execute. Absent/empty defaults to the read-only pair.
	Enabled []string `yaml:"enabled"`
	// Timeout is the per-tool execute deadline (default 30s). Every Execute
	// is wrapped in context.WithTimeout.
	Timeout Duration `yaml:"timeout"`
	// OutputLimit caps the model-facing tool result in bytes (default 64KB).
	// Oversized output is truncated and the full text is spilled to
	// data/spill/<session>-<seq>.txt.
	OutputLimit int `yaml:"output_limit"`
	// RunCommand is the policy for the sole execution-class tool (default
	// disabled; design.md §5 / D10 落地).
	RunCommand RunCommandConfig `yaml:"run_command"`
}

// RunCommandConfig is the run_command tool policy. The tool is registered and
// usable only when Enabled is true (default off); its timeout may override the
// global tools.timeout; Workdir fixes the working directory of every command.
type RunCommandConfig struct {
	Enabled bool     `yaml:"enabled"`
	Timeout Duration `yaml:"timeout"` // 0/absent => use tools.timeout
	Workdir string   `yaml:"workdir"` // fixed cwd; empty => the agent's own cwd
}

// Duration unmarshals a YAML scalar like "30s" into a time.Duration. An empty
// or absent value yields the zero duration.
type Duration struct {
	time.Duration
}

// UnmarshalYAML parses a Go duration string ("30s", "1m", ...). An empty
// string is accepted as the zero duration.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("config: duration must be a string like \"30s\": %w", err)
	}
	if s == "" {
		d.Duration = 0
		return nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: invalid duration %q: %w", s, err)
	}
	d.Duration = dur
	return nil
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
			applyDefaults(&cfg)
			return cfg, nil
		}
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	applyDefaults(&cfg)
	// BaseURL intentionally keeps an empty value to mean "provider default".
	return cfg, nil
}

// applyDefaults fills every field that is empty or absent so callers never
// branch on field presence. It runs on both the missing-file and parsed paths.
func applyDefaults(cfg *Config) {
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir
	}
	if cfg.PromptsDir == "" {
		cfg.PromptsDir = DefaultPromptsDir
	}
	if len(cfg.Tools.Enabled) == 0 {
		cfg.Tools.Enabled = append([]string(nil), defaultEnabledTools...)
	}
	if cfg.Tools.Timeout.Duration <= 0 {
		cfg.Tools.Timeout.Duration = DefaultToolTimeout
	}
	if cfg.Tools.OutputLimit <= 0 {
		cfg.Tools.OutputLimit = DefaultOutputLimit
	}
	// Enabling run_command makes it whitelisted too, so the single
	// tools.run_command.enabled switch is what turns the execution tool on
	// (design.md §5 / D10).
	if cfg.Tools.RunCommand.Enabled && !contains(cfg.Tools.Enabled, "run_command") {
		cfg.Tools.Enabled = append(cfg.Tools.Enabled, "run_command")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
