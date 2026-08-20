// Package anthropic implements the llm.Provider for the Anthropic Messages
// API (M8-2b, dispatch-m8-2b §2): streaming SSE with tool use and thinking
// (reasoning) passthrough. Serialization follows dispatch-m8-2b §2.1 (system
// extraction, user/assistant/tool-result blocks, thinking→reasoning, tool_use
// input), and the SSE reader follows §2.2 (content_block_* / message_* /
// error events). The HTTP client semantics (x-api-key + anthropic-version
// headers, redirects blocked, ctx cancellation, bounded error body) reuse the
// M7 internal/web/deepseek.go Anthropic-compatible client. Credentials are
// env-only (ANTHROPIC_API_KEY, 纪律 6): the composition root passes the value
// in so this package stays testable.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"personal-agent/internal/llm"
)

const (
	// defaultBaseURL is the Anthropic Messages API base URL; "/messages" is
	// appended (dispatch-m8-2b §2.1, default https://api.anthropic.com/v1).
	defaultBaseURL = "https://api.anthropic.com/v1"
	// defaultModel is the default model when Config.Model is empty. Kept in
	// sync with config.DefaultAnthropicModel (dispatch-m8-2b §3).
	defaultModel = "claude-sonnet-4-5"
	// defaultMaxTokens is the response token budget (dispatch-m8-2b §2.1).
	defaultMaxTokens = 4096
	// apiVersion is the anthropic-version header (dispatch-m8-2b §2.1).
	apiVersion = "2023-06-01"
	// providerID is the stable provider id (dispatch-m8-2b §2.3).
	providerID = "anthropic"
	// maxErrorBody bounds the non-2xx error body read (dispatch-m8-2b §2.3,
	// 1 MiB).
	maxErrorBody = 1 << 20
	// noOutputPlaceholder is emitted for a user message whose content is empty
	// after conversion (Anthropic rejects empty content, dispatch-m8-2b §2.1
	// rule 5, 照 dsh 同款).
	noOutputPlaceholder = "(no output)"
)

// errRedirectDetected is returned by the CheckRedirect callback to turn
// "follow the redirect" into an error: any 3xx is never followed nor read
// (dispatch-m8-2b §2.3, mirroring M7 web/deepseek.go).
var errRedirectDetected = errors.New("anthropic: redirect not followed")

// Config configures the Anthropic provider. APIKey must come from the
// environment (ANTHROPIC_API_KEY only, 纪律 6).
type Config struct {
	BaseURL string // default https://api.anthropic.com/v1 ("/messages" appended)
	APIKey  string // ANTHROPIC_API_KEY value; empty means absent
	Model   string // default claude-sonnet-4-5
	// MaxTokens is the response token budget; <= 0 uses the default 4096
	// (advanced max_tokens/temperature/stop knobs are out of scope this
	// milestone, dispatch-m8-2b §1).
	MaxTokens int
	// HTTPClient is optional; defaults to http.DefaultClient. The provider
	// copies it with a no-redirect CheckRedirect, never mutating the caller's
	// shared client.
	HTTPClient *http.Client
}

// anthropicProvider is the llm.Provider implementing the Anthropic Messages
// API (dispatch-m8-2b §2.3).
type anthropicProvider struct {
	baseURL   string
	apiKey    string
	model     string
	maxTokens int
	client    *http.Client
}

// New returns an anthropicProvider with defaults applied (base URL, model,
// max_tokens).
func New(cfg Config) *anthropicProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &anthropicProvider{
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:    cfg.APIKey,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		client:    cfg.HTTPClient,
	}
}

// ID returns the stable provider id "anthropic" (dispatch-m8-2b §2.3).
func (p *anthropicProvider) ID() string { return providerID }

// Available reports whether the provider can be used: a cheap local check that
// never performs a network call — apiKey present and base URL parseable (same
// shape as deepseek.Client.Available / web.DeepSeekSearchProvider.Available,
// dispatch-m8-2b §2.3).
func (p *anthropicProvider) Available() bool {
	if p.apiKey == "" {
		return false
	}
	u, err := url.Parse(p.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	return true
}

// Stream starts a streaming Messages request and returns an incremental
// reader (D6). The request is serialized per dispatch-m8-2b §2.1, POSTed to
// {baseURL}/messages with the Anthropic headers, and the SSE response is
// decoded per §2.2. ctx cancellation runs through the HTTP request and the
// body reads.
func (p *anthropicProvider) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	body := requestBody{
		Model:     p.model,
		MaxTokens: p.maxTokens,
		System:    extractSystem(req.Messages),
		Messages:  toWireMessages(req.Messages),
		Tools:     toWireTools(req.Tools),
		Stream:    true,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "application/json")

	resp, err := p.doNoRedirect(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return newStreamReader(resp), nil
	}
	defer resp.Body.Close()
	detail := errorDetail(resp)
	if detail == "" {
		detail = resp.Status
	}
	return nil, fmt.Errorf("anthropic: provider error: %s", detail)
}

// doNoRedirect issues httpReq with a no-follow redirect policy (any 3xx is
// blocked at CheckRedirect and mapped to an error, mirroring M7
// web/deepseek.go). ctx cancellation is reported as a cancellation error.
func (p *anthropicProvider) doNoRedirect(req *http.Request) (*http.Response, error) {
	client := &http.Client{
		Transport: p.client.Transport,
		Jar:       p.client.Jar,
		Timeout:   p.client.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return errRedirectDetected
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errRedirectDetected) {
			return nil, fmt.Errorf("anthropic: redirect blocked (3xx not followed)")
		}
		if req.Context().Err() != nil {
			return nil, fmt.Errorf("anthropic: cancelled: %w", req.Context().Err())
		}
		return nil, fmt.Errorf("anthropic: request failed: %w", err)
	}
	return resp, nil
}

// errorDetail extracts the server-provided message from a non-2xx response
// (bounded 1 MiB read; shapes {"error":{"message":...}} or {"message":...},
// mirroring M7 web/deepseek.go anthropicErrorDetail). Empty string when it
// cannot be parsed.
func errorDetail(resp *http.Response) string {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return ""
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ""
	}
	if envelope.Error.Message != "" {
		return envelope.Error.Message
	}
	return envelope.Message
}

// —— wire shapes for the Messages API request body (dispatch-m8-2b §2.1) ——

// wireMessage is one entry of the "messages" array. Content is an ordered
// block list; blocks are map[string]any so thinking / tool_use / tool_result
// can carry their own shapes without a double-track type.
type wireMessage struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

// wireTool is one entry of the "tools" array.
type wireTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type requestBody struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    string        `json:"system,omitempty"` // extracted RoleSystem text
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools,omitempty"`
	Stream    bool          `json:"stream"`
}

// extractSystem joins every RoleSystem message's text into the top-level
// "system" field (dispatch-m8-2b §2.1 rule 1); system messages never enter
// the messages array.
func extractSystem(msgs []llm.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		if m.Role != llm.RoleSystem {
			continue
		}
		if t := m.Text(); t != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(t)
		}
	}
	return sb.String()
}

// toWireMessages serializes a chat history into the Messages API "messages"
// array (dispatch-m8-2b §2.1 rules 2–5):
//   - RoleSystem messages are extracted to the top-level system field
//     (extractSystem) and never enter the array;
//   - user messages become text blocks (M8-3 adds image blocks);
//   - assistant messages keep their block order — reasoning blocks become
//     thinking blocks before text blocks (dsh 范式), and ToolCalls become
//     tool_use blocks with the parsed arguments JSON;
//   - RoleTool messages (tool results) are grouped into a single user message
//     of tool_result blocks at their position in the sequence (consecutive
//     results merge into one message, dispatch-m8-2b §2.1 rule 4);
//   - a user message whose content is empty after conversion gets the
//     "(no output)" placeholder (Anthropic rejects empty content, rule 5).
func toWireMessages(msgs []llm.Message) []wireMessage {
	var out []wireMessage
	var pendingToolResults []map[string]any
	flushToolResults := func() {
		if len(pendingToolResults) == 0 {
			return
		}
		out = append(out, wireMessage{Role: "user", Content: pendingToolResults})
		pendingToolResults = nil
	}
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem:
			// Extracted to the top-level system field; not a wire message.
		case llm.RoleTool:
			pendingToolResults = append(pendingToolResults, map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Text(),
			})
		case llm.RoleUser:
			flushToolResults()
			out = append(out, wireMessage{Role: "user", Content: textBlocks(m.Content)})
		case llm.RoleAssistant:
			flushToolResults()
			out = append(out, wireMessage{Role: "assistant", Content: assistantBlocks(m)})
		}
	}
	flushToolResults()
	return out
}

// textBlocks converts a user message's content parts to text blocks
// (dispatch-m8-2b §2.1 rule 2: this milestone only text → {"type":"text"}).
// An empty result yields the "(no output)" placeholder (rule 5).
func textBlocks(blocks []llm.ContentBlock) []map[string]any {
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		if b.Kind == llm.BlockText {
			out = append(out, map[string]any{"type": "text", "text": b.Text})
		}
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"type": "text", "text": noOutputPlaceholder})
	}
	return out
}

// assistantBlocks serializes an assistant message's content in order
// (dispatch-m8-2b §2.1 rule 3, dsh 范式: reasoning before text):
// BlockReasoning → thinking, BlockText → text, then ToolCalls → tool_use.
func assistantBlocks(m llm.Message) []map[string]any {
	out := make([]map[string]any, 0, len(m.Content)+len(m.ToolCalls))
	for _, b := range m.Content {
		switch b.Kind {
		case llm.BlockReasoning:
			out = append(out, map[string]any{"type": "thinking", "thinking": b.Text})
		case llm.BlockText:
			out = append(out, map[string]any{"type": "text", "text": b.Text})
		}
	}
	for _, tc := range m.ToolCalls {
		out = append(out, map[string]any{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  tc.Name,
			"input": parseArguments(tc.Arguments),
		})
	}
	return out
}

// parseArguments unmarshals a tool call's raw JSON arguments into the tool_use
// "input" object (dispatch-m8-2b §2.1 rule 3). An empty argument string maps
// to an empty object (the no-arguments case); any parse failure or a
// non-object result falls back to {"_raw": <raw>} so the arguments are never
// silently dropped.
func parseArguments(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil || v == nil {
		return map[string]any{"_raw": raw}
	}
	return v
}

// toWireTools converts the tool schemas to the Messages API "tools" array
// (dispatch-m8-2b §2.1).
func toWireTools(tools []llm.ToolSchema) []wireTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]wireTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, wireTool{Name: t.Name, Description: t.Description, InputSchema: t.Parameters})
	}
	return out
}
