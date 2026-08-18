// Package deepseek implements the llm.LLM adapter for the DeepSeek chat
// completions API (OpenAI-compatible, SSE streaming). Design.md §6: the
// default provider, base_url=https://api.deepseek.com; streaming is a
// first-class requirement (D6).
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"personal-agent/internal/llm"
)

const (
	defaultBaseURL    = "https://api.deepseek.com"
	defaultModel      = "deepseek-chat"
	defaultMaxRetries = 2
	// defaultBackoffBase is the first backoff delay; each later attempt doubles
	// it, capped at maxBackoff.
	defaultBackoffBase = 500 * time.Millisecond
	maxBackoff         = 8 * time.Second
)

// Config configures the DeepSeek adapter. APIKey must come from the
// environment (design.md §6: keys never enter code, config, or logs).
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client // optional; defaults to http.DefaultClient
	// MaxRetries is how many times a transient failure (network error, HTTP
	// 429, or HTTP 5xx) is retried with backoff before returning the error.
	// Zero (the default) uses 2. 4xx errors, including auth failures, are
	// never retried (dispatch-m2 §5).
	MaxRetries int
	// Backoff returns the delay before retry attempt n (1-based). Nil uses an
	// exponential schedule (500ms, 1s, 2s, ... capped at 8s).
	Backoff func(attempt int) time.Duration
}

// Client is a DeepSeek LLM adapter.
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	client     *http.Client
	maxRetries int
	backoff    func(attempt int) time.Duration
}

// New returns a Client with defaults applied (base URL, model, retries).
func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = defaultMaxRetries
	}
	backoff := cfg.Backoff
	if backoff == nil {
		backoff = exponentialBackoff
	}
	return &Client{
		baseURL:    strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		client:     cfg.HTTPClient,
		maxRetries: cfg.MaxRetries,
		backoff:    backoff,
	}
}

// wire message/tool shapes for the OpenAI-compatible request body.

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
}

type wireToolCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type chatBody struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Tools    []wireTool    `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
}

func toWireMessage(m llm.Message) wireMessage {
	w := wireMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
	for _, tc := range m.ToolCalls {
		wtc := wireToolCall{ID: tc.ID, Type: "function"}
		wtc.Function.Name = tc.Name
		wtc.Function.Arguments = tc.Arguments
		w.ToolCalls = append(w.ToolCalls, wtc)
	}
	return w
}

func toWireTool(t llm.ToolSchema) wireTool {
	wt := wireTool{Type: "function"}
	wt.Function.Name = t.Name
	wt.Function.Description = t.Description
	wt.Function.Parameters = t.Parameters
	return wt
}

// Stream starts a streaming chat request and returns an incremental reader.
// Transient failures (network errors, HTTP 429, HTTP 5xx) are retried with
// backoff up to maxRetries; the context is honored both by the HTTP request
// and between attempts. 4xx errors are returned immediately (dispatch-m2 §5).
func (c *Client) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	body := chatBody{Model: c.model, Stream: true}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, toWireMessage(m))
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, toWireTool(t))
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("deepseek: marshal request: %w", err)
	}

	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("deepseek: cancelled: %w", err)
		}
		reader, retryable, err := c.streamOnce(ctx, payload)
		if err == nil {
			return reader, nil
		}
		if !retryable || attempt >= c.maxRetries {
			return nil, err
		}
		if err := sleepCtx(ctx, c.backoff(attempt+1)); err != nil {
			return nil, fmt.Errorf("deepseek: retry aborted: %w", err)
		}
	}
}

// streamOnce performs a single HTTP attempt. On success it returns the
// streamReader. On failure it returns (nil, retryable, err): retryable is true
// for network failures, HTTP 429, and HTTP 5xx; everything else (4xx) is not.
func (c *Client) streamOnce(ctx context.Context, payload []byte) (llm.StreamReader, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, false, fmt.Errorf("deepseek: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		// Network-level failure: retryable.
		return nil, true, fmt.Errorf("deepseek: request failed: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return &streamReader{
			dec:       newSSEDecoder(resp.Body),
			resp:      resp,
			toolIndex: map[int]int{},
		}, false, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	err = fmt.Errorf("deepseek: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError
	return nil, retryable, err
}

// sleepCtx waits d, aborting early when ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// exponentialBackoff returns 500ms * 2^(attempt-1), capped at maxBackoff.
func exponentialBackoff(attempt int) time.Duration {
	d := defaultBackoffBase << (attempt - 1)
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}
