package session

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"personal-agent/internal/llm"
)

func TestAppendAssignsSeqAndType(t *testing.T) {
	l := New()
	ev, err := l.Append(EventUserMessage, NewUserMessage("hello"))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if ev.Seq != 1 || ev.Type != EventUserMessage {
		t.Fatalf("seq/type = %d/%q, want 1/%q", ev.Seq, ev.Type, EventUserMessage)
	}
	var d userMessageData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Text != "hello" {
		t.Fatalf("text = %q, want hello", d.Text)
	}
	ev2, _ := l.Append(EventAssistantChunk, NewAssistantChunk("hi"))
	if ev2.Seq != 2 {
		t.Fatalf("seq = %d, want 2", ev2.Seq)
	}
}

func TestEventsSnapshotIsolation(t *testing.T) {
	l := New()
	l.Append(EventUserMessage, NewUserMessage("a"))
	snap := l.Events()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	l.Append(EventUserMessage, NewUserMessage("b"))
	if len(snap) != 1 {
		t.Fatalf("snapshot mutated: len = %d, want 1", len(snap))
	}
	if len(l.Events()) != 2 {
		t.Fatalf("log len = %d, want 2", len(l.Events()))
	}
}

func TestDeriveHistoryBasicConversation(t *testing.T) {
	l := New()
	l.Append(EventUserMessage, NewUserMessage("what time is it"))
	l.Append(EventAssistantChunk, NewAssistantChunk("Let "))
	l.Append(EventAssistantChunk, NewAssistantChunk("me check"))
	l.Append(EventAssistantMessage, NewAssistantMessage("Let me check", nil, "stop"))

	msgs := l.DeriveHistory()
	if len(msgs) != 2 {
		t.Fatalf("derived %d messages, want 2", len(msgs))
	}
	if msgs[0].Role != llm.RoleUser || msgs[0].Content != "what time is it" {
		t.Fatalf("msg0 = %+v", msgs[0])
	}
	// chunks fold away; the authoritative assistant/message wins
	if msgs[1].Role != llm.RoleAssistant || msgs[1].Content != "Let me check" {
		t.Fatalf("msg1 = %+v", msgs[1])
	}
}

func TestDeriveHistoryToolRoundTrip(t *testing.T) {
	l := New()
	l.Append(EventUserMessage, NewUserMessage("read the file"))
	l.Append(EventAssistantMessage, NewAssistantMessage("", []llm.ToolCall{
		{ID: "call_1", Name: "read_file", Arguments: `{"path":"/tmp/x"}`},
	}, "tool_calls"))
	l.Append(EventToolResult, NewToolResult("call_1", "read_file", "file contents"))

	msgs := l.DeriveHistory()
	if len(msgs) != 3 {
		t.Fatalf("derived %d messages, want 3", len(msgs))
	}
	asst := msgs[1]
	if asst.Role != llm.RoleAssistant || len(asst.ToolCalls) != 1 {
		t.Fatalf("assistant msg = %+v", asst)
	}
	if asst.ToolCalls[0].ID != "call_1" || asst.ToolCalls[0].Name != "read_file" {
		t.Fatalf("tool call = %+v", asst.ToolCalls[0])
	}
	tool := msgs[2]
	if tool.Role != llm.RoleTool || tool.ToolCallID != "call_1" || tool.Content != "file contents" {
		t.Fatalf("tool msg = %+v", tool)
	}
}

func TestDeriveHistoryToolErrorBecomesToolMessage(t *testing.T) {
	l := New()
	l.Append(EventUserMessage, NewUserMessage("do it"))
	l.Append(EventAssistantMessage, NewAssistantMessage("", []llm.ToolCall{
		{ID: "call_2", Name: "read_file", Arguments: `{"path":"/nope"}`},
	}, "tool_calls"))
	l.Append(EventToolError, NewToolError("call_2", "read_file", "no such file"))

	msgs := l.DeriveHistory()
	if len(msgs) != 3 {
		t.Fatalf("derived %d messages, want 3", len(msgs))
	}
	tool := msgs[2]
	if tool.Role != llm.RoleTool || tool.ToolCallID != "call_2" {
		t.Fatalf("tool msg = %+v", tool)
	}
	if tool.Content != "Error: no such file" {
		t.Fatalf("tool error content = %q", tool.Content)
	}
}

func TestAppendAssignsEventVersion(t *testing.T) {
	l := New()
	ev, err := l.Append(EventUserMessage, NewUserMessage("hi"))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if ev.Version != EventVersion {
		t.Fatalf("version = %d, want %d", ev.Version, EventVersion)
	}
}

// TestRestoreRebuildsLogAndContinuesSeq verifies startup replay rebuilds the
// log from persisted events and the next Append continues the sequence.
func TestRestoreRebuildsLogAndContinuesSeq(t *testing.T) {
	stored := []Event{
		{Seq: 1, Type: EventUserMessage, At: time.Now().UTC(), Version: 1, Data: json.RawMessage(`{"text":"hello"}`)},
		{Seq: 2, Type: EventAssistantMessage, At: time.Now().UTC(), Version: 1, Data: json.RawMessage(`{"text":"hi"}`)},
	}
	l := New()
	if err := l.Restore(stored); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(l.Events()) != 2 {
		t.Fatalf("events = %d, want 2", len(l.Events()))
	}
	ev, err := l.Append(EventUserMessage, NewUserMessage("next"))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if ev.Seq != 3 {
		t.Fatalf("seq = %d, want 3 (continues after restored seq 2)", ev.Seq)
	}
	msgs := l.DeriveHistory()
	if len(msgs) != 3 {
		t.Fatalf("derived %d messages, want 3", len(msgs))
	}
}

func TestRestoreRejectsNonMonotonicSeq(t *testing.T) {
	l := New()
	if err := l.Restore([]Event{
		{Seq: 2, Type: EventUserMessage},
		{Seq: 1, Type: EventUserMessage},
	}); err == nil {
		t.Fatal("expected non-monotonic seq error")
	}
}

// TestAppendSinkPersistsEvent verifies the durable sink receives every
// committed event (dispatch-m2: 事件追加写入).
func TestAppendSinkPersistsEvent(t *testing.T) {
	var got []Event
	l := New()
	l.SetSink(func(ev Event) error {
		got = append(got, ev)
		return nil
	})
	if _, err := l.Append(EventUserMessage, NewUserMessage("hi")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if len(got) != 1 || got[0].Type != EventUserMessage {
		t.Fatalf("sink got %+v", got)
	}
}

// TestAppendSinkErrorRollsBack verifies a failing sink rolls the event back
// out of the log and fails the Append, so memory never drifts from disk.
func TestAppendSinkErrorRollsBack(t *testing.T) {
	l := New()
	l.SetSink(func(Event) error { return errors.New("disk full") })
	if _, err := l.Append(EventUserMessage, NewUserMessage("hi")); err == nil {
		t.Fatal("expected sink error")
	}
	if len(l.Events()) != 0 {
		t.Fatalf("log has %d events after failed persist, want 0", len(l.Events()))
	}
}

// TestRestoreDoesNotInvokeSink verifies replay never writes back through the
// sink (loading is not appending).
func TestRestoreDoesNotInvokeSink(t *testing.T) {
	var calls int
	l := New()
	l.SetSink(func(Event) error { calls++; return nil })
	if err := l.Restore([]Event{{Seq: 1, Type: EventUserMessage, Version: 1}}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if calls != 0 {
		t.Fatalf("sink invoked %d times during restore, want 0", calls)
	}
}
