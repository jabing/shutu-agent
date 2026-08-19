// Package config loads the runtime configuration from config.yaml (design.md
// §2). API keys are never part of configuration: they only ever come from the
// environment (design.md §6, Agent.md §5.6), so this file never contains them.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	// M4a kb defaults (dispatch-m4a §3): the knowledge base is off by default
	// (D10); a bounded Search/Recall returns 5 hits by default. The database
	// path defaults to <data_dir>/kb/knowledge.sqlite (resolved in
	// applyDefaults so it follows a custom data_dir).
	DefaultKBTopK = 5

	// M4b kb defaults (dispatch-m4b §5): proactive recall injects 3 hits per
	// round by default (0 disables proactive recall); the lightweight catalog
	// is injected into the system prompt by default. These are accessed through
	// KBConfig.RecallLimitValue / KBConfig.CatalogValue, which apply the
	// defaults when the YAML field is absent.
	DefaultKBRecallLimit = 3

	// M5a jobs defaults (dispatch-m5a-2 §3): the per-owner active-job cap is
	// 10 when jobs.max_concurrent_jobs_per_owner is absent or non-positive
	// (mirrors dsh jobs-local and internal/jobs' own default).
	DefaultMaxConcurrentJobsPerOwner = 10
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
	KB         KBConfig    `yaml:"kb"`          // knowledge-base policy (M4a kernel)
	Jobs       JobsConfig  `yaml:"jobs"`        // background-job policy (M5a)
}

// JobsConfig is the background-job policy (dispatch-m5a-2 §3 / ADR
// 2026-08-18-m5-agent-core.md 决策 ①). Jobs are off by default (D10): when
// disabled the composition root neither initializes a registry nor registers
// or whitelists the job_* tools.
type JobsConfig struct {
	// Enabled gates the whole capability: when false, no registry is created
	// (jobs.NewLocal is never called) and the job_* tools are neither
	// registered nor whitelisted (D10).
	Enabled bool `yaml:"enabled"`
	// MaxConcurrentJobsPerOwner caps the running+stopping jobs in one owner
	// bucket (and the shared unowned bucket); <= 0 means the default 10.
	MaxConcurrentJobsPerOwner int `yaml:"max_concurrent_jobs_per_owner"`
}

// KBConfig is the knowledge-base policy (dispatch-m4a §3 / dispatch-m4b §5).
// The knowledge base is off by default (D10). RecallLimit and Catalog are
// pointers so an absent YAML field (→ the default) is distinguishable from an
// explicit 0/false, which carries real meaning here: recall_limit 0 disables
// proactive recall and catalog false suppresses the system-prompt catalog.
// Read them through RecallLimitValue / CatalogValue, never directly.
type KBConfig struct {
	// Enabled gates the whole capability: when false, no KB provider is
	// initialized (kb.OpenSQLite is never called) and the kb_* tools are
	// neither registered nor whitelisted (D10).
	Enabled bool `yaml:"enabled"`
	// DBPath is the SQLite database file; "" defaults to
	// <data_dir>/kb/knowledge.sqlite.
	DBPath string `yaml:"db_path"`
	// TopK is the default result count for a bounded Search/Recall (<=0 uses
	// the default 5).
	TopK int `yaml:"top_k"`
	// RecallLimit is the per-round proactive recall count (dispatch-m4b §5):
	// 0 disables proactive recall, nil (absent) means the default 3.
	RecallLimit *int `yaml:"recall_limit"`
	// Catalog toggles injecting the lightweight KB catalog (name/description,
	// no bodies) into the system prompt; nil (absent) means true.
	Catalog *bool `yaml:"catalog"`
	// Extraction toggles the post-answer extraction writeback (dispatch-m4c
	// §3): when false, the composition root never invokes kb.Extract. nil
	// (absent) means true — within an enabled kb the extraction defaults on,
	// matching the config.yaml documentation.
	Extraction *bool `yaml:"extraction"`
}

// RecallLimitValue returns the effective per-round proactive recall count:
// the configured value, 0 when explicitly disabled, or DefaultKBRecallLimit
// when absent.
func (k KBConfig) RecallLimitValue() int {
	if k.RecallLimit == nil || *k.RecallLimit < 0 {
		return DefaultKBRecallLimit
	}
	return *k.RecallLimit
}

// CatalogValue returns whether the lightweight KB catalog is injected into
// the system prompt (true by default).
func (k KBConfig) CatalogValue() bool {
	if k.Catalog == nil {
		return true
	}
	return *k.Catalog
}

// ExtractionValue returns whether the post-answer extraction writeback runs
// (true by default; false skips extraction even when kb is enabled).
func (k KBConfig) ExtractionValue() bool {
	if k.Extraction == nil {
		return true
	}
	return *k.Extraction
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
	// Enabling kb whitelists its three consumer tools as well, so the single
	// kb.enabled switch turns the whole capability (provider + tools + recall)
	// on; default off (D10, dispatch-m4b §1 — mirrors run_command).
	if cfg.KB.Enabled {
		for _, name := range kbToolNames {
			if !contains(cfg.Tools.Enabled, name) {
				cfg.Tools.Enabled = append(cfg.Tools.Enabled, name)
			}
		}
	}
	// M4a kb defaults: off by default; the database path follows data_dir; a
	// bounded search returns 5 hits. An explicitly-set db_path is used
	// verbatim (it may point anywhere, e.g. an absolute path).
	if cfg.KB.DBPath == "" {
		cfg.KB.DBPath = filepath.Join(cfg.DataDir, "kb", "knowledge.sqlite")
	}
	if cfg.KB.TopK <= 0 {
		cfg.KB.TopK = DefaultKBTopK
	}
	// Enabling jobs whitelists its five consumer tools as well, so the single
	// jobs.enabled switch turns the whole capability (registry + tools + event
	// logging) on; default off (D10, dispatch-m5a-2 §3 — mirrors kb).
	if cfg.Jobs.Enabled {
		for _, name := range jobsToolNames {
			if !contains(cfg.Tools.Enabled, name) {
				cfg.Tools.Enabled = append(cfg.Tools.Enabled, name)
			}
		}
	}
	// M5a jobs defaults: off by default; the per-owner active-job cap is 10.
	if cfg.Jobs.MaxConcurrentJobsPerOwner <= 0 {
		cfg.Jobs.MaxConcurrentJobsPerOwner = DefaultMaxConcurrentJobsPerOwner
	}
}

// kbToolNames are the knowledge-base consumer tools (design.md §8 Consumer /
// dispatch-m4b §1). They are registered and whitelisted only when kb is
// enabled; keeping the names here makes the "kb.enabled ⇒ 工具自动白名单" rule a
// single, tested fact shared by applyDefaults and the composition root.
var kbToolNames = []string{"kb_search", "kb_read", "kb_add"}

// jobsToolNames are the background-job consumer tools (dispatch-m5a-2 §2).
// They are registered and whitelisted only when jobs is enabled; keeping the
// names here makes the "jobs.enabled ⇒ 工具自动白名单" rule a single, tested fact
// shared by applyDefaults and the composition root.
var jobsToolNames = []string{"job_start", "job_status", "job_cancel", "job_wait", "job_read"}

func contains(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
