// webm11_test.go — the M11 (增加提供方 / 增加自定义提供方, dsh-synced) composition-
// root tests: provider API-key override persistence with env fallback, custom
// OpenAI-compatible provider registration, deletion, and the webProviders view
// that lists every known provider (built-ins always, customs from settings)
// with configured/registered/available state. The webserver-side API tests live
// in internal/webserver.
package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/jabing/shutu-agent/internal/config"
	"github.com/jabing/shutu-agent/internal/store"
)

// m11App builds an app with a real sqlite store and a registered registry so
// the provider-management endpoints can persist and rebuild. deepseek is the
// selected provider, so its key must be present for registerLLM to succeed.
func m11App(t *testing.T) (*app, *store.SQLiteStore) {
	t.Helper()
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mm := true
	a := &app{
		cfg: config.Config{
			Model: "deepseek-chat",
			LLM: config.LLMConfig{
				Provider:   "deepseek",
				OpenAI:     config.OpenAIProviderConfig{BaseURL: "https://api.openai.com/v1", Model: "gpt-4o"},
				Anthropic:  config.AnthropicProviderConfig{BaseURL: "https://api.anthropic.com/v1", Model: "claude-sonnet-4-5"},
				Multimodal: config.MultimodalConfig{Enabled: &mm, MaxImageBytes: 1 << 20},
			},
		},
		store: st,
	}
	if err := a.registerLLM(); err != nil {
		t.Fatalf("registerLLM: %v", err)
	}
	return a, st
}

// TestWebProviderKeyOverride verifies M11 key precedence: a key configured via
// webSaveProvider is persisted (llm.key.<id>), beats the env var, is visible as
// configured in webProviders, and deleting it falls back to the env default.
func TestWebProviderKeyOverride(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	a, st := m11App(t)

	// Before any override: configured via env, key from env.
	cfg := a.webConfig()
	if p := findProvider(cfg["providers"].([]map[string]any), "deepseek"); p == nil || p["configured"] != true {
		t.Fatalf("deepseek should be configured via env before override")
	}

	// Save an override key — it wins over the env var.
	if err := a.webSaveProvider(context.Background(), "deepseek", "ui-key"); err != nil {
		t.Fatalf("webSaveProvider: %v", err)
	}
	got, err := st.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["llm.key.deepseek"] != "ui-key" {
		t.Fatalf("llm.key.deepseek = %q, want ui-key (persisted)", got["llm.key.deepseek"])
	}
	if k := a.providerKey("deepseek"); k != "ui-key" {
		t.Fatalf("providerKey(deepseek) = %q, want ui-key (override wins)", k)
	}

	// Clearing the override falls back to the env var.
	if err := a.webSaveProvider(context.Background(), "deepseek", ""); err != nil {
		t.Fatalf("webSaveProvider(clear): %v", err)
	}
	if k := a.providerKey("deepseek"); k != "env-key" {
		t.Fatalf("providerKey(deepseek) after clear = %q, want env-key (fallback)", k)
	}
	if got, _ := st.GetSettings(context.Background()); got["llm.key.deepseek"] != "" {
		t.Fatalf("llm.key.deepseek should be deleted after clear, got %q", got["llm.key.deepseek"])
	}
}

// TestWebCustomProviderLifecycle verifies M11 增加自定义提供方: saving a custom
// OpenAI-compatible provider persists the profile, registers it in the live
// registry, lists it in webProviders, and DELETE removes it again.
func TestWebCustomProviderLifecycle(t *testing.T) {
	a, st := m11App(t)
	t.Setenv("OLLAMA_API_KEY", "ollama-key")

	if err := a.webSaveCustomProvider(context.Background(), "ollama", "Ollama", "http://localhost:11434/v1", "llama3.1", "", ""); err != nil {
		t.Fatalf("webSaveCustomProvider: %v", err)
	}

	// Persisted profile.
	got, _ := st.GetSettings(context.Background())
	if got["llm.custom.ollama"] == "" {
		t.Fatal("llm.custom.ollama should be persisted")
	}

	// Registered in the live registry under its route.
	p, err := a.llmReg.Get("ollama")
	if err != nil {
		t.Fatalf("custom provider not registered: %v", err)
	}
	if p.ID() != "ollama" {
		t.Fatalf("custom provider id = %q, want ollama", p.ID())
	}

	// Listed in webProviders as a custom, configured (env key present), available.
	cfg := a.webConfig()
	op := findProvider(cfg["providers"].([]map[string]any), "ollama")
	if op == nil {
		t.Fatal("custom provider missing from webProviders")
	}
	if op["custom"] != true || op["name"] != "Ollama" || op["base_url"] != "http://localhost:11434/v1" {
		t.Fatalf("custom provider view = %#v, want custom/name/base_url", op)
	}
	if op["configured"] != true || op["available"] != true {
		t.Fatalf("custom provider should be configured+available (env key), got %#v", op)
	}

	// Delete removes it from the registry, settings and webProviders.
	if err := a.webDeleteCustomProvider(context.Background(), "ollama"); err != nil {
		t.Fatalf("webDeleteCustomProvider: %v", err)
	}
	if _, err := a.llmReg.Get("ollama"); err == nil {
		t.Fatal("custom provider should be removed from the registry")
	}
	got, _ = st.GetSettings(context.Background())
	if got["llm.custom.ollama"] != "" || got["llm.key.ollama"] != "" {
		t.Fatalf("custom provider settings should be removed, got %q %q", got["llm.custom.ollama"], got["llm.key.ollama"])
	}
	if findProvider(a.webConfig()["providers"].([]map[string]any), "ollama") != nil {
		t.Fatal("custom provider should be gone from webProviders")
	}
}

// TestWebCustomProviderValidation verifies the M11 fail-closed rules: id/name/
// base_url/model are required, a custom route cannot shadow a built-in, an
// invalid route is rejected, and deleting a built-in is rejected.
func TestWebCustomProviderValidation(t *testing.T) {
	a, _ := m11App(t)
	cases := []struct {
		name string
		edit func() error
	}{
		{"missing fields", func() error { return a.webSaveCustomProvider(context.Background(), "x", "", "", "", "", "") }},
		{"builtin shadow", func() error { return a.webSaveCustomProvider(context.Background(), "openai", "X", "http://x/v1", "m", "", "") }},
		{"invalid route", func() error { return a.webSaveCustomProvider(context.Background(), "Bad Route!", "X", "http://x/v1", "m", "", "") }},
		{"trailing dash", func() error { return a.webSaveCustomProvider(context.Background(), "bad-", "X", "http://x/v1", "m", "", "") }},
		{"doubled dash", func() error { return a.webSaveCustomProvider(context.Background(), "bad--route", "X", "http://x/v1", "m", "", "") }},
		{"digit leading", func() error { return a.webSaveCustomProvider(context.Background(), "1st-route", "X", "http://x/v1", "m", "", "") }},
		{"unknown protocol", func() error { return a.webSaveCustomProvider(context.Background(), "my-llm", "X", "http://x/v1", "m", "", "bogus-protocol") }},
		{"delete builtin", func() error { return a.webDeleteCustomProvider(context.Background(), "deepseek") }},
		{"delete unknown", func() error { return a.webDeleteCustomProvider(context.Background(), "nope") }},
	}
	for _, tc := range cases {
		if err := tc.edit(); err == nil {
			t.Errorf("%s: want an error, got nil", tc.name)
		}
	}
	// Valid route characters are accepted.
	if err := a.webSaveCustomProvider(context.Background(), "my-llm-2", "X", "http://x/v1", "m", "", ""); err != nil {
		t.Errorf("valid route rejected: %v", err)
	}
}

// TestWebProvidersListsDormantBuiltins verifies M11 webProviders lists every
// directory built-in even when its env key is absent (dormant, so 增加提供方 can
// add them), the active deepseek stays configured, each entry carries its
// protocol and canonical env var, and a provider becomes registered as soon as
// its key is configured (by protocol).
func TestWebProvidersListsDormantBuiltins(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "env-key") // every other key absent
	a, _ := m11App(t)
	providers := a.webConfig()["providers"].([]map[string]any)

	seen := map[string]map[string]any{}
	for _, p := range providers {
		seen[p["id"].(string)] = p
	}
	// The full directory is listed, dormant (not registered/configured) except
	// deepseek, and every entry carries its protocol + env var.
	if len(seen) != len(builtinProviders) {
		t.Errorf("webProviders lists %d providers, want %d (full directory)", len(seen), len(builtinProviders))
	}
	for _, bp := range builtinProviders {
		p, ok := seen[bp.id]
		if !ok {
			t.Errorf("built-in provider %q missing from webProviders", bp.id)
			continue
		}
		if p["protocol"] != string(bp.protocol) {
			t.Errorf("%s protocol = %v, want %s", bp.id, p["protocol"], bp.protocol)
		}
		if p["env_var"] != bp.env {
			t.Errorf("%s env_var = %v, want %s", bp.id, p["env_var"], bp.env)
		}
	}
	if seen["deepseek"]["registered"] != true || seen["deepseek"]["configured"] != true {
		t.Fatalf("deepseek should be registered+configured, got %#v", seen["deepseek"])
	}
	for _, id := range []string{"openai", "anthropic", "google", "xai", "groq"} {
		if seen[id]["registered"] != false || seen[id]["configured"] != false {
			t.Fatalf("%s should be dormant (not registered, not configured), got %#v", id, seen[id])
		}
	}
	// openai/anthropic stay dormant: their keys were never set.
	if seen["openai"]["model"] != "gpt-4o" || seen["openai"]["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("openai should keep its config-driven model/base_url, got %#v", seen["openai"])
	}
}

// TestWebProvidersRegisterByProtocol verifies a directory provider becomes
// registered through the adapter that speaks its protocol once a key is
// configured (M11-pi-ai 四协议): a completions provider (groq), a messages
// provider (minimax), the Gemini provider (google) and a Responses provider
// (xai) all register; the env-var form and the settings-override form both work.
func TestWebProvidersRegisterByProtocol(t *testing.T) {
	a, st := m11App(t)

	// Env-var key only → provider registers, stays configured.
	t.Setenv("GROQ_API_KEY", "groq-key")
	if err := a.registerLLM(); err != nil {
		t.Fatalf("registerLLM after GROQ_API_KEY: %v", err)
	}
	if p, err := a.llmReg.Get("groq"); err != nil {
		t.Fatalf("groq not registered: %v", err)
	} else if !p.Available() {
		t.Fatal("groq should be available with its env key")
	}
	if op := findProvider(a.webConfig()["providers"].([]map[string]any), "groq"); op == nil || op["registered"] != true || op["configured"] != true {
		t.Fatalf("groq should be registered+configured, got %#v", op)
	}

	// Settings override key (llm.key.<id>) for a messages-protocol provider.
	if err := a.webSaveProvider(context.Background(), "minimax", "minimax-ui-key"); err != nil {
		t.Fatalf("webSaveProvider(minimax): %v", err)
	}
	if p, err := a.llmReg.Get("minimax"); err != nil {
		t.Fatalf("minimax not registered: %v", err)
	} else if p.ID() != "minimax" || !p.Available() {
		t.Fatalf("minimax registered under wrong id or unavailable: %v", p.ID())
	}
	if got, _ := st.GetSettings(context.Background()); got["llm.key.minimax"] != "minimax-ui-key" {
		t.Fatalf("llm.key.minimax = %q, want minimax-ui-key", got["llm.key.minimax"])
	}

	// Gemini protocol provider registers via its env var.
	t.Setenv("GEMINI_API_KEY", "gemini-key")
	if err := a.registerLLM(); err != nil {
		t.Fatalf("registerLLM after GEMINI_API_KEY: %v", err)
	}
	if p, err := a.llmReg.Get("google"); err != nil {
		t.Fatalf("google not registered: %v", err)
	} else if !p.Available() {
		t.Fatal("google should be available with its env key")
	}

	// Responses protocol provider registers via its env var.
	t.Setenv("XAI_API_KEY", "xai-key")
	if err := a.registerLLM(); err != nil {
		t.Fatalf("registerLLM after XAI_API_KEY: %v", err)
	}
	if p, err := a.llmReg.Get("xai"); err != nil {
		t.Fatalf("xai not registered: %v", err)
	} else if !p.Available() {
		t.Fatal("xai should be available with its env key")
	}

	// Removing the override returns the provider to dormant.
	if err := a.webSaveProvider(context.Background(), "minimax", ""); err != nil {
		t.Fatalf("webSaveProvider(minimax clear): %v", err)
	}
	if p, err := a.llmReg.Get("minimax"); err == nil || p != nil {
		t.Fatalf("minimax should be back to dormant after clearing, got %v", err)
	}
}

// TestProviderEnvSpecialCases verifies the directory's canonical env vars are
// honored (HF_TOKEN, KIMI_API_KEY, AI_GATEWAY_API_KEY — not the derived
// <UPPER>_API_KEY form).
func TestProviderEnvSpecialCases(t *testing.T) {
	cases := map[string]string{
		"huggingface":      "HF_TOKEN",
		"kimi-coding":      "KIMI_API_KEY",
		"vercel-ai-gateway": "AI_GATEWAY_API_KEY",
		"deepseek":         "DEEPSEEK_API_KEY",
		"groq":             "GROQ_API_KEY",
		"custom-route":     "CUSTOM_ROUTE_API_KEY",
	}
	for id, want := range cases {
		if got := providerEnv(id); got != want {
			t.Errorf("providerEnv(%s) = %q, want %q", id, got, want)
		}
	}
}

// TestWebCustomProviderProtocolDispatch verifies the M11-pi-ai protocol field:
// a custom provider declared with a non-completions protocol registers through
// the matching adapter (anthropic-messages → anthropic adapter under the custom
// route; google-generative-ai → google adapter), the profile persists the
// protocol, webProviders surfaces protocol + protocol_label, and an empty
// display name defaults to the route id (dsh CustomProviderCard 范式).
func TestWebCustomProviderProtocolDispatch(t *testing.T) {
	a, st := m11App(t)

	// anthropic-messages custom provider with a key override.
	if err := a.webSaveCustomProvider(context.Background(), "my-messages", "", "https://gw.example/anthropic", "claude-sonnet-4-5", "msg-key", "anthropic-messages"); err != nil {
		t.Fatalf("webSaveCustomProvider(messages): %v", err)
	}
	// Display name defaults to the route id.
	got, _ := st.GetSettings(context.Background())
	var cp customProviderProfile
	if err := json.Unmarshal([]byte(got["llm.custom.my-messages"]), &cp); err != nil {
		t.Fatalf("unmarshal profile: %v", err)
	}
	if cp.Name != "my-messages" || cp.Protocol != "anthropic-messages" {
		t.Fatalf("profile = %#v, want name defaulted to route + protocol persisted", cp)
	}
	// Registered under its route via the anthropic adapter.
	p, err := a.llmReg.Get("my-messages")
	if err != nil {
		t.Fatalf("custom provider not registered: %v", err)
	}
	if p.ID() != "my-messages" || !p.Available() {
		t.Fatalf("custom provider id = %q available=%v", p.ID(), p.Available())
	}
	// webProviders surfaces protocol + label.
	op := findProvider(a.webConfig()["providers"].([]map[string]any), "my-messages")
	if op == nil {
		t.Fatal("custom provider missing from webProviders")
	}
	if op["protocol"] != "anthropic-messages" || op["protocol_label"] != "Anthropic Messages" {
		t.Fatalf("protocol view = %#v", op)
	}
	if op["name"] != "my-messages" {
		t.Fatalf("name = %v, want defaulted route", op["name"])
	}

	// google-generative-ai custom provider (no key override; env fallback).
	t.Setenv("MY_GEMINI_API_KEY", "gem-key")
	if err := a.webSaveCustomProvider(context.Background(), "my-gemini", "My Gemini", "https://gem.example", "gemini-2.5-flash", "", "google-generative-ai"); err != nil {
		t.Fatalf("webSaveCustomProvider(gemini): %v", err)
	}
	if p, err := a.llmReg.Get("my-gemini"); err != nil {
		t.Fatalf("gemini custom provider not registered: %v", err)
	} else if !p.Available() {
		t.Fatal("gemini custom provider should be available with its env key")
	}
	og := findProvider(a.webConfig()["providers"].([]map[string]any), "my-gemini")
	if og == nil || og["protocol_label"] != "Google Generative AI" {
		t.Fatalf("gemini protocol view = %#v", og)
	}

	// Delete both; registry clears.
	if err := a.webDeleteCustomProvider(context.Background(), "my-messages"); err != nil {
		t.Fatalf("delete my-messages: %v", err)
	}
	if _, err := a.llmReg.Get("my-messages"); err == nil {
		t.Fatal("my-messages should be gone")
	}
}

func findProvider(list []map[string]any, id string) map[string]any {
	for _, p := range list {
		if p["id"] == id {
			return p
		}
	}
	return nil
}
