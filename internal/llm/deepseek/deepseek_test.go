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

	"personal-agent/internal/llm"
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
		Model: "deepseek-chat",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "hi"},
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
	if gotBody["model"] != "deepseek-chat" {
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
			{Role: llm.RoleSystem, Content: "sys"},
			{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "get_time", Arguments: "{}"}}},
			{Role: llm.RoleTool, ToolCallID: "c1", Content: "out"},
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
