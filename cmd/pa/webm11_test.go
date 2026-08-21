// webm11_test.go — the M11 (增加提供方 / 增加自定义提供方, dsh-synced) composition-
// root tests: provider API-key override persistence with env fallback, custom
// OpenAI-compatible provider registration, deletion, and the webProviders view
// that lists every known provider (built-ins always, customs from settings)
// with configured/registered/available state. The webserver-side API tests live
// in internal/webserver.
package main

import (
	"context"
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

	if err := a.webSaveCustomProvider(context.Background(), "ollama", "Ollama", "http://localhost:11434/v1", "llama3.1", ""); err != nil {
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
		{"missing fields", func() error { return a.webSaveCustomProvider(context.Background(), "x", "", "", "", "") }},
		{"builtin shadow", func() error { return a.webSaveCustomProvider(context.Background(), "openai", "X", "http://x/v1", "m", "") }},
		{"invalid route", func() error { return a.webSaveCustomProvider(context.Background(), "Bad Route!", "X", "http://x/v1", "m", "") }},
		{"trailing dash", func() error { return a.webSaveCustomProvider(context.Background(), "bad-", "X", "http://x/v1", "m", "") }},
		{"delete builtin", func() error { return a.webDeleteCustomProvider(context.Background(), "deepseek") }},
		{"delete unknown", func() error { return a.webDeleteCustomProvider(context.Background(), "nope") }},
	}
	for _, tc := range cases {
		if err := tc.edit(); err == nil {
			t.Errorf("%s: want an error, got nil", tc.name)
		}
	}
	// Valid route characters are accepted.
	if err := a.webSaveCustomProvider(context.Background(), "my-llm-2", "X", "http://x/v1", "m", ""); err != nil {
		t.Errorf("valid route rejected: %v", err)
	}
}

// TestWebProvidersListsDormantBuiltins verifies M11 webProviders lists openai /
// anthropic even when their env key is absent (dormant, so 增加提供方 can add them),
// and the active deepseek stays configured.
func TestWebProvidersListsDormantBuiltins(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "env-key") // openai/anthropic keys absent
	a, _ := m11App(t)
	providers := a.webConfig()["providers"].([]map[string]any)

	seen := map[string]map[string]any{}
	for _, p := range providers {
		seen[p["id"].(string)] = p
	}
	for _, id := range []string{"deepseek", "openai", "anthropic"} {
		if _, ok := seen[id]; !ok {
			t.Errorf("built-in provider %q missing from webProviders", id)
		}
	}
	if seen["deepseek"]["registered"] != true || seen["deepseek"]["configured"] != true {
		t.Fatalf("deepseek should be registered+configured, got %#v", seen["deepseek"])
	}
	if seen["openai"]["registered"] != false || seen["openai"]["configured"] != false {
		t.Fatalf("openai should be dormant (not registered, not configured), got %#v", seen["openai"])
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
