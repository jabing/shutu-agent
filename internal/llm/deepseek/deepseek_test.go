package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jabing/shutu-agent/internal/llm"
)

// newTestClient starts a fake DeepSeek endpoint and returns a Client pointed
// at it.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL, APIKey: "test-key"})
}

func sse(payloads ...string) string {
	var sb strings.Builder
	for _, p := range payloads {
		sb.WriteString("data: " + p + "\n\n")
	}
	return sb.String()
}

func TestStreamText(t *testing.T) {
	var gotBody map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sse(
			`{"choices":[{"delta":{"content":"Hel"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":"lo"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			"[DONE]",
		)))
	})

	reader, err := c.Stream(context.Background(), llm.ChatRequest{
		Model: "deepseek-v4-flash",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.Text("hi")}},
		},
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	var text strings.Builder
	var finish llm.StreamEvent
	for {
		ev, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if ev.Kind == llm.StreamTextDelta {
			text.WriteString(ev.Text)
		}
		if ev.Kind == llm.StreamFinish {
			finish = ev
		}
	}
	if text.String() != "Hello" {
		t.Fatalf("text = %q, want Hello", text.String())
	}
	if finish.FinishReason != "stop" {
		t.Fatalf("finish reason = %q", finish.FinishReason)
	}
	if len(finish.ToolCalls) != 0 {
		t.Fatalf("unexpected tool calls: %+v", finish.ToolCalls)
	}
	if gotBody["stream"] != true {
		t.Fatalf("stream flag = %v, want true", gotBody["stream"])
	}
	if gotBody["model"] != "deepseek-v4-flash" {
		t.Fatalf("model = %v", gotBody["model"])
	}
}

func TestStreamToolCalls(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sse(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_time","arguments":""}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"tz\":\"UTC\"}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			"[DONE]",
		)))
	})

	reader, err := c.Stream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var finish llm.StreamEvent
	for {
		ev, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if ev.Kind == llm.StreamFinish {
			finish = ev
		}
	}
	if len(finish.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v, want 1", finish.ToolCalls)
	}
	call := finish.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "get_time" {
		t.Fatalf("call = %+v", call)
	}
	if call.Arguments != `{"tz":"UTC"}` {
		t.Fatalf("arguments = %q", call.Arguments)
	}
	if finish.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q", finish.FinishReason)
	}
}

func TestStreamSendsToolsAndToolMessage(t *testing.T) {
	var gotBody map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]")))
	})

	_, err := c.Stream(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.ContentBlock{llm.Text("sys")}},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "get_time", Arguments: "{}"}}},
			{Role: llm.RoleTool, ToolCallID: "c1", Content: []llm.ContentBlock{llm.Text("out")}},
		},
		Tools: []llm.ToolSchema{{Name: "get_time", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v", gotBody["messages"])
	}
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", gotBody["tools"])
	}
}

func TestStreamHTTPError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"bad key"}`))
	})
	if _, err := c.Stream(context.Background(), llm.ChatRequest{}); err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestStreamTruncatedMissingDone(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"x"},"finish_reason":null}]}` + "\n\n"))
	})
	reader, err := c.Stream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	_, err = reader.Next() // content delta
	if err != nil {
		t.Fatalf("first next: %v", err)
	}
	_, err = reader.Next() // EOF without [DONE]
	if err == nil {
		t.Fatal("expected truncated-stream error")
	}
}

func TestStreamMalformedPayload(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {not json}` + "\n\n"))
	})
	reader, err := c.Stream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, err := reader.Next(); err == nil {
		t.Fatal("expected malformed payload error")
	}
}

// newRetryClient returns a Client with fast, zero-delay retries for tests.
func newRetryClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	c := newTestClient(t, handler)
	c.maxRetries = 2
	c.backoff = func(int) time.Duration { return 0 }
	return c
}

// TestStreamRetries429ThenSucceeds verifies the 429→200 backoff retry path
// (dispatch-m2 §5; acceptance requires an httptest 429→200 case).
func TestStreamRetries429ThenSucceeds(t *testing.T) {
	var calls int
	c := newRetryClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sse(
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			"[DONE]",
		)))
	})

	reader, err := c.Stream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var text strings.Builder
	for {
		ev, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if ev.Kind == llm.StreamTextDelta {
			text.WriteString(ev.Text)
		}
	}
	if text.String() != "ok" {
		t.Fatalf("text = %q, want ok", text.String())
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (429 then 200)", calls)
	}
}

// TestStreamDoesNotRetry4xx verifies auth/4xx errors are returned immediately
// without retry (dispatch-m2 §5).
func TestStreamDoesNotRetry4xx(t *testing.T) {
	var calls int
	c := newRetryClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	})
	if _, err := c.Stream(context.Background(), llm.ChatRequest{}); err == nil {
		t.Fatal("expected error on 401")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (4xx must not retry)", calls)
	}
}

// TestStreamRetries5xxExhausted verifies 5xx is retried maxRetries times and
// then the last error is returned.
func TestStreamRetries5xxExhausted(t *testing.T) {
	var calls int
	c := newRetryClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.Stream(context.Background(), llm.ChatRequest{}); err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (initial + 2 retries)", calls)
	}
}

// TestStreamRetryAbortsOnCancellation verifies a cancelled context aborts the
// backoff wait instead of sleeping out the full delay.
func TestStreamRetryAbortsOnCancellation(t *testing.T) {
	var calls int
	ctx, cancel := context.WithCancel(context.Background())
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		cancel() // cancel right after the first 429 so the backoff aborts
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c.maxRetries = 5
	c.backoff = func(int) time.Duration { return time.Hour } // would hang without cancellation
	if _, err := c.Stream(ctx, llm.ChatRequest{}); err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (retry aborted by cancellation)", calls)
	}
}

// TestID returns the stable provider id (M8-2, dispatch-m8-2 §3).
func TestID(t *testing.T) {
	if got := New(Config{}).ID(); got != "deepseek-official" {
		t.Fatalf("ID = %q, want deepseek", got)
	}
}

// TestAvailable verifies the cheap local availability check (dispatch-m8-2 §3):
// key present + base URL parseable, with no network call. A missing key or an
// unparseable base URL makes the provider unavailable.
func TestAvailable(t *testing.T) {
	// Key present, valid base URL (explicit and defaulted).
	if !New(Config{APIKey: "k"}).Available() {
		t.Error("key + default base URL must be available")
	}
	if !New(Config{APIKey: "k", BaseURL: "https://api.deepseek.com"}).Available() {
		t.Error("key + explicit base URL must be available")
	}

	// Missing key → unavailable regardless of base URL.
	if New(Config{}).Available() {
		t.Error("empty key must be unavailable")
	}
	if New(Config{BaseURL: "https://api.deepseek.com"}).Available() {
		t.Error("empty key with a base URL must be unavailable")
	}

	// Invalid / unparseable base URL → unavailable even with a key.
	for _, bad := range []string{":", "://", "not a url", "http://"} {
		if New(Config{APIKey: "k", BaseURL: bad}).Available() {
			t.Errorf("base URL %q must be unavailable", bad)
		}
	}
}

// TestToWireMessageReasoning verifies the M8 wire contract: an assistant
// message whose content carries a reasoning block serializes the joined
// reasoning text on the OpenAI-compatible reasoning_content field, and
// non-assistant messages never carry it (dispatch-m8 §3).
func TestToWireMessageReasoning(t *testing.T) {
	asst := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			{Kind: llm.BlockReasoning, Text: "step by step, "},
			{Kind: llm.BlockReasoning, Text: "then answer"},
			llm.Text("Here is the answer."),
		},
	}
	w, err := toWireMessage(asst)
	if err != nil {
		t.Fatalf("toWireMessage: %v", err)
	}
	if w.ReasoningContent != "step by step, then answer" {
		t.Fatalf("reasoning_content = %q, want the joined reasoning text", w.ReasoningContent)
	}
	if w.Content != "Here is the answer." {
		t.Fatalf("content = %q, want the text blocks only (no reasoning)", w.Content)
	}

	// A user message with a reasoning block must NOT carry reasoning_content.
	usr := llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{{Kind: llm.BlockReasoning, Text: "x"}, llm.Text("hi")},
	}
	wu, err := toWireMessage(usr)
	if err != nil {
		t.Fatalf("toWireMessage: %v", err)
	}
	if wu.ReasoningContent != "" {
		t.Fatalf("user message must not carry reasoning_content, got %q", wu.ReasoningContent)
	}
	if wu.Content != "hi" {
		t.Fatalf("user content = %q, want hi", wu.Content)
	}
}

// TestToWireMessageReasoningOnTheWire verifies reasoning_content actually lands
// in the request body sent to the endpoint (httptest asserting the JSON body,
// dispatch-m8 §6).
func TestToWireMessageReasoningOnTheWire(t *testing.T) {
	var gotBody map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]")))
	})
	_, err := c.Stream(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
				{Kind: llm.BlockReasoning, Text: "think hard"},
				llm.Text("answer"),
			}},
		},
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", gotBody["messages"])
	}
	first, _ := msgs[0].(map[string]any)
	if first["reasoning_content"] != "think hard" {
		t.Fatalf("wire message = %+v, want reasoning_content=think hard", first)
	}
	if first["content"] != "answer" {
		t.Fatalf("wire message content = %+v, want answer", first)
	}
}

// TestStreamReasoningDelta verifies the SSE reader surfaces reasoning_content
// deltas as StreamReasoningDelta and accumulates them onto StreamFinish.Reasoning
// (dispatch-m8 §3/§6).
func TestStreamReasoningDelta(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sse(
			`{"choices":[{"delta":{"reasoning_content":"Let me"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"reasoning_content":" think"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":"Done."},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			"[DONE]",
		)))
	})
	reader, err := c.Stream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var reasoning, text strings.Builder
	var finish llm.StreamEvent
	for {
		ev, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		switch ev.Kind {
		case llm.StreamReasoningDelta:
			reasoning.WriteString(ev.Text)
		case llm.StreamTextDelta:
			text.WriteString(ev.Text)
		case llm.StreamFinish:
			finish = ev
		}
	}
	if reasoning.String() != "Let me think" {
		t.Fatalf("reasoning deltas = %q, want %q", reasoning.String(), "Let me think")
	}
	if text.String() != "Done." {
		t.Fatalf("text deltas = %q, want Done.", text.String())
	}
	if finish.Reasoning != "Let me think" {
		t.Fatalf("finish.Reasoning = %q, want the accumulated reasoning", finish.Reasoning)
	}
	if finish.FinishReason != "stop" {
		t.Fatalf("finish reason = %q", finish.FinishReason)
	}
}

// TestStreamNoReasoningRegression verifies a stream without any
// reasoning_content behaves exactly as before: only text deltas and a finish
// with empty Reasoning (M8 regression guard).
func TestStreamNoReasoningRegression(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sse(
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			"[DONE]",
		)))
	})
	reader, err := c.Stream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var text strings.Builder
	var finish llm.StreamEvent
	for {
		ev, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if ev.Kind == llm.StreamTextDelta {
			text.WriteString(ev.Text)
		}
		if ev.Kind == llm.StreamFinish {
			finish = ev
		}
	}
	if text.String() != "ok" {
		t.Fatalf("text = %q, want ok", text.String())
	}
	if finish.Reasoning != "" {
		t.Fatalf("finish.Reasoning = %q, want empty without reasoning deltas", finish.Reasoning)
	}
}
