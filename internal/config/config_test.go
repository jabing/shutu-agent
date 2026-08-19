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

// M4a: kb is off by default (D10), the database path defaults to
// <data_dir>/kb/knowledge.sqlite, and a bounded search returns 5 hits by
// default (dispatch-m4a §3). M4b: proactive recall defaults to 3 hits/round
// and the catalog defaults to injected (dispatch-m4b §5).
func TestLoadKBDefaultsWhenAbsent(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KB.Enabled {
		t.Error("kb must be disabled by default (D10)")
	}
	wantPath := filepath.Join(cfg.DataDir, "kb", "knowledge.sqlite")
	if cfg.KB.DBPath != wantPath {
		t.Errorf("kb.db_path = %q, want %q", cfg.KB.DBPath, wantPath)
	}
	if cfg.KB.TopK != DefaultKBTopK {
		t.Errorf("kb.top_k = %d, want %d", cfg.KB.TopK, DefaultKBTopK)
	}
	if got := cfg.KB.RecallLimitValue(); got != DefaultKBRecallLimit {
		t.Errorf("kb.recall_limit (absent) = %d, want %d", got, DefaultKBRecallLimit)
	}
	if !cfg.KB.CatalogValue() {
		t.Error("kb.catalog (absent) must default to true")
	}
}

func TestLoadParsesKBSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "kb:\n  enabled: true\n  db_path: /tmp/kb.sqlite\n  top_k: 3\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.KB.Enabled {
		t.Error("kb.enabled should be true")
	}
	if cfg.KB.DBPath != "/tmp/kb.sqlite" {
		t.Errorf("kb.db_path = %q, want /tmp/kb.sqlite", cfg.KB.DBPath)
	}
	if cfg.KB.TopK != 3 {
		t.Errorf("kb.top_k = %d, want 3", cfg.KB.TopK)
	}
}

// M4b: an explicit recall_limit / catalog is honored, and the defaults are
// used for the fields left absent in the same file.
func TestLoadParsesKBRecallAndCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "kb:\n  enabled: true\n  recall_limit: 5\n  catalog: false\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.KB.RecallLimitValue(); got != 5 {
		t.Errorf("kb.recall_limit = %d, want 5", got)
	}
	if cfg.KB.CatalogValue() {
		t.Error("kb.catalog = true, want false (explicitly disabled)")
	}
	if cfg.KB.TopK != DefaultKBTopK {
		t.Errorf("absent kb.top_k = %d, want default %d", cfg.KB.TopK, DefaultKBTopK)
	}
}

// M4b: recall_limit 0 means "proactive recall off" — it must survive the
// absent-vs-zero distinction (0 is meaningful, not the default).
func TestLoadRecallLimitZeroDisables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("kb:\n  enabled: true\n  recall_limit: 0\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.KB.RecallLimitValue(); got != 0 {
		t.Errorf("kb.recall_limit = %d, want 0 (proactive recall off)", got)
	}
}

// M4c: kb.extraction defaults to true when absent (dispatch-m4c §3 — within an
// enabled kb the extraction writeback is on by default, matching config.yaml),
// and an explicit false is honored.
func TestLoadKBExtractionDefaultsAndDisables(t *testing.T) {
	// Absent ⇒ true.
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.KB.ExtractionValue() {
		t.Error("kb.extraction (absent) must default to true")
	}

	// Explicit false ⇒ skipped.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("kb:\n  enabled: true\n  extraction: false\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg2, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.KB.ExtractionValue() {
		t.Error("kb.extraction = true, want false (explicitly disabled)")
	}

	// Explicit true is honored too.
	path2 := filepath.Join(t.TempDir(), "config2.yaml")
	if err := os.WriteFile(path2, []byte("kb:\n  enabled: true\n  extraction: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg3, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg3.KB.ExtractionValue() {
		t.Error("kb.extraction = false, want true (explicitly enabled)")
	}
}

// M4b: kb.enabled: true is the single switch that turns the whole capability
// on — the three kb_* tools must also become whitelisted (dispatch-m4b §1,
// mirrors run_command). When kb is disabled they must NOT be whitelisted.
func TestLoadKBEnabledAppendsToolsToWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("kb:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range kbToolNames {
		if !contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v lacks %q after kb.enabled", cfg.Tools.Enabled, name)
		}
	}

	// Default (kb disabled): no kb tool in the whitelist.
	cfg2, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range kbToolNames {
		if contains(cfg2.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when kb disabled", cfg2.Tools.Enabled, name)
		}
	}
}

// An explicit db_path is used verbatim; only the empty default follows
// data_dir.
func TestLoadExplicitKBDBPathIsVerbatim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "data_dir: custom\nkb:\n  enabled: true\n  db_path: data/kb/knowledge.sqlite\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KB.DBPath != "data/kb/knowledge.sqlite" {
		t.Errorf("kb.db_path = %q, want verbatim data/kb/knowledge.sqlite", cfg.KB.DBPath)
	}
}

// The empty db_path default follows a custom data_dir.
func TestLoadKBDBPathFollowsDataDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "data_dir: /srv/pa-data\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join("/srv/pa-data", "kb", "knowledge.sqlite")
	if cfg.KB.DBPath != want {
		t.Errorf("kb.db_path = %q, want %q", cfg.KB.DBPath, want)
	}
	if cfg.KB.Enabled {
		t.Error("kb must stay disabled by default (D10)")
	}
}

// M5a: jobs is off by default (D10), and the per-owner active-job cap defaults
// to 10 (dispatch-m5a-2 §3).
func TestLoadJobsDefaultsWhenAbsent(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Jobs.Enabled {
		t.Error("jobs must be disabled by default (D10)")
	}
	if cfg.Jobs.MaxConcurrentJobsPerOwner != DefaultMaxConcurrentJobsPerOwner {
		t.Errorf("jobs.max_concurrent_jobs_per_owner = %d, want default %d",
			cfg.Jobs.MaxConcurrentJobsPerOwner, DefaultMaxConcurrentJobsPerOwner)
	}
	// With jobs disabled no job tool may be whitelisted.
	for _, name := range jobsToolNames {
		if contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when jobs disabled", cfg.Tools.Enabled, name)
		}
	}
}

// M5a: jobs.enabled: true is the single switch that turns the whole capability
// on — the five job_* tools must also become whitelisted (dispatch-m5a-2 §3,
// mirrors kb). An absent max falls back to the default 10.
func TestLoadJobsEnabledAppendsToolsToWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("jobs:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range jobsToolNames {
		if !contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v lacks %q after jobs.enabled", cfg.Tools.Enabled, name)
		}
	}
	if cfg.Jobs.MaxConcurrentJobsPerOwner != DefaultMaxConcurrentJobsPerOwner {
		t.Errorf("absent jobs.max_concurrent_jobs_per_owner = %d, want default %d",
			cfg.Jobs.MaxConcurrentJobsPerOwner, DefaultMaxConcurrentJobsPerOwner)
	}
}

// M5a: an explicit max_concurrent_jobs_per_owner is honored, and a
// non-positive value falls back to the default 10 (dispatch-m5a-2 §3).
func TestLoadJobsMaxConcurrentExplicitAndDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("jobs:\n  enabled: true\n  max_concurrent_jobs_per_owner: 3\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Jobs.MaxConcurrentJobsPerOwner != 3 {
		t.Errorf("jobs.max_concurrent_jobs_per_owner = %d, want 3", cfg.Jobs.MaxConcurrentJobsPerOwner)
	}

	path2 := filepath.Join(t.TempDir(), "config2.yaml")
	if err := os.WriteFile(path2, []byte("jobs:\n  enabled: true\n  max_concurrent_jobs_per_owner: 0\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Jobs.MaxConcurrentJobsPerOwner != DefaultMaxConcurrentJobsPerOwner {
		t.Errorf("jobs.max_concurrent_jobs_per_owner 0 = %d, want default %d",
			cfg2.Jobs.MaxConcurrentJobsPerOwner, DefaultMaxConcurrentJobsPerOwner)
	}
}

// M5b: subagent is off by default (D10), the delegation depth cap defaults to
// 8, the default provider to "spawn", and no subagent tool is whitelisted
// while disabled (dispatch-m5b-2 §3).
func TestLoadSubagentDefaultsWhenAbsent(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Subagent.Enabled {
		t.Error("subagent must be disabled by default (D10)")
	}
	if cfg.Subagent.MaxDepth != DefaultSubagentMaxDepth {
		t.Errorf("subagent.max_depth = %d, want default %d", cfg.Subagent.MaxDepth, DefaultSubagentMaxDepth)
	}
	if cfg.Subagent.DefaultProvider != DefaultSubagentProvider {
		t.Errorf("subagent.default_provider = %q, want default %q", cfg.Subagent.DefaultProvider, DefaultSubagentProvider)
	}
	for _, name := range subagentToolNames {
		if contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when subagent disabled", cfg.Tools.Enabled, name)
		}
	}
}

// M5b: an explicit subagent section is honored (enabled, max_depth,
// default_provider), while a non-positive max_depth and an empty
// default_provider fall back to their defaults (dispatch-m5b-2 §3).
func TestLoadSubagentParsesSectionAndFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("subagent:\n  enabled: true\n  max_depth: 4\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Subagent.Enabled {
		t.Error("subagent.enabled should be true")
	}
	if cfg.Subagent.MaxDepth != 4 {
		t.Errorf("subagent.max_depth = %d, want 4", cfg.Subagent.MaxDepth)
	}
	if cfg.Subagent.DefaultProvider != DefaultSubagentProvider {
		t.Errorf("absent subagent.default_provider = %q, want default %q", cfg.Subagent.DefaultProvider, DefaultSubagentProvider)
	}

	path2 := filepath.Join(t.TempDir(), "config2.yaml")
	if err := os.WriteFile(path2, []byte("subagent:\n  enabled: true\n  max_depth: 0\n  default_provider: \"\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Subagent.MaxDepth != DefaultSubagentMaxDepth {
		t.Errorf("subagent.max_depth 0 = %d, want default %d", cfg2.Subagent.MaxDepth, DefaultSubagentMaxDepth)
	}
	if cfg2.Subagent.DefaultProvider != DefaultSubagentProvider {
		t.Errorf("subagent.default_provider empty = %q, want default %q", cfg2.Subagent.DefaultProvider, DefaultSubagentProvider)
	}
}

// M5b: subagent.enabled: true is the single switch that turns the whole
// capability on — the four subagent_* tools must also become whitelisted
// (dispatch-m5b-2 §3, mirrors jobs/kb).
func TestLoadSubagentEnabledAppendsToolsToWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("subagent:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range subagentToolNames {
		if !contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v lacks %q after subagent.enabled", cfg.Tools.Enabled, name)
		}
	}
}

// M5c: compaction is off by default (D10), the token-pressure threshold
// defaults to 32000, the retained tail to 8 turns, and max_chars to 0 (the
// engine default). Unlike kb/jobs/subagent, compaction has no consumer tools —
// nothing may be whitelisted for it even when enabled (dispatch-m5c-2a §2).
func TestLoadCompactionDefaultsWhenAbsent(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Compaction.Enabled {
		t.Error("compaction must be disabled by default (D10)")
	}
	if cfg.Compaction.TokenThreshold != DefaultCompactionTokenThreshold {
		t.Errorf("compaction.token_threshold = %d, want default %d",
			cfg.Compaction.TokenThreshold, DefaultCompactionTokenThreshold)
	}
	if cfg.Compaction.RetainTurns != DefaultCompactionRetainTurns {
		t.Errorf("compaction.retain_turns = %d, want default %d",
			cfg.Compaction.RetainTurns, DefaultCompactionRetainTurns)
	}
	if cfg.Compaction.MaxChars != 0 {
		t.Errorf("compaction.max_chars = %d, want 0 (engine default)", cfg.Compaction.MaxChars)
	}
	// The default whitelist must stay exactly the read-only pair: compaction
	// adds no tools.
	want := []string{"get_time", "read_file"}
	if !reflect.DeepEqual(cfg.Tools.Enabled, want) {
		t.Errorf("whitelist = %v, want %v (compaction adds nothing)", cfg.Tools.Enabled, want)
	}
}

// M5c: an explicit compaction section is honored (enabled, token_threshold,
// retain_turns, max_chars), while a non-positive token_threshold/retain_turns
// fall back to their defaults (校验非负: a negative value never survives) and
// max_chars stays 0 (engine default) (dispatch-m5c-2a §2).
func TestLoadCompactionParsesSectionAndFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("compaction:\n  enabled: true\n  token_threshold: 50000\n  retain_turns: 12\n  max_chars: 2000\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Compaction.Enabled {
		t.Error("compaction.enabled should be true")
	}
	if cfg.Compaction.TokenThreshold != 50000 {
		t.Errorf("compaction.token_threshold = %d, want 50000", cfg.Compaction.TokenThreshold)
	}
	if cfg.Compaction.RetainTurns != 12 {
		t.Errorf("compaction.retain_turns = %d, want 12", cfg.Compaction.RetainTurns)
	}
	if cfg.Compaction.MaxChars != 2000 {
		t.Errorf("compaction.max_chars = %d, want 2000", cfg.Compaction.MaxChars)
	}

	// Non-positive (including negative) thresholds fall back to defaults;
	// max_chars stays 0.
	path2 := filepath.Join(t.TempDir(), "config2.yaml")
	if err := os.WriteFile(path2, []byte("compaction:\n  enabled: true\n  token_threshold: 0\n  retain_turns: -3\n  max_chars: 0\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Compaction.TokenThreshold != DefaultCompactionTokenThreshold {
		t.Errorf("compaction.token_threshold 0 = %d, want default %d",
			cfg2.Compaction.TokenThreshold, DefaultCompactionTokenThreshold)
	}
	if cfg2.Compaction.RetainTurns != DefaultCompactionRetainTurns {
		t.Errorf("compaction.retain_turns -3 = %d, want default %d",
			cfg2.Compaction.RetainTurns, DefaultCompactionRetainTurns)
	}
	if cfg2.Compaction.MaxChars != 0 {
		t.Errorf("compaction.max_chars = %d, want 0 (engine default)", cfg2.Compaction.MaxChars)
	}
}

// M5c: compaction.enabled: true must NOT append any tool to the whitelist —
// compaction has no consumer tools (automatic triggering runs through the loop
// pre-step injector, manual through the /compact command, dispatch-m5c-2a §2).
func TestLoadCompactionEnabledDoesNotAppendToolsToWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("compaction:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Enabling compaction leaves the whitelist exactly at the read-only pair:
	// no compaction tool exists to add, and none is invented.
	want := []string{"get_time", "read_file"}
	if !reflect.DeepEqual(cfg.Tools.Enabled, want) {
		t.Errorf("whitelist = %v, want %v (compaction.enabled adds nothing)", cfg.Tools.Enabled, want)
	}
}

// M5d: skill is off by default (D10), dirs are empty, the catalog is bounded
// to 500 chars and the returned skill body to 8000 chars, and skill_load is
// not whitelisted while disabled (dispatch-m5d-2 §2).
func TestLoadSkillDefaultsWhenAbsent(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Skill.Enabled {
		t.Error("skill must be disabled by default (D10)")
	}
	if len(cfg.Skill.Dirs) != 0 {
		t.Errorf("skill.dirs = %v, want empty", cfg.Skill.Dirs)
	}
	if cfg.Skill.CatalogMaxChars != DefaultSkillCatalogMaxChars {
		t.Errorf("skill.catalog_max_chars = %d, want default %d",
			cfg.Skill.CatalogMaxChars, DefaultSkillCatalogMaxChars)
	}
	if cfg.Skill.BodyMaxChars != DefaultSkillBodyMaxChars {
		t.Errorf("skill.body_max_chars = %d, want default %d",
			cfg.Skill.BodyMaxChars, DefaultSkillBodyMaxChars)
	}
	for _, name := range skillToolNames {
		if contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when skill disabled", cfg.Tools.Enabled, name)
		}
	}
}

// M5d: an explicit skill section is honored (enabled, dirs, catalog_max_chars,
// body_max_chars), while non-positive bounds fall back to their defaults
// (校验非负: a negative value never survives) (dispatch-m5d-2 §2).
func TestLoadSkillParsesSectionAndFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("skill:\n  enabled: true\n  dirs: [C:\\skills, D:\\more]\n  catalog_max_chars: 300\n  body_max_chars: 4096\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Skill.Enabled {
		t.Error("skill.enabled should be true")
	}
	if len(cfg.Skill.Dirs) != 2 || cfg.Skill.Dirs[0] != `C:\skills` || cfg.Skill.Dirs[1] != `D:\more` {
		t.Errorf("skill.dirs = %v, want [C:\\skills D:\\more]", cfg.Skill.Dirs)
	}
	if cfg.Skill.CatalogMaxChars != 300 {
		t.Errorf("skill.catalog_max_chars = %d, want 300", cfg.Skill.CatalogMaxChars)
	}
	if cfg.Skill.BodyMaxChars != 4096 {
		t.Errorf("skill.body_max_chars = %d, want 4096", cfg.Skill.BodyMaxChars)
	}

	// Non-positive (including negative) bounds fall back to the defaults.
	path2 := filepath.Join(t.TempDir(), "config2.yaml")
	if err := os.WriteFile(path2, []byte("skill:\n  enabled: true\n  catalog_max_chars: 0\n  body_max_chars: -1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Skill.CatalogMaxChars != DefaultSkillCatalogMaxChars {
		t.Errorf("skill.catalog_max_chars 0 = %d, want default %d",
			cfg2.Skill.CatalogMaxChars, DefaultSkillCatalogMaxChars)
	}
	if cfg2.Skill.BodyMaxChars != DefaultSkillBodyMaxChars {
		t.Errorf("skill.body_max_chars -1 = %d, want default %d",
			cfg2.Skill.BodyMaxChars, DefaultSkillBodyMaxChars)
	}
}

// M5d: skill.enabled: true is the single switch that turns the whole capability
// on — the skill_load tool must also become whitelisted (dispatch-m5d-2 §2,
// mirrors kb/jobs/subagent).
func TestLoadSkillEnabledAppendsToolsToWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("skill:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range skillToolNames {
		if !contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v lacks %q after skill.enabled", cfg.Tools.Enabled, name)
		}
	}
}

// M6a-2: schedule is off by default (D10) and the serial clock cadence
// defaults to 1m; the schedule_* tools must not be whitelisted while disabled
// (dispatch-m6a-2 §2).
func TestLoadScheduleDefaultsWhenAbsent(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Schedule.Enabled {
		t.Error("schedule must be disabled by default (D10)")
	}
	if cfg.Schedule.TickInterval.Duration != DefaultScheduleTickInterval {
		t.Errorf("schedule.tick_interval = %v, want default %v",
			cfg.Schedule.TickInterval.Duration, DefaultScheduleTickInterval)
	}
	for _, name := range scheduleToolNames {
		if contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when schedule disabled", cfg.Tools.Enabled, name)
		}
	}
}

// M6a-2: an explicit schedule section is honored (enabled, tick_interval as a
// Go duration string), while a non-positive cadence falls back to the default
// (校验非负: a negative value never survives) (dispatch-m6a-2 §2).
func TestLoadScheduleParsesSectionAndFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("schedule:\n  enabled: true\n  tick_interval: 30s\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Schedule.Enabled {
		t.Error("schedule.enabled should be true")
	}
	if cfg.Schedule.TickInterval.Duration != 30*time.Second {
		t.Errorf("schedule.tick_interval = %v, want 30s", cfg.Schedule.TickInterval.Duration)
	}

	// A non-positive (including negative) cadence falls back to the default.
	path2 := filepath.Join(t.TempDir(), "config2.yaml")
	if err := os.WriteFile(path2, []byte("schedule:\n  enabled: true\n  tick_interval: -1m\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Schedule.TickInterval.Duration != DefaultScheduleTickInterval {
		t.Errorf("schedule.tick_interval -1m = %v, want default %v",
			cfg2.Schedule.TickInterval.Duration, DefaultScheduleTickInterval)
	}
}

// M6a-2: schedule.enabled: true is the single switch that turns the whole
// capability on — the schedule_* tools must also become whitelisted
// (dispatch-m6a-2 §2, mirrors kb/jobs/subagent/skill).
func TestLoadScheduleEnabledAppendsToolsToWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("schedule:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range scheduleToolNames {
		if !contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v lacks %q after schedule.enabled", cfg.Tools.Enabled, name)
		}
	}
}

// M6b-2: plan is off by default (D10) and the plan_* tools must not be
// whitelisted while disabled (dispatch-m6b-2 §2).
func TestLoadPlanDefaultsWhenAbsent(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Plan.Enabled {
		t.Error("plan must be disabled by default (D10)")
	}
	for _, name := range planToolNames {
		if contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when plan disabled", cfg.Tools.Enabled, name)
		}
	}
}

// M6b-2: an explicit plan section is honored, and an explicit plan.enabled:
// false leaves the default whitelist untouched (D10).
func TestLoadPlanParsesSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("plan:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Plan.Enabled {
		t.Error("plan.enabled should be true")
	}

	path2 := filepath.Join(t.TempDir(), "config2.yaml")
	if err := os.WriteFile(path2, []byte("plan:\n  enabled: false\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Plan.Enabled {
		t.Error("plan.enabled = true, want false (explicitly disabled)")
	}
	for _, name := range planToolNames {
		if contains(cfg2.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when plan explicitly disabled", cfg2.Tools.Enabled, name)
		}
	}
}

// M6b-2: plan.enabled: true is the single switch that turns the whole
// capability on — the six plan_* tools must also become whitelisted
// (dispatch-m6b-2 §2, mirrors kb/jobs/subagent/skill/schedule).
func TestLoadPlanEnabledAppendsToolsToWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("plan:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range planToolNames {
		if !contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v lacks %q after plan.enabled", cfg.Tools.Enabled, name)
		}
	}
}

// M6c-2: an absent spill section means the capability is off by default (D10)
// with auto_spill defaulting on within an enabled spill (AutoSpillValue), and
// no spill_* tool is whitelisted.
func TestLoadSpillDefaultsWhenAbsent(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Spill.Enabled {
		t.Error("spill must be disabled by default (D10)")
	}
	if !cfg.Spill.AutoSpillValue() {
		t.Error("auto_spill must default to true (absent ⇒ true)")
	}
	for _, name := range spillToolNames {
		if contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when spill disabled", cfg.Tools.Enabled, name)
		}
	}
}

// M6c-2: an explicit spill section is honored; an explicit enabled:false
// leaves the default whitelist untouched, and auto_spill:false disables the
// auto-sedimentation while an explicit true/absent keeps it on.
func TestLoadSpillParsesSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("spill:\n  enabled: false\n  auto_spill: false\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Spill.Enabled {
		t.Error("spill.enabled = true, want false (explicitly disabled)")
	}
	if cfg.Spill.AutoSpillValue() {
		t.Error("auto_spill must be false when explicitly disabled")
	}
	for _, name := range spillToolNames {
		if contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when spill explicitly disabled", cfg.Tools.Enabled, name)
		}
	}

	// auto_spill absent within an enabled spill defaults to true.
	path2 := filepath.Join(t.TempDir(), "config2.yaml")
	if err := os.WriteFile(path2, []byte("spill:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg2.Spill.Enabled {
		t.Error("spill.enabled should be true")
	}
	if !cfg2.Spill.AutoSpillValue() {
		t.Error("auto_spill must default to true when absent within an enabled spill")
	}
}

// M6c-2: spill.enabled: true is the single switch that turns the whole
// capability on — the four spill_* tools must also become whitelisted
// (dispatch-m6c-2 §2, mirrors kb/jobs/subagent/skill/schedule/plan).
func TestLoadSpillEnabledAppendsToolsToWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("spill:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range spillToolNames {
		if !contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v lacks %q after spill.enabled", cfg.Tools.Enabled, name)
		}
	}
}

// M6d-2: interact is off by default (D10), sensitive_tools is empty (no
// gating), and the interact_* tools must not be whitelisted while disabled
// (dispatch-m6d-2 §2).
func TestLoadInteractDefaultsWhenAbsent(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Interact.Enabled {
		t.Error("interact must be disabled by default (D10)")
	}
	if len(cfg.Interact.SensitiveTools) != 0 {
		t.Errorf("interact.sensitive_tools = %v, want empty (no gating by default)", cfg.Interact.SensitiveTools)
	}
	for _, name := range interactToolNames {
		if contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when interact disabled", cfg.Tools.Enabled, name)
		}
	}
}

// M6d-2: an explicit interact section is honored (enabled, sensitive_tools),
// while an explicit enabled:false leaves the default whitelist untouched and an
// empty sensitive_tools stays empty (D10, dispatch-m6d-2 §2).
func TestLoadInteractParsesSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("interact:\n  enabled: true\n  sensitive_tools: [run_command, job_start]\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Interact.Enabled {
		t.Error("interact.enabled should be true")
	}
	if len(cfg.Interact.SensitiveTools) != 2 || cfg.Interact.SensitiveTools[0] != "run_command" || cfg.Interact.SensitiveTools[1] != "job_start" {
		t.Errorf("interact.sensitive_tools = %v, want [run_command job_start]", cfg.Interact.SensitiveTools)
	}

	// Explicit enabled:false and an empty sensitive_tools stay verbatim.
	path2 := filepath.Join(t.TempDir(), "config2.yaml")
	if err := os.WriteFile(path2, []byte("interact:\n  enabled: false\n  sensitive_tools: []\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Interact.Enabled {
		t.Error("interact.enabled = true, want false (explicitly disabled)")
	}
	if len(cfg2.Interact.SensitiveTools) != 0 {
		t.Errorf("interact.sensitive_tools = %v, want empty", cfg2.Interact.SensitiveTools)
	}
	for _, name := range interactToolNames {
		if contains(cfg2.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when interact explicitly disabled", cfg2.Tools.Enabled, name)
		}
	}
}

// M6d-2: interact.enabled: true is the single switch that turns the whole
// capability on — the two interact_* tools must also become whitelisted
// (dispatch-m6d-2 §2, mirrors kb/jobs/subagent/skill/schedule/plan/spill).
func TestLoadInteractEnabledAppendsToolsToWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("interact:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range interactToolNames {
		if !contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v lacks %q after interact.enabled", cfg.Tools.Enabled, name)
		}
	}
}

// M6e-2: an absent code section means the capability is off by default (D10),
// the sandbox timeout defaults to 30s, the per-stream output cap to 65536,
// sandbox_dir stays empty (provider default <project>/.sandbox), allow_network
// stays false (declarative no-network boundary), and code_run is not
// whitelisted while disabled (dispatch-m6e-2 §2).
func TestLoadCodeDefaultsWhenAbsent(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Code.Enabled {
		t.Error("code must be disabled by default (D10)")
	}
	if cfg.Code.Timeout.Duration != DefaultCodeTimeout {
		t.Errorf("code.timeout = %v, want default %v", cfg.Code.Timeout.Duration, DefaultCodeTimeout)
	}
	if cfg.Code.MaxOutput != DefaultCodeMaxOutput {
		t.Errorf("code.max_output = %d, want default %d", cfg.Code.MaxOutput, DefaultCodeMaxOutput)
	}
	if cfg.Code.SandboxDir != "" {
		t.Errorf("code.sandbox_dir = %q, want empty (provider default <project>/.sandbox)", cfg.Code.SandboxDir)
	}
	if cfg.Code.AllowNetwork {
		t.Error("code.allow_network must default to false (declarative no-network boundary)")
	}
	for _, name := range codeToolNames {
		if contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when code disabled", cfg.Tools.Enabled, name)
		}
	}
}

// M6e-2: an explicit code section is honored (enabled, timeout, max_output,
// sandbox_dir, allow_network), while a non-positive timeout/max_output fall
// back to their defaults (校验非负: a negative configured value never survives).
func TestLoadCodeParsesSectionAndFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("code:\n  enabled: true\n  timeout: 5s\n  max_output: 4096\n  sandbox_dir: C:\\sandbox\n  allow_network: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Code.Enabled {
		t.Error("code.enabled should be true")
	}
	if cfg.Code.Timeout.Duration != 5*time.Second {
		t.Errorf("code.timeout = %v, want 5s", cfg.Code.Timeout.Duration)
	}
	if cfg.Code.MaxOutput != 4096 {
		t.Errorf("code.max_output = %d, want 4096", cfg.Code.MaxOutput)
	}
	if cfg.Code.SandboxDir != `C:\sandbox` {
		t.Errorf("code.sandbox_dir = %q, want C:\\sandbox", cfg.Code.SandboxDir)
	}
	if !cfg.Code.AllowNetwork {
		t.Error("code.allow_network = false, want true (explicitly enabled)")
	}

	// Non-positive (including negative) bounds fall back to the defaults.
	path2 := filepath.Join(t.TempDir(), "config2.yaml")
	if err := os.WriteFile(path2, []byte("code:\n  enabled: true\n  timeout: -1s\n  max_output: 0\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Code.Timeout.Duration != DefaultCodeTimeout {
		t.Errorf("code.timeout -1s = %v, want default %v", cfg2.Code.Timeout.Duration, DefaultCodeTimeout)
	}
	if cfg2.Code.MaxOutput != DefaultCodeMaxOutput {
		t.Errorf("code.max_output 0 = %d, want default %d", cfg2.Code.MaxOutput, DefaultCodeMaxOutput)
	}
}

// M6e-2: code.enabled: true is the single switch that turns the whole
// capability on — the code_run tool must also become whitelisted
// (dispatch-m6e-2 §2, mirrors kb/jobs/subagent/skill/schedule/plan/spill/
// interact).
func TestLoadCodeEnabledAppendsToolsToWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("code:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range codeToolNames {
		if !contains(cfg.Tools.Enabled, name) {
			t.Errorf("whitelist %v lacks %q after code.enabled", cfg.Tools.Enabled, name)
		}
	}

	// Explicit enabled:false leaves the default whitelist untouched.
	path2 := filepath.Join(t.TempDir(), "config2.yaml")
	if err := os.WriteFile(path2, []byte("code:\n  enabled: false\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Code.Enabled {
		t.Error("code.enabled = true, want false (explicitly disabled)")
	}
	for _, name := range codeToolNames {
		if contains(cfg2.Tools.Enabled, name) {
			t.Errorf("whitelist %v must not contain %q when code explicitly disabled", cfg2.Tools.Enabled, name)
		}
	}
}
