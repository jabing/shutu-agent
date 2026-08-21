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

	"personal-agent/internal/config"
	"personal-agent/internal/llm"
	"personal-agent/internal/llm/anthropic"
	"personal-agent/internal/llm/deepseek"
	"personal-agent/internal/llm/openai"
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
func (a *app) registerLLM() error {
	reg := llm.NewRegistry()

	// The deepseek provider is always registered; its parameters come from the
	// legacy top-level model/base_url (the deepseek default configuration,
	// dispatch-m8-2 §5) and DEEPSEEK_API_KEY from the environment.
	if err := reg.Register(deepseek.New(deepseek.Config{
		APIKey:               os.Getenv("DEEPSEEK_API_KEY"),
		BaseURL:              a.cfg.BaseURL,
		Model:                a.cfg.Model,
		MaxRetries:           2,
		SupportsImages:       strings.Contains(a.cfg.LLM.ModelInputModalities, "image"),
		MaxRequestImageBytes: a.cfg.LLM.Multimodal.MaxRequestImageBytes, // 默认 20MiB 由 New 兜底
	})); err != nil {
		return fmt.Errorf("pa: register deepseek provider: %w", err)
	}

	// The openai provider is registered only when its credential is present
	// (OPENAI_API_KEY env-only, dispatch-m8-2 §6). It reuses the deepseek
	// OpenAI-compatible client — zero new wire code (dispatch-m8-2 §4).
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
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
	// (ANTHROPIC_API_KEY env-only, dispatch-m8-2b §3). Its parameters come from
	// llm.anthropic.base_url/model (defaults https://api.anthropic.com/v1 /
	// claude-sonnet-4-5, dispatch-m8-2b §3).
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
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

	// Select by cfg.LLM.Provider; an unknown id is a fail-closed startup error
	// (dispatch-m8-2 §5/§6).
	p, err := reg.Get(a.cfg.LLM.Provider)
	if err != nil {
		return fmt.Errorf("pa: %w (llm.provider=%q; registered: %s)", err, a.cfg.LLM.Provider, llmProviderIDs(reg))
	}
	if !p.Available() {
		return fmt.Errorf("pa: llm provider %q is not available (missing %s or invalid base_url)", p.ID(), llmCredentialEnv(p.ID()))
	}

	a.llm = p
	a.llmReg = reg
	return nil
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
		return "the provider's API key environment variable"
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
