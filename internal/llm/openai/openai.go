// Package openai implements the llm.Provider for OpenAI-compatible SSE
// endpoints (M8-2, dispatch-m8-2 §4). It deliberately reuses the deepseek
// client: the DeepSeek API is OpenAI compatible — identical wire, including
// the reasoning_content passthrough, which OpenAI-compatible reasoning models
// also use, so the M8 reasoning semantics hold naturally. There is zero new
// serialization/parsing code here.
package openai

import (
	"context"

	"github.com/jabing/shutu-agent/internal/llm"
	"github.com/jabing/shutu-agent/internal/llm/deepseek"
)

const (
	// DefaultBaseURL is the OpenAI-compatible base URL when llm.openai.base_url
	// is empty (dispatch-m8-2 §4).
	DefaultBaseURL = "https://api.openai.com/v1"
	// DefaultModel is the chat model when llm.openai.model is empty
	// (dispatch-m8-2 §4).
	DefaultModel = "gpt-4o-mini"

	// providerID is the stable provider id of this adapter.
	providerID = "openai"
)

// Config configures the OpenAI-compatible provider. APIKey must come from the
// environment (OPENAI_API_KEY only, design.md §6).
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	// SupportsImages is the model's input-modality capability declaration,
	// passed from config llm.model_input_modalities by the composition root
	// (dispatch-m8-3b §4.1). false (the default) means an image request fails
	// closed inside the shared OpenAI-compatible client.
	SupportsImages bool
	// MaxRequestImageBytes is the per-request image byte budget (dispatch-m8-3b
	// §4.1); non-positive uses the default 20MiB.
	MaxRequestImageBytes int
}

// openaiProvider is an llm.Provider delegating the OpenAI-compatible SSE wire
// to a shared deepseek.Client (dispatch-m8-2 §4: 零新 wire 代码).
type openaiProvider struct {
	c *deepseek.Client
}

// New returns an openaiProvider built on a deepseek.Client with OpenAI-compatible
// defaults applied (base_url https://api.openai.com/v1, model gpt-4o-mini,
// both configurable). Stream/Available delegate to the shared client.
func New(cfg Config) *openaiProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	return &openaiProvider{
		c: deepseek.New(deepseek.Config{
			BaseURL:              cfg.BaseURL,
			APIKey:               cfg.APIKey,
			Model:                cfg.Model,
			SupportsImages:       cfg.SupportsImages,
			MaxRequestImageBytes: cfg.MaxRequestImageBytes,
		}),
	}
}

// ID returns the stable provider id "openai" (dispatch-m8-2 §4).
func (p *openaiProvider) ID() string { return providerID }

// Available reports whether the provider can be used: a cheap local check (API
// key present and base URL parseable) that never performs a network call —
// exactly the deepseek.Client.Available semantics, which already validates the
// key and the (defaulted, never empty) base URL (dispatch-m8-2 §4).
func (p *openaiProvider) Available() bool {
	return p.c.Available()
}

// Stream delegates to the shared OpenAI-compatible SSE implementation
// (dispatch-m8-2 §4).
func (p *openaiProvider) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	return p.c.Stream(ctx, req)
}
