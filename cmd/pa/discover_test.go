// discover_test.go — the M11-pi-ai model discovery ("获取可用模型") tests: a
// directory route answers from its catalog with no network call; a
// hand-declared OpenAI-compatible endpoint is interrogated via GET {base}/models
// and its id/name/capacities are parsed; a non-listable protocol (anthropic,
// gemini) is rejected with a hand-entry fallback; an unreadable reply is a
// failure, not an empty list.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// discoverApp builds an app with no registry dependency — discovery never needs
// the LLM registry, only the store-free directory accessors.
func discoverApp(t *testing.T) *app {
	t.Helper()
	return &app{}
}

// modelsHandler returns an OpenAI-compatible GET /models responder driven by
// the returned server pointer: url is the endpoint, auth the bearer it saw.
func startModelsServer(t *testing.T) (*httptest.Server, *string, *string) {
	t.Helper()
	var url string
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		url = r.URL.Path
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"gpt-4o-mini","object":"model","created":0},
			{"id":"gpt-4o","name":"GPT-4o","context_window":128000,"max_output_tokens":16384},
			{"id":"","name":"blank id should be skipped"},
			{"id":"gateway/llama","context_length":8192,"max_tokens":4096}
		]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &url, &auth
}

func TestDiscoverCatalogRoute(t *testing.T) {
	a := discoverApp(t)
	models, err := a.webDiscoverModels(context.Background(), discoverRequest{Provider: "groq"})
	if err != nil {
		t.Fatalf("catalog route probe: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("catalog route should answer its candidates without a network call")
	}
	if models[0].ID == "" {
		t.Fatal("candidate must carry an id")
	}
	// Unknown provider + no base URL is a user error.
	if _, err := a.webDiscoverModels(context.Background(), discoverRequest{Provider: "nope"}); err == nil {
		t.Fatal("unknown route with no base URL should error")
	}
}

func TestDiscoverListableProtocols(t *testing.T) {
	srv, url, auth := startModelsServer(t)
	a := discoverApp(t)
	models, err := a.webDiscoverModels(context.Background(), discoverRequest{
		BaseURL: srv.URL, Protocol: "openai-completions", APIKey: "k-abc",
	})
	if err != nil {
		t.Fatalf("openai-completions probe: %v", err)
	}
	if *url != "/models" {
		t.Fatalf("listed path = %q, want /models", *url)
	}
	if *auth != "Bearer k-abc" {
		t.Fatalf("authorization = %q, want Bearer k-abc", *auth)
	}
	// The blank-id entry is skipped; the rest are parsed in order.
	if len(models) != 3 {
		t.Fatalf("got %d models, want 3 (blank id skipped): %#v", len(models), models)
	}
	if models[0].ID != "gpt-4o-mini" {
		t.Fatalf("first id = %q", models[0].ID)
	}
	if models[1].ID != "gpt-4o" || models[1].Name != "GPT-4o" || models[1].ContextWindow != 128000 || models[1].MaxTokens != 16384 {
		t.Fatalf("gpt-4o capacities = %#v", models[1])
	}
	if models[2].ID != "gateway/llama" || models[2].ContextWindow != 8192 || models[2].MaxTokens != 4096 {
		t.Fatalf("gateway/llama capacities = %#v", models[2])
	}

	// openai-responses is listable too.
	if _, err := a.webDiscoverModels(context.Background(), discoverRequest{BaseURL: srv.URL, Protocol: "openai-responses"}); err != nil {
		t.Fatalf("openai-responses probe: %v", err)
	}
}

func TestDiscoverUnsupportedProtocols(t *testing.T) {
	a := discoverApp(t)
	for _, protocol := range []string{"anthropic-messages", "google-generative-ai"} {
		_, err := a.webDiscoverModels(context.Background(), discoverRequest{BaseURL: "https://x.example", Protocol: protocol})
		if err == nil {
			t.Fatalf("%s should be rejected as non-listable", protocol)
		}
		if !strings.Contains(err.Error(), "无模型列表可读") {
			t.Fatalf("%s error = %v, want hand-entry fallback", protocol, err)
		}
	}
	// Empty protocol defaults to openai-completions → listable, so a reachable
	// endpoint is interrogated (reachability handled by the caller's client).
}

func TestDiscoverFailures(t *testing.T) {
	a := discoverApp(t)
	// Non-JSON reply.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	t.Cleanup(srv.Close)
	if _, err := a.webDiscoverModels(context.Background(), discoverRequest{BaseURL: srv.URL}); err == nil {
		t.Fatal("non-JSON reply should error")
	}
	srv.Close()

	// HTTP error status.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv2.Close)
	_, err := a.webDiscoverModels(context.Background(), discoverRequest{BaseURL: srv2.URL, APIKey: "bad"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("401 should surface, got %v", err)
	}
	srv2.Close()

	// Unreachable endpoint.
	if _, err := a.webDiscoverModels(context.Background(), discoverRequest{BaseURL: "http://127.0.0.1:1"}); err == nil {
		t.Fatal("unreachable endpoint should error")
	}
}

func TestDiscoverOverflowRejected(t *testing.T) {
	// A reply over the 4MiB bound is refused (dsh MAX_RESPONSE_BYTES).
	big := `{"data":[{"id":"` + strings.Repeat("x", discoverMaxResponseBytes) + `"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(big))
	}))
	t.Cleanup(srv.Close)
	a := discoverApp(t)
	if _, err := a.webDiscoverModels(context.Background(), discoverRequest{BaseURL: srv.URL}); err == nil {
		t.Fatal("oversize reply should be rejected")
	}
}

// TestDiscoverCapacityParsing guards the number handling: capacities are read
// only from positive integers (dsh capacity()).
func TestDiscoverCapacityParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"a","context_window":0,"max_output_tokens":-1},
			{"id":"b","context_window":128000}
		]}`))
	}))
	t.Cleanup(srv.Close)
	a := discoverApp(t)
	models, err := a.webDiscoverModels(context.Background(), discoverRequest{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models", len(models))
	}
	if models[0].ContextWindow != 0 || models[0].MaxTokens != 0 {
		t.Fatalf("non-positive capacities must read as 0, got %#v", models[0])
	}
	if models[1].ContextWindow != 128000 {
		t.Fatalf("context_window = %d", models[1].ContextWindow)
	}
}

// TestDiscoverWirePayload ensures the JSON wire tags survive round-tripping.
func TestDiscoverWirePayload(t *testing.T) {
	var req discoverRequest
	if err := json.Unmarshal([]byte(`{"provider":"x","base_url":"http://e","protocol":"openai-completions","api_key":"k"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Provider != "x" || req.BaseURL != "http://e" || req.Protocol != "openai-completions" || req.APIKey != "k" {
		t.Fatalf("wire decode = %#v", req)
	}
}
