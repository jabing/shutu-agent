package main

import (
	"strings"
	"testing"

	"personal-agent/internal/config"
)

// TestRegisterLLMDefaultDeepseekRegression verifies the M8-2 default-provider
// regression (dispatch-m8-2 §7): with no OPENAI_API_KEY only deepseek is
// registered, and with the default llm.provider (deepseek) the selected
// provider is injected into a.llm — behavior identical to before M8-2.
func TestRegisterLLMDefaultDeepseekRegression(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	a := &app{
		cfg: config.Config{
			Model: "deepseek-chat",
			LLM:   config.LLMConfig{Provider: "deepseek"},
		},
	}
	if err := a.registerLLM(); err != nil {
		t.Fatalf("registerLLM: %v", err)
	}
	if a.llm == nil || a.llmReg == nil {
		t.Fatal("registerLLM must set a.llm and a.llmReg")
	}

	// Only deepseek is registered.
	ids := make([]string, 0, 1)
	for _, p := range a.llmReg.List() {
		ids = append(ids, p.ID())
	}
	if len(ids) != 1 || ids[0] != "deepseek" {
		t.Fatalf("registered providers = %v, want [deepseek]", ids)
	}

	// The selected (default) provider is deepseek and it is the injected llm.
	sel, err := a.llmReg.Get(a.cfg.LLM.Provider)
	if err != nil {
		t.Fatalf("Get %q: %v", a.cfg.LLM.Provider, err)
	}
	if !sel.Available() {
		t.Fatal("deepseek must be available with DEEPSEEK_API_KEY set")
	}
	if a.llm != sel {
		t.Fatal("a.llm must be the selected provider")
	}
}

// TestRegisterLLMRegistersOpenaiWhenKeyPresent verifies the openai registration
// gate (dispatch-m8-2 §6): when OPENAI_API_KEY is present the openai provider
// is registered too and can be selected.
func TestRegisterLLMRegistersOpenaiWhenKeyPresent(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "dk")
	t.Setenv("OPENAI_API_KEY", "ok")
	t.Setenv("ANTHROPIC_API_KEY", "")
	a := &app{
		cfg: config.Config{
			Model: "deepseek-chat",
			LLM: config.LLMConfig{
				Provider: "openai",
				OpenAI:   config.OpenAIProviderConfig{BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini"},
			},
		},
	}
	if err := a.registerLLM(); err != nil {
		t.Fatalf("registerLLM: %v", err)
	}
	ids := make([]string, 0, 2)
	for _, p := range a.llmReg.List() {
		ids = append(ids, p.ID())
	}
	if len(ids) != 2 || ids[0] != "deepseek" || ids[1] != "openai" {
		t.Fatalf("registered providers = %v, want [deepseek openai]", ids)
	}
	sel, err := a.llmReg.Get("openai")
	if err != nil {
		t.Fatalf("Get openai: %v", err)
	}
	if !sel.Available() {
		t.Fatal("openai must be available with OPENAI_API_KEY set")
	}
	if a.llm != sel {
		t.Fatal("a.llm must be the selected openai provider")
	}
}

// TestRegisterLLMUnknownProviderFailsClosed verifies the fail-closed startup
// rule (dispatch-m8-2 §5/§6/§7): an unknown llm.provider errors instead of
// silently falling back.
func TestRegisterLLMUnknownProviderFailsClosed(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "")
	a := &app{cfg: config.Config{LLM: config.LLMConfig{Provider: "nope"}}}
	if err := a.registerLLM(); err == nil {
		t.Fatal("unknown llm.provider must fail closed at startup")
	} else if !strings.Contains(err.Error(), "no such provider") {
		t.Errorf("err = %q, want the registry no-such-provider error", err)
	}
	if a.llm != nil {
		t.Fatal("a.llm must stay nil when registration fails")
	}
}

// TestRegisterLLMSelectedProviderUnavailableFailsClosed verifies the selected
// provider's credential gate: a missing key for the selected provider is a
// fail-closed startup error — preserving the M8-1-before behavior of a missing
// DEEPSEEK_API_KEY failing at startup, made provider-aware (纪律 6).
func TestRegisterLLMSelectedProviderUnavailableFailsClosed(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	a := &app{cfg: config.Config{LLM: config.LLMConfig{Provider: "deepseek"}}}
	if err := a.registerLLM(); err == nil {
		t.Fatal("selected deepseek with no DEEPSEEK_API_KEY must fail closed at startup")
	} else if !strings.Contains(err.Error(), "not available") {
		t.Errorf("err = %q, want the not-available message", err)
	}
}

// TestLLMStatusOutput verifies the /llm-status report (dispatch-m8-2 §6/§7):
// the selected provider marked *, availability per registered provider, and the
// modalities line (照 /kb-status 风格).
func TestLLMStatusOutput(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	a := &app{
		cfg: config.Config{
			Model: "deepseek-chat",
			LLM: config.LLMConfig{
				Provider: "openai",
				OpenAI:   config.OpenAIProviderConfig{BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini"},
			},
		},
	}
	if err := a.registerLLM(); err != nil {
		t.Fatalf("registerLLM: %v", err)
	}
	out := captureStdout(func() {
		if err := a.llmStatus(); err != nil {
			t.Errorf("llmStatus: %v", err)
		}
	})
	if !strings.Contains(out, "llm: enabled") {
		t.Errorf("output missing header: %q", out)
	}
	if !strings.Contains(out, "* openai: available (model=gpt-4o-mini)") {
		t.Errorf("output missing selected openai line: %q", out)
	}
	if !strings.Contains(out, "deepseek: available (model=deepseek-chat)") {
		t.Errorf("output missing deepseek line: %q", out)
	}
	if !strings.Contains(out, "modalities: text") {
		t.Errorf("output missing modalities line: %q", out)
	}
	if !strings.Contains(out, "multimodal: disabled") {
		t.Errorf("output missing multimodal disabled line (D10 default): %q", out)
	}
}

// TestLLMStatusShowsUnavailableProvider verifies an unconfigured (keyless)
// registered provider is reported as unavailable (dispatch-m8-2 §6: 未配置的
// provider 显示 unavailable). The deepseek provider is always registered; with
// no DEEPSEEK_API_KEY it stays in the registry and shows as unavailable while
// the selected openai provider (OPENAI_API_KEY set) is active.
func TestLLMStatusShowsUnavailableProvider(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("ANTHROPIC_API_KEY", "")
	a := &app{
		cfg: config.Config{
			Model: "deepseek-chat",
			LLM: config.LLMConfig{
				Provider: "openai",
				OpenAI:   config.OpenAIProviderConfig{BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini"},
			},
		},
	}
	if err := a.registerLLM(); err != nil {
		t.Fatalf("registerLLM: %v", err)
	}
	out := captureStdout(func() {
		if err := a.llmStatus(); err != nil {
			t.Errorf("llmStatus: %v", err)
		}
	})
	if !strings.Contains(out, "* openai: available (model=gpt-4o-mini)") {
		t.Errorf("output missing selected openai line: %q", out)
	}
	if !strings.Contains(out, "deepseek: unavailable") {
		t.Errorf("output must show deepseek as unavailable: %q", out)
	}
}

// TestLLMStatusWithoutRegistry verifies /llm-status before registerLLM reports
// the missing registry instead of panicking.
func TestLLMStatusWithoutRegistry(t *testing.T) {
	a := &app{}
	out := captureStdout(func() {
		if err := a.llmStatus(); err != nil {
			t.Errorf("llmStatus: %v", err)
		}
	})
	if !strings.Contains(out, "no provider registry") {
		t.Errorf("output = %q, want the no-registry report", out)
	}
}

// TestRegisterLLMRegistersAnthropicWhenKeyPresent verifies the anthropic
// registration gate (dispatch-m8-2b §3): when ANTHROPIC_API_KEY is present the
// anthropic provider is registered too and can be selected.
func TestRegisterLLMRegistersAnthropicWhenKeyPresent(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "dk")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "ak")
	a := &app{
		cfg: config.Config{
			Model: "deepseek-chat",
			LLM: config.LLMConfig{
				Provider:  "anthropic",
				Anthropic: config.AnthropicProviderConfig{BaseURL: "https://api.anthropic.com/v1", Model: "claude-sonnet-4-5"},
			},
		},
	}
	if err := a.registerLLM(); err != nil {
		t.Fatalf("registerLLM: %v", err)
	}
	ids := make([]string, 0, 2)
	for _, p := range a.llmReg.List() {
		ids = append(ids, p.ID())
	}
	if len(ids) != 2 || ids[0] != "deepseek" || ids[1] != "anthropic" {
		t.Fatalf("registered providers = %v, want [deepseek anthropic]", ids)
	}
	sel, err := a.llmReg.Get("anthropic")
	if err != nil {
		t.Fatalf("Get anthropic: %v", err)
	}
	if !sel.Available() {
		t.Fatal("anthropic must be available with ANTHROPIC_API_KEY set")
	}
	if a.llm != sel {
		t.Fatal("a.llm must be the selected anthropic provider")
	}
}

// TestRegisterLLMAnthropicNotRegisteredWithoutKey verifies the anthropic
// registration gate is key-gated (dispatch-m8-2b §3): without ANTHROPIC_API_KEY
// the anthropic provider is not registered, so selecting it fails closed.
func TestRegisterLLMAnthropicNotRegisteredWithoutKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "dk")
	t.Setenv("ANTHROPIC_API_KEY", "")
	a := &app{
		cfg: config.Config{
			Model: "deepseek-chat",
			LLM:   config.LLMConfig{Provider: "anthropic"},
		},
	}
	if err := a.registerLLM(); err == nil {
		t.Fatal("selecting anthropic with no ANTHROPIC_API_KEY must fail closed")
	} else if !strings.Contains(err.Error(), "no such provider") {
		t.Errorf("err = %q, want the no-such-provider error", err)
	}
}

// TestLLMStatusShowsAnthropic verifies /llm-status includes the anthropic
// provider when registered (dispatch-m8-2b §3: /llm-status 自动涵盖 via the
// registry listing).
func TestLLMStatusShowsAnthropic(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "dk")
	t.Setenv("ANTHROPIC_API_KEY", "ak")
	a := &app{
		cfg: config.Config{
			Model: "deepseek-chat",
			LLM: config.LLMConfig{
				Provider:  "anthropic",
				Anthropic: config.AnthropicProviderConfig{BaseURL: "https://api.anthropic.com/v1", Model: "claude-sonnet-4-5"},
			},
		},
	}
	if err := a.registerLLM(); err != nil {
		t.Fatalf("registerLLM: %v", err)
	}
	out := captureStdout(func() {
		if err := a.llmStatus(); err != nil {
			t.Errorf("llmStatus: %v", err)
		}
	})
	if !strings.Contains(out, "* anthropic: available (model=claude-sonnet-4-5)") {
		t.Errorf("output missing selected anthropic line: %q", out)
	}
	if !strings.Contains(out, "deepseek: available (model=deepseek-chat)") {
		t.Errorf("output missing deepseek line: %q", out)
	}
	if !strings.Contains(out, "modalities: text") {
		t.Errorf("output missing modalities line: %q", out)
	}
}

// TestLLMStatusMultimodalEnabled verifies the M8-3 /llm-status additions
// (dispatch-m8-3 §4): the modalities line reflects cfg.LLM.ModelInputModalities
// (text,image here) and the multimodal line reports enabled when
// llm.multimodal.enabled is true (vs the disabled default elsewhere).
func TestLLMStatusMultimodalEnabled(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	a := &app{
		cfg: config.Config{
			Model: "deepseek-chat",
			LLM: config.LLMConfig{
				Provider:             "deepseek",
				ModelInputModalities: "text,image",
				Multimodal:           config.MultimodalConfig{Enabled: true, MaxImageBytes: 1 << 20},
			},
		},
	}
	if err := a.registerLLM(); err != nil {
		t.Fatalf("registerLLM: %v", err)
	}
	out := captureStdout(func() {
		if err := a.llmStatus(); err != nil {
			t.Errorf("llmStatus: %v", err)
		}
	})
	if !strings.Contains(out, "modalities: text,image") {
		t.Errorf("output missing modalities text,image line: %q", out)
	}
	if !strings.Contains(out, "multimodal: enabled") {
		t.Errorf("output missing multimodal enabled line: %q", out)
	}
}

// TestLLMStatusMultimodalDisabledDefault verifies the D10 default shows
// multimodal: disabled even when a provider is registered and running.
func TestLLMStatusMultimodalDisabledDefault(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	a := &app{
		cfg: config.Config{
			Model: "deepseek-chat",
			LLM:   config.LLMConfig{Provider: "deepseek"},
		},
	}
	if err := a.registerLLM(); err != nil {
		t.Fatalf("registerLLM: %v", err)
	}
	out := captureStdout(func() {
		if err := a.llmStatus(); err != nil {
			t.Errorf("llmStatus: %v", err)
		}
	})
	if !strings.Contains(out, "multimodal: disabled") {
		t.Errorf("output missing multimodal disabled line: %q", out)
	}
	if !strings.Contains(out, "modalities: text") {
		t.Errorf("output missing modalities text line (fallback default): %q", out)
	}
}
