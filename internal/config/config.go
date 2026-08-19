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

	// M5b subagent defaults (dispatch-m5b-2 §3): the delegation depth cap is 8
	// when subagent.max_depth is absent or non-positive, and the default
	// provider is "spawn" (the only provider shipped in M5b) when
	// subagent.default_provider is empty.
	DefaultSubagentMaxDepth = 8
	DefaultSubagentProvider = "spawn"

	// M5c compaction defaults (dispatch-m5c-2a §2 / dispatch-m5c-2 §2): the
	// token-pressure threshold is 32000 when compaction.token_threshold is
	// absent or non-positive, and the retained tail is 8 turns when
	// compaction.retain_turns is absent or non-positive. max_chars defaults to
	// 0, meaning the engine default (the wiring passes BasicEngine's default,
	// or 0 for the engine to fall back on).
	DefaultCompactionTokenThreshold = 32000
	DefaultCompactionRetainTurns    = 8

	// M5d-2 skill defaults (dispatch-m5d-2 §2): the injected catalog is
	// bounded to 500 chars when skill.catalog_max_chars is absent or
	// non-positive, and skill_load returns at most 8000 chars of a skill body
	// when skill.body_max_chars is absent or non-positive (dispatch-m5d 约束:
	// 正文有长度上限防超长注入).
	DefaultSkillCatalogMaxChars = 500
	DefaultSkillBodyMaxChars    = 8000

	// M6a-2 schedule defaults (dispatch-m6a-2 §2): the serial pre-step clock
	// advances at the configured cadence; tick_interval defaults to 1m when
	// absent or non-positive. M6a-2 deliberately has no background ticker (D5)
	// — the loop's per-turn "schedule" pre-step injector calls Engine.Tick on
	// the serial path, and tick_interval is the documented cadence knob for
	// that advancement (reserved for a future gated advance; the value is
	// parsed and defaulted here regardless).
	DefaultScheduleTickInterval = time.Minute

	// M6e-2 code-sandbox defaults (dispatch-m6e-2 §2): the sandbox execution
	// deadline is 30s when code.timeout is absent or non-positive (mirrors the
	// local provider's own default), the per-stream output cap is 65536 bytes
	// when code.max_output is absent or non-positive (64KiB, the same bound as
	// the provider default and tools.output_limit), and sandbox_dir stays empty
	// meaning the provider default (<project>/.sandbox).
	DefaultCodeTimeout   = 30 * time.Second
	DefaultCodeMaxOutput = 64 * 1024
)

// defaultEnabledTools is the whitelist applied when tools.enabled is absent.
// It intentionally contains only the read-only tools (D10: 白名单先行).
var defaultEnabledTools = []string{"get_time", "read_file"}

// Config is the file-backed runtime configuration. Any field may be omitted in
// config.yaml; Load fills defaults for empty values, so callers never branch
// on field presence.
type Config struct {
	Model      string           `yaml:"model"`       // chat model; default deepseek-chat
	BaseURL    string           `yaml:"base_url"`    // optional OpenAI-compatible base URL; empty means the provider default
	DataDir    string           `yaml:"data_dir"`    // directory for pa.db (and runtime data); default "data"
	PromptsDir string           `yaml:"prompts_dir"` // directory of prompt section files; default "config/prompts"
	Tools      ToolsConfig      `yaml:"tools"`       // tool-execution policy (M3)
	KB         KBConfig         `yaml:"kb"`          // knowledge-base policy (M4a kernel)
	Jobs       JobsConfig       `yaml:"jobs"`        // background-job policy (M5a)
	Subagent   SubagentConfig   `yaml:"subagent"`    // subagent policy (M5b)
	Compaction CompactionConfig `yaml:"compaction"`  // context-compaction policy (M5c)
	Skill      SkillConfig      `yaml:"skill"`       // skill policy (M5d)
	Schedule   ScheduleConfig   `yaml:"schedule"`    // schedule policy (M6a)
	Plan       PlanConfig       `yaml:"plan"`        // task-planning policy (M6b)
	Spill      SpillConfig      `yaml:"spill"`       // long-term-memory policy (M6c)
	Interact   InteractConfig   `yaml:"interact"`    // human-approval policy (M6d)
	Code       CodeConfig       `yaml:"code"`        // code-sandbox policy (M6e)
	Mcp        McpConfig        `yaml:"mcp"`         // MCP tool-ecosystem policy (M6f)
	Fs         FsConfig         `yaml:"fs"`          // safe-file-operation policy (M6f)
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

// SubagentConfig is the subagent policy (dispatch-m5b-2 §3 / ADR
// 2026-08-18-m5-agent-core.md 决策 ②). Subagents are off by default (D10): when
// disabled the composition root neither initializes a runtime nor registers or
// whitelists the subagent_* tools.
type SubagentConfig struct {
	// Enabled gates the whole capability: when false, no Runtime/SpawnProvider
	// is created and the subagent_* tools are neither registered nor
	// whitelisted (D10).
	Enabled bool `yaml:"enabled"`
	// MaxDepth is the default delegation depth cap applied by subagent_spawn
	// when the model omits max_depth; <= 0 means the default 8.
	MaxDepth int `yaml:"max_depth"`
	// DefaultProvider is the provider subagent_spawn delegates to; empty means
	// the default "spawn" (the only provider shipped in M5b, so the tool
	// resolves to it regardless).
	DefaultProvider string `yaml:"default_provider"`
}

// CompactionConfig is the context-compaction policy (dispatch-m5c-2a §2 /
// dispatch-m5c-2 §2 / ADR 2026-08-18-m5-agent-core.md 决策 ③). Compaction is
// off by default (D10): when disabled the composition root neither registers
// the automatic pre-step trigger nor the /compact command. Unlike kb/jobs/
// subagent, enabling compaction whitelists no tools — compaction has no
// consumer tools (automatic triggering runs through the loop pre-step
// injector, manual through the /compact command, dispatch-m5c-2 §2).
type CompactionConfig struct {
	// Enabled gates the whole capability: when false, no compaction engine is
	// wired into the loop's PreStep and the /compact command reports the
	// capability as unavailable (D10).
	Enabled bool `yaml:"enabled"`
	// TokenThreshold is the surface-token pressure threshold above which a
	// step auto-compacts; <= 0 means the default 32000.
	TokenThreshold int `yaml:"token_threshold"`
	// RetainTurns is the tail of recent turns the basic provider keeps
	// unshadowed; <= 0 means the default 8.
	RetainTurns int `yaml:"retain_turns"`
	// MaxChars bounds the generated summary; <= 0 means the engine default
	// (the wiring passes BasicEngine's default, or 0 for the engine to fall
	// back on).
	MaxChars int `yaml:"max_chars"`
}

// SkillConfig is the skill policy (dispatch-m5d-2 §2 / ADR
// 2026-08-18-m5-agent-core.md 决策 ④). Skills are off by default (D10): when
// disabled the composition root neither creates a provider/registry nor
// registers or whitelists the skill_load tool, and no catalog injector is
// registered.
type SkillConfig struct {
	// Enabled gates the whole capability: when false, no skill provider/
	// registry is created and skill_load is neither registered nor whitelisted,
	// and no skill catalog pre-step injector is wired (D10).
	Enabled bool `yaml:"enabled"`
	// Dirs are additional custom skill directories (source "custom", rank 300)
	// scanned by the filesystem provider, in order. Empty by default.
	Dirs []string `yaml:"dirs"`
	// CatalogMaxChars bounds the injected skill catalog (sorted name +
	// description) in chars; <= 0 means the default 500.
	CatalogMaxChars int `yaml:"catalog_max_chars"`
	// BodyMaxChars bounds the skill body skill_load returns to the model in
	// chars (Unicode-safe truncation, 防超长注入); <= 0 means the default 8000.
	BodyMaxChars int `yaml:"body_max_chars"`
}

// ScheduleConfig is the schedule policy (dispatch-m6a-2 §2 / ADR
// 2026-08-19-m6-agent-full.md 决策 M6a). Schedules are off by default (D10):
// when disabled the composition root neither creates an Engine nor registers
// or whitelists the schedule_* tools, and no "schedule" pre-step injector is
// wired.
type ScheduleConfig struct {
	// Enabled gates the whole capability: when false, no Provider/Engine is
	// created and the schedule_* tools are neither registered nor whitelisted,
	// and no schedule pre-step injector is wired (D10).
	Enabled bool `yaml:"enabled"`
	// TickInterval is the cadence of the serial schedule-clock advancement
	// (per-turn pre-step Engine.Tick). There is no background ticker in M6a-2
	// (D5); the value is parsed and defaulted here so a future gated advance
	// can consume it. <= 0 means the default 1m.
	TickInterval Duration `yaml:"tick_interval"`
}

// PlanConfig is the task-planning policy (dispatch-m6b-2 §2 / ADR
// 2026-08-19-m6-agent-full.md 决策 M6b). Planning is off by default (D10): when
// disabled the composition root neither creates an Engine nor registers or
// whitelists the plan_* tools.
type PlanConfig struct {
	// Enabled gates the whole capability: when false, no Provider/Engine is
	// created and the plan_* tools are neither registered nor whitelisted
	// (D10).
	Enabled bool `yaml:"enabled"`
}

// SpillConfig is the long-term-memory policy (dispatch-m6c-2 §2 / ADR
// 2026-08-19-m6-agent-full.md 决策 M6c). Spill is off by default (D10): when
// disabled the composition root neither creates a Provider/Engine nor
// registers or whitelists the spill_* tools, and no auto-sedimentation path is
// wired. AutoSpill is a pointer so an absent YAML field (→ the default true) is
// distinguishable from an explicit false, which carries real meaning here:
// auto_spill: false keeps the spill_* tools usable while turning the automatic
// end-of-turn sedimentation off. Read it through AutoSpillValue, never
// directly.
type SpillConfig struct {
	// Enabled gates the whole capability: when false, no Provider/Engine is
	// created, the spill_* tools are neither registered nor whitelisted, and
	// no auto-sedimentation path is wired (D10).
	Enabled bool `yaml:"enabled"`
	// AutoSpill toggles the end-of-turn auto-sedimentation writeback
	// (Engine.AutoSpill over the session event log): nil (absent) means true —
	// within an enabled spill the auto-sedimentation defaults on, matching the
	// config.yaml documentation. It only takes effect when Enabled is true.
	AutoSpill *bool `yaml:"auto_spill"`
}

// AutoSpillValue returns whether the end-of-turn auto-sedimentation runs
// (true by default within an enabled spill; false explicitly disables it).
func (s SpillConfig) AutoSpillValue() bool {
	if s.AutoSpill == nil {
		return true
	}
	return *s.AutoSpill
}

// InteractConfig is the human-approval policy (dispatch-m6d-2 §2 / ADR
// 2026-08-19-m6-agent-full.md 决策 M6d). Interact is off by default (D10): when
// disabled the composition root neither creates an Engine nor registers or
// whitelists the interact_* tools, and no sensitive-tool gate is installed.
type InteractConfig struct {
	// Enabled gates the whole capability: when false, no Provider/Engine is
	// created, the interact_* tools are neither registered nor whitelisted,
	// and no sensitive-tool gate is installed (D10).
	Enabled bool `yaml:"enabled"`
	// SensitiveTools names the tools whose execution must first pass a human
	// approval (the ADR 决策 M6d sensitive-tool gate: approved before the tool
	// runs, rejected returns a denial to the model). Empty means no gating —
	// an enabled interact still registers the interact_* tools but intercepts
	// nothing.
	SensitiveTools []string `yaml:"sensitive_tools"`
}

// CodeConfig is the code-sandbox policy (dispatch-m6e-2 §2 / ADR
// 2026-08-19-m6-agent-full.md 决策 M6e). The code sandbox is off by default
// (D10): when disabled the composition root neither creates a local Provider /
// Engine nor registers or whitelists the code_run tool. It is controlled
// isolation, not strong isolation (process boundary + timeout + output quota +
// default no network; Windows has no network namespace — see the internal/code
// package comment for the exact boundary).
type CodeConfig struct {
	// Enabled gates the whole capability: when false, no local Provider/Engine
	// is created and code_run is neither registered nor whitelisted (D10).
	Enabled bool `yaml:"enabled"`
	// Timeout is the sandbox execution deadline code_run applies when the model
	// omits the per-call timeout (and the outer per-tool deadline bound for
	// code_run, mirroring tools.run_command.timeout); <= 0 means the default 30s.
	Timeout Duration `yaml:"timeout"`
	// MaxOutput is the per-stream output cap of a sandbox run (the model cannot
	// override it); <= 0 means the default 65536 bytes.
	MaxOutput int `yaml:"max_output"`
	// SandboxDir is the sandbox working directory used when the model omits
	// cwd. Empty means the provider default (<project>/.sandbox).
	SandboxDir string `yaml:"sandbox_dir"`
	// AllowNetwork is a declarative network toggle: false (the default) means
	// the sandbox injects no network credentials — the v1 local provider always
	// scrubs credential-shaped environment entries regardless of this flag. It
	// is a recorded boundary, not strong isolation: denying network access at
	// the OS level is out of scope on Windows (no network namespace).
	AllowNetwork bool `yaml:"allow_network"`
}

// McpConfig is the MCP tool-ecosystem policy (dispatch-m6f-2 §2 / ADR
// 2026-08-19-m6-agent-full.md 决策 M6f). MCP is off by default (D10): when
// disabled the composition root neither creates a Factory nor registers or
// whitelists the mcp_* tools, and no server is bridged. When enabled, mcp_list
// lists a configured server's tools and mcp_call invokes one by name (each a
// fresh stdio client per call, D5), and every tool each configured server
// advertises is bridged into the tool registry as mcp.<server>.<tool> with its
// input schema passed through, calling back into the server via tools/call.
type McpConfig struct {
	// Enabled gates the whole capability: when false, no mcp Factory is
	// created, the mcp_* tools are neither registered nor whitelisted, and no
	// server is bridged (D10).
	Enabled bool `yaml:"enabled"`
	// Servers are the configured MCP servers (stdio, newline-delimited
	// JSON-RPC). Each server's tools are bridged at startup with the
	// mcp.<server>.<tool> prefix.
	Servers []McpServer `yaml:"servers"`
}

// McpServer is one configured MCP server: a unique Name (used as the
// mcp_list/mcp_call selector and the mcp.<server>.<tool> bridge prefix) and a
// stdio command line (Cmd plus Args) the Factory spawns.
type McpServer struct {
	Name string   `yaml:"name"`
	Cmd  string   `yaml:"cmd"`
	Args []string `yaml:"args"`
}

// FsConfig is the safe-file-operation policy (dispatch-m6f-3 §3 / ADR
// 2026-08-19-m6-agent-full.md 决策 M6f). The fs capability is off by default
// (D10): when disabled the composition root neither creates a FileService nor
// registers or whitelists the fs_* tools. When enabled, every fs_* operation
// is constrained to the allowed root.
type FsConfig struct {
	// Enabled gates the whole capability: when false, no FileService is
	// created and the fs_* tools are neither registered nor whitelisted (D10).
	Enabled bool `yaml:"enabled"`
	// Root is the allowed root every fs_* path must stay inside. Empty means
	// the default <project> (the process working directory), resolved by the
	// FileService constructor.
	Root string `yaml:"root"`
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
	// Enabling subagent whitelists its four consumer tools as well, so the
	// single subagent.enabled switch turns the whole capability (runtime +
	// provider + tools + event logging) on; default off (D10, dispatch-m5b-2
	// §3 — mirrors kb/jobs).
	if cfg.Subagent.Enabled {
		for _, name := range subagentToolNames {
			if !contains(cfg.Tools.Enabled, name) {
				cfg.Tools.Enabled = append(cfg.Tools.Enabled, name)
			}
		}
	}
	// M5b subagent defaults: off by default; the delegation depth cap is 8;
	// the default provider is "spawn".
	if cfg.Subagent.MaxDepth <= 0 {
		cfg.Subagent.MaxDepth = DefaultSubagentMaxDepth
	}
	if cfg.Subagent.DefaultProvider == "" {
		cfg.Subagent.DefaultProvider = DefaultSubagentProvider
	}
	// M5c compaction defaults: off by default (D10); the token-pressure
	// threshold is 32000; the retained tail is 8 turns; max_chars 0 means the
	// engine default. Compaction deliberately whitelists no tools — it has none
	// (automatic triggering runs through the loop pre-step injector, manual
	// through the /compact command, dispatch-m5c-2a §2). Non-positive
	// thresholds/retain are clamped to the defaults (校验非负: a negative
	// configured value can never survive to the wiring).
	if cfg.Compaction.TokenThreshold <= 0 {
		cfg.Compaction.TokenThreshold = DefaultCompactionTokenThreshold
	}
	if cfg.Compaction.RetainTurns <= 0 {
		cfg.Compaction.RetainTurns = DefaultCompactionRetainTurns
	}
	// M5d-2 skill defaults: off by default (D10); the catalog is bounded to
	// 500 chars and the returned skill body to 8000 chars. Enabling skill
	// whitelists its single consumer tool skill_load, so the one
	// skill.enabled switch turns the whole capability (provider + registry +
	// tool + catalog injector) on (mirrors kb/jobs/subagent). Non-positive
	// bounds are clamped to the defaults (校验非负: a negative configured value
	// can never survive to the wiring).
	if cfg.Skill.Enabled {
		for _, name := range skillToolNames {
			if !contains(cfg.Tools.Enabled, name) {
				cfg.Tools.Enabled = append(cfg.Tools.Enabled, name)
			}
		}
	}
	if cfg.Skill.CatalogMaxChars <= 0 {
		cfg.Skill.CatalogMaxChars = DefaultSkillCatalogMaxChars
	}
	if cfg.Skill.BodyMaxChars <= 0 {
		cfg.Skill.BodyMaxChars = DefaultSkillBodyMaxChars
	}
	// M6a-2 schedule defaults: off by default (D10); the serial clock cadence
	// is 1m. Enabling schedule whitelists its three consumer tools, so the one
	// schedule.enabled switch turns the whole capability (Provider + Engine +
	// tools + pre-step trigger + fire event/job wiring) on (mirrors
	// kb/jobs/subagent/skill). Non-positive cadence is clamped to the default
	// (校验非负: a negative configured value can never survive to the wiring).
	if cfg.Schedule.Enabled {
		for _, name := range scheduleToolNames {
			if !contains(cfg.Tools.Enabled, name) {
				cfg.Tools.Enabled = append(cfg.Tools.Enabled, name)
			}
		}
	}
	if cfg.Schedule.TickInterval.Duration <= 0 {
		cfg.Schedule.TickInterval.Duration = DefaultScheduleTickInterval
	}
	// M6b-2 plan defaults: off by default (D10). Enabling plan whitelists its
	// six consumer tools, so the one plan.enabled switch turns the whole
	// capability (Provider + Engine + tools + event logging) on (mirrors
	// kb/jobs/subagent/skill/schedule).
	if cfg.Plan.Enabled {
		for _, name := range planToolNames {
			if !contains(cfg.Tools.Enabled, name) {
				cfg.Tools.Enabled = append(cfg.Tools.Enabled, name)
			}
		}
	}
	// M6c-2 spill defaults: off by default (D10); auto_spill defaults on
	// within an enabled spill (AutoSpillValue, mirroring kb extraction).
	// Enabling spill whitelists its four consumer tools, so the one
	// spill.enabled switch turns the whole capability (Provider + Engine +
	// tools + event logging + auto-sedimentation) on (mirrors
	// kb/jobs/subagent/skill/schedule/plan).
	if cfg.Spill.Enabled {
		for _, name := range spillToolNames {
			if !contains(cfg.Tools.Enabled, name) {
				cfg.Tools.Enabled = append(cfg.Tools.Enabled, name)
			}
		}
	}
	// M6d-2 interact defaults: off by default (D10). Enabling interact
	// whitelists its two consumer tools, so the one interact.enabled switch
	// turns the whole capability (Provider + Engine + tools + event logging +
	// the sensitive-tool gate) on (mirrors kb/jobs/subagent/skill/schedule/
	// plan/spill). sensitive_tools is left verbatim: empty means the gate is
	// not installed even when enabled (no gating by default).
	if cfg.Interact.Enabled {
		for _, name := range interactToolNames {
			if !contains(cfg.Tools.Enabled, name) {
				cfg.Tools.Enabled = append(cfg.Tools.Enabled, name)
			}
		}
	}
	// M6e-2 code defaults: off by default (D10); the sandbox timeout is 30s,
	// the per-stream output cap 65536 bytes, and sandbox_dir empty (the
	// provider default <project>/.sandbox). Enabling code whitelists its single
	// consumer tool code_run, so the one code.enabled switch turns the whole
	// capability (Provider + Engine + tool + event logging) on (mirrors
	// kb/jobs/subagent/skill/schedule/plan/spill/interact). Non-positive bounds
	// are clamped to the defaults (校验非负: a negative configured value can
	// never survive to the wiring). allow_network stays verbatim: false by
	// default (declarative no-network boundary).
	if cfg.Code.Enabled {
		for _, name := range codeToolNames {
			if !contains(cfg.Tools.Enabled, name) {
				cfg.Tools.Enabled = append(cfg.Tools.Enabled, name)
			}
		}
	}
	if cfg.Code.Timeout.Duration <= 0 {
		cfg.Code.Timeout.Duration = DefaultCodeTimeout
	}
	if cfg.Code.MaxOutput <= 0 {
		cfg.Code.MaxOutput = DefaultCodeMaxOutput
	}
	// M6f-2 mcp defaults: off by default (D10). Enabling mcp whitelists its two
	// consumer tools mcp_list and mcp_call, so the one mcp.enabled switch turns
	// the whole capability (Factory + mcp_* tools + server bridging + event
	// logging) on (mirrors kb/jobs/subagent/skill/schedule/plan/spill/interact/
	// code). Bridged server tools (mcp.<server>.<tool>) cannot be whitelisted
	// here — their names are only known at runtime — so the composition root
	// whitelists each one as it is registered.
	if cfg.Mcp.Enabled {
		for _, name := range mcpToolNames {
			if !contains(cfg.Tools.Enabled, name) {
				cfg.Tools.Enabled = append(cfg.Tools.Enabled, name)
			}
		}
	}
	// M6f-3 fs defaults: off by default (D10); root empty means the default
	// <project> (the process working directory), resolved by the FileService
	// constructor — there is nothing to default here. Enabling fs whitelists
	// its three consumer tools, so the one fs.enabled switch turns the whole
	// capability (FileService + fs_* tools + event logging) on (mirrors
	// kb/jobs/subagent/skill/schedule/plan/spill/interact/code/mcp).
	if cfg.Fs.Enabled {
		for _, name := range fsToolNames {
			if !contains(cfg.Tools.Enabled, name) {
				cfg.Tools.Enabled = append(cfg.Tools.Enabled, name)
			}
		}
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

// subagentToolNames are the subagent consumer tools (dispatch-m5b-2 §2). They
// are registered and whitelisted only when subagent is enabled; keeping the
// names here makes the "subagent.enabled ⇒ 工具自动白名单" rule a single, tested
// fact shared by applyDefaults and the composition root.
var subagentToolNames = []string{"subagent_spawn", "subagent_status", "subagent_cancel", "subagent_list"}

// skillToolNames are the skill consumer tools (dispatch-m5d-2 §2). skill_load
// is registered and whitelisted only when skill is enabled; keeping the name
// here makes the "skill.enabled ⇒ 工具自动白名单" rule a single, tested fact
// shared by applyDefaults and the composition root.
var skillToolNames = []string{"skill_load"}

// scheduleToolNames are the schedule consumer tools (dispatch-m6a-2 §3). They
// are registered and whitelisted only when schedule is enabled; keeping the
// names here makes the "schedule.enabled ⇒ 工具自动白名单" rule a single, tested
// fact shared by applyDefaults and the composition root.
var scheduleToolNames = []string{"schedule_create", "schedule_list", "schedule_delete"}

// planToolNames are the plan consumer tools (dispatch-m6b-2 §3). They are
// registered and whitelisted only when plan is enabled; keeping the names here
// makes the "plan.enabled ⇒ 工具自动白名单" rule a single, tested fact shared by
// applyDefaults and the composition root.
var planToolNames = []string{"plan_goal", "plan_plan", "plan_todo", "plan_status", "plan_list", "plan_remove"}

// spillToolNames are the spill consumer tools (dispatch-m6c-2 §3). They are
// registered and whitelisted only when spill is enabled; keeping the names here
// makes the "spill.enabled ⇒ 工具自动白名单" rule a single, tested fact shared by
// applyDefaults and the composition root.
var spillToolNames = []string{"spill_write", "spill_recall", "spill_list", "spill_delete"}

// interactToolNames are the human-approval consumer tools (dispatch-m6d-2 §3).
// They are registered and whitelisted only when interact is enabled; keeping
// the names here makes the "interact.enabled ⇒ 工具自动白名单" rule a single,
// tested fact shared by applyDefaults and the composition root.
var interactToolNames = []string{"interact_ask", "interact_status"}

// codeToolNames are the code-sandbox consumer tools (dispatch-m6e-2 §2).
// code_run is registered and whitelisted only when code is enabled; keeping the
// name here makes the "code.enabled ⇒ 工具自动白名单" rule a single, tested fact
// shared by applyDefaults and the composition root.
var codeToolNames = []string{"code_run"}

// mcpToolNames are the MCP consumer tools (dispatch-m6f-2 §2). mcp_list and
// mcp_call are registered and whitelisted only when mcp is enabled; keeping the
// names here makes the "mcp.enabled ⇒ 工具自动白名单" rule a single, tested fact
// shared by applyDefaults and the composition root. Bridged server tools
// (mcp.<server>.<tool>) are dynamic and are whitelisted by the composition root
// as they are registered.
var mcpToolNames = []string{"mcp_list", "mcp_call"}

// fsToolNames are the safe-file-operation consumer tools (dispatch-m6f-3 §3).
// They are registered and whitelisted only when fs is enabled; keeping the
// names here makes the "fs.enabled ⇒ 工具自动白名单" rule a single, tested fact
// shared by applyDefaults and the composition root.
var fsToolNames = []string{"fs_read", "fs_write", "fs_list"}

func contains(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
