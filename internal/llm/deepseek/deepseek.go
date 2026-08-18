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

	"personal-agent/internal/llm"
)

const (
	defaultBaseURL = "https://api.deepseek.com"
	defaultModel   = "deepseek-chat"
)

// Config configures the DeepSeek adapter. APIKey must come from the
// environment (design.md §6: keys never enter code, config, or logs).
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client // optional; defaults to http.DefaultClient
}

// Client is a DeepSeek LLM adapter.
type Client struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// New returns a Client with defaults applied (base URL, model).
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
	return &Client{
		baseURL: strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		client:  cfg.HTTPClient,
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("deepseek: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("deepseek: request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("deepseek: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return &streamReader{
		dec:       newSSEDecoder(resp.Body),
		resp:      resp,
		toolIndex: map[int]int{},
	}, nil
}
