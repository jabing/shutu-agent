// llm.go — the M8-2 composition-root LLM wiring (dispatch-m8-2 §6 / M8-2b §3).
// This is where the multi-provider registry is built and the selected provider
// is injected into the REPL: registerLLM registers the deepseek provider
// (always; DEEPSEEK_API_KEY env-only), the openai provider (only when
// OPENAI_API_KEY is present), and the anthropic provider (only when
// ANTHROPIC_API_KEY is present, M8-2b), resolves cfg.LLM.Provider against the
// registry (unknown id ⇒ fail-closed startup error, no silent fallback), and
// injects the selected provider into a.llm — the single llm.LLM that the loop,
// compaction, subagent and kb extraction all consume (D2). The registry is
// kept on app.llmReg for /llm-status, which reports provider/model/modalities
// in the /kb-status style. The loop's turn/step structure is untouched (D4):
// the loop keeps calling a.llm.Stream and never sees the registry.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/jabing/shutu-agent/internal/config"
	"github.com/jabing/shutu-agent/internal/llm"
	"github.com/jabing/shutu-agent/internal/llm/anthropic"
	"github.com/jabing/shutu-agent/internal/llm/deepseek"
	"github.com/jabing/shutu-agent/internal/llm/openai"
)

// registerLLM builds the provider registry and injects the selected provider
// into a.llm. Fail-closed contract (dispatch-m8-2 §6):
//   - an unknown cfg.LLM.Provider is a startup error (no silent fallback);
//   - a selected provider whose credential is missing is a startup error too —
//     this preserves the M8-1-before behavior of a missing DEEPSEEK_API_KEY
//     failing at startup, made provider-aware (纪律 6: 凭证 env-only).
//
// Other registered providers may be unavailable (their key absent) — /llm-status
// reports them as such; only the selected one must be usable.
//
// M11 (增加提供方 / 增加自定义提供方, dsh-synced): every provider id can carry a
// configured API key persisted in the settings table (llm.key.<id>); a configured
// key wins over the env var (配置后以配置的为准, user 2026-09). Custom
// OpenAI-compatible providers are declared in settings (llm.custom.<id> JSON) and
// registered here under their route.
func (a *app) registerLLM() error {
	reg := llm.NewRegistry()

	// The deepseek provider is always registered; its parameters come from the
	// legacy top-level model/base_url (the deepseek default configuration,
	// dispatch-m8-2 §5) and DEEPSEEK_API_KEY from the environment (a configured
	// llm.key.deepseek setting wins, M11).
	if err := reg.Register(deepseek.New(deepseek.Config{
		APIKey:               a.providerKey("deepseek"),
		BaseURL:              a.cfg.BaseURL,
		Model:                a.cfg.Model,
		MaxRetries:           2,
		SupportsImages:       strings.Contains(a.cfg.LLM.ModelInputModalities, "image"),
		MaxRequestImageBytes: a.cfg.LLM.Multimodal.MaxRequestImageBytes, // 默认 20MiB 由 New 兜底
	})); err != nil {
		return fmt.Errorf("pa: register deepseek provider: %w", err)
	}

	// The openai provider is registered only when its credential is present
	// (OPENAI_API_KEY env-only, dispatch-m8-2 §6; configured llm.key.openai
	// wins, M11). It reuses the deepseek OpenAI-compatible client — zero new
	// wire code (dispatch-m8-2 §4).
	if key := a.providerKey("openai"); key != "" {
		if err := reg.Register(openai.New(openai.Config{
			APIKey:               key,
			BaseURL:              a.cfg.LLM.OpenAI.BaseURL,
			Model:                a.cfg.LLM.OpenAI.Model,
			SupportsImages:       strings.Contains(a.cfg.LLM.ModelInputModalities, "image"),
			MaxRequestImageBytes: a.cfg.LLM.Multimodal.MaxRequestImageBytes, // 默认 20MiB 由 New 兜底
		})); err != nil {
			return fmt.Errorf("pa: register openai provider: %w", err)
		}
	}

	// The anthropic provider is registered only when its credential is present
	// (ANTHROPIC_API_KEY env-only, dispatch-m8-2b §3; configured llm.key.anthropic
	// wins, M11). Its parameters come from llm.anthropic.base_url/model
	// (defaults https://api.anthropic.com/v1 / claude-sonnet-4-5, M8-2b §3).
	if key := a.providerKey("anthropic"); key != "" {
		if err := reg.Register(anthropic.New(anthropic.Config{
			APIKey:               key,
			BaseURL:              a.cfg.LLM.Anthropic.BaseURL,
			Model:                a.cfg.LLM.Anthropic.Model,
			SupportsImages:       strings.Contains(a.cfg.LLM.ModelInputModalities, "image"),
			MaxRequestImageBytes: a.cfg.LLM.Multimodal.MaxRequestImageBytes, // 默认 20MiB 由 New 兜底
		})); err != nil {
			return fmt.Errorf("pa: register anthropic provider: %w", err)
		}
	}

	// M11: custom OpenAI-compatible providers declared through the Model
	// settings page (llm.custom.<id> in the settings table). Each carries its
	// own route id, display name, base URL and model; its key follows the same
	// precedence (configured llm.key.<id> > env <ROUTE>_API_KEY).
	for _, cp := range a.customProviders {
		if err := reg.Register(openai.New(openai.Config{
			ID:                   cp.ID,
			APIKey:               a.providerKey(cp.ID),
			BaseURL:              cp.BaseURL,
			Model:                cp.Model,
			SupportsImages:       strings.Contains(a.cfg.LLM.ModelInputModalities, "image"),
			MaxRequestImageBytes: a.cfg.LLM.Multimodal.MaxRequestImageBytes,
		})); err != nil {
			return fmt.Errorf("pa: register custom provider %q: %w", cp.ID, err)
		}
	}

	// Select by cfg.LLM.Provider; an unknown id is a fail-closed startup error
	// (dispatch-m8-2 §5/§6).
	p, err := reg.Get(a.cfg.LLM.Provider)
	if err != nil {
		return fmt.Errorf("pa: %w (llm.provider=%q; registered: %s)", err, a.cfg.LLM.Provider, llmProviderIDs(reg))
	}
	if !p.Available() {
		return fmt.Errorf("pa: llm provider %q is not available (missing %s or invalid base_url)", p.ID(), llmCredentialEnv(p.ID()))
	}

	a.llmMu.Lock()
	a.llm = p
	a.llmReg = reg
	a.llmMu.Unlock()
	return nil
}

// currentLLM returns the currently selected provider under the read lock. Every
// consumer that wires a.llm into a component (loop, compaction, kb extraction,
// subagent spawn, eval judge) reads through this so the live model switch can
// swap the pointer safely (P5.1). Loop is re-wired every turn (buildLoop), so
// a model switch takes effect on the very next message.
func (a *app) currentLLM() llm.LLM {
	a.llmMu.RLock()
	defer a.llmMu.RUnlock()
	return a.llm
}

// llmProviderIDs returns the registered provider ids as a comma-joined list
// (for the fail-closed error message).
func llmProviderIDs(reg *llm.Registry) string {
	ids := make([]string, 0, len(reg.List()))
	for _, p := range reg.List() {
		ids = append(ids, p.ID())
	}
	return strings.Join(ids, ", ")
}

// customProviderProfile is the persisted M11 custom-provider declaration
// (settings row llm.custom.<route> = JSON). A custom provider is an
// OpenAI-compatible endpoint: route id, display name, base URL and default
// model. Its API key follows the standard precedence (llm.key.<route> setting
// > env <ROUTE>_API_KEY).
type customProviderProfile struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
}

// llmKeyEnv returns the environment variable that carries provider id's API
// key. Built-ins map to their canonical credential variable; a custom provider
// id derives one by upper-casing the route (my-llm → MY_LLM_API_KEY). This is
// the env-only default (纪律 6); a key configured through the Model settings
// page (llm.key.<id>, M11) takes precedence over it (配置后以配置的为准).
func llmKeyEnv(id string) string {
	switch id {
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	default:
		return strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_API_KEY"
	}
}

// providerKey returns provider id's effective API key: a key configured through
// the Model settings page wins (llm.key.<id>, persisted in the settings table),
// otherwise the environment variable (llmKeyEnv). nil llmKeys (direct-constructed
// apps in tests) falls straight back to the env default.
func (a *app) providerKey(id string) string {
	if a.llmKeys != nil {
		if k, ok := a.llmKeys[id]; ok && k != "" {
			return k
		}
	}
	return os.Getenv(llmKeyEnv(id))
}

// llmCredentialEnv returns the environment variable that carries provider id's
// API key (env-only, 纪律 6).
func llmCredentialEnv(id string) string {
	switch id {
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	default:
		return llmKeyEnv(id)
	}
}

// llmStatus prints the /llm-status report (dispatch-m8-2 §6, 照 /kb-status
// 风格; M8-3 §4 adds modalities + multimodal): the selected provider (marked *),
// every registered provider with its availability, the input modalities
// (cfg.LLM.ModelInputModalities: text / text,image), and the multimodal gate
// (enabled|disabled, D10). An unconfigured provider (key absent / bad base_url)
// is shown as unavailable.
func (a *app) llmStatus() error {
	if a.llmReg == nil {
		fmt.Println("llm: no provider registry (registerLLM did not run)")
		return nil
	}
	sel := a.cfg.LLM.Provider
	fmt.Println("llm: enabled")
	for _, p := range a.llmReg.List() {
		marker := "  "
		if p.ID() == sel {
			marker = "* "
		}
		avail := "available"
		if !p.Available() {
			avail = "unavailable"
		}
		fmt.Printf("%s%s: %s (model=%s)\n", marker, p.ID(), avail, llmProviderModel(a.cfg, p.ID()))
	}
	fmt.Printf("  modalities: %s\n", llmModalitiesValue(a.cfg))
	mm := "disabled"
	if a.multimodalEnabled() {
		mm = "enabled"
	}
	fmt.Printf("  multimodal: %s\n", mm)
	return nil
}

// llmModalitiesValue returns the effective model_input_modalities declaration,
// falling back to the default "text" when the config field is empty (the
// applyDefaults path always fills it, but direct-constructed configs in tests
// and defensive callers read the fallback). /llm-status and printHelp display
// it (dispatch-m8-3 §3/§4: "text" | "text,image").
func llmModalitiesValue(cfg config.Config) string {
	if cfg.LLM.ModelInputModalities == "" {
		return config.DefaultModelInputModalities
	}
	return cfg.LLM.ModelInputModalities
}

// llmProviderModel returns the configured model for provider id: the legacy
// top-level model for deepseek, llm.openai.model for openai, llm.anthropic.model
// for anthropic (dispatch-m8-2 §5 / M8-2b §3: top-level model/base_url stay as
// the deepseek default configuration).
func llmProviderModel(cfg config.Config, id string) string {
	switch id {
	case "openai":
		return cfg.LLM.OpenAI.Model
	case "anthropic":
		return cfg.LLM.Anthropic.Model
	}
	return cfg.Model
}

// llmProviderBaseURL returns the effective base URL for provider id (the web
// Model editor shows it read-only; an empty value means the provider default,
// rendered as "提供方默认"). It mirrors llmProviderModel's routing: the legacy
// top-level base_url stays the deepseek default configuration.
func llmProviderBaseURL(cfg config.Config, id string) string {
	switch id {
	case "openai":
		return cfg.LLM.OpenAI.BaseURL
	case "anthropic":
		return cfg.LLM.Anthropic.BaseURL
	}
	return cfg.BaseURL
}

// modelCandidates returns the suggested model names for provider id (P5.1 live
// model picker). These are honest suggestions — the picker also allows a free
// model string. Candidates mirror the M8-1/M8-2/M8-2b defaults.
func modelCandidates(id string) []string {
	switch id {
	case "deepseek":
		return []string{"deepseek-chat", "deepseek-reasoner"}
	case "openai":
		return []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini"}
	case "anthropic":
		return []string{"claude-sonnet-4-5", "claude-opus-4-1", "claude-haiku-4-5"}
	}
	return nil
}
