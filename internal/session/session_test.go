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
	l.Append(EventToolResult, NewToolResult("call_1", "read_file", "file contents", nil))

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

// TestNextSeq verifies NextSeq reports the seq the next Append will assign
// (M3 spill naming depends on it).
func TestNextSeq(t *testing.T) {
	l := New()
	if got := l.NextSeq(); got != 1 {
		t.Fatalf("NextSeq = %d, want 1", got)
	}
	l.Append(EventUserMessage, NewUserMessage("a"))
	l.Append(EventUserMessage, NewUserMessage("b"))
	if got := l.NextSeq(); got != 3 {
		t.Fatalf("NextSeq = %d, want 3", got)
	}
}

// TestKBRecallEventAppendsAndReplays verifies the M4a kb/recall event type
// (design.md §3 / D3): it appends with the right vocabulary, survives the
// JSON round-trip and restart replay, and stays opaque to history derivation
// (the recall is injected into context by the caller, so it never mutates the
// derived history).
func TestKBRecallEventAppendsAndReplays(t *testing.T) {
	var persisted []Event
	l := New()
	l.SetSink(func(ev Event) error {
		persisted = append(persisted, ev)
		return nil
	})
	if _, err := l.Append(EventKBRecall, NewKBRecall("架构决策", []RecallHit{
		{ID: "kb-1", Title: "架构决策记录", Snippet: "我们决定采用 SQLite FTS5…", Type: "decision", Tags: []string{"架构", "决策"}, Source: "session:s1:turn:1", Score: 0.9},
	})); err != nil {
		t.Fatalf("append: %v", err)
	}
	ev := l.Events()[0]
	if ev.Type != EventKBRecall || ev.Version != EventVersion {
		t.Fatalf("event = %+v, want type %q version %d", ev, EventKBRecall, EventVersion)
	}
	var d kbRecallData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Query != "架构决策" || len(d.Hits) != 1 || d.Hits[0].ID != "kb-1" || d.Hits[0].Title != "架构决策记录" {
		t.Fatalf("payload = %+v", d)
	}
	if len(persisted) != 1 || persisted[0].Type != EventKBRecall {
		t.Fatalf("sink (append path) = %+v", persisted)
	}
	// restart replay: a fresh log rebuilt from what was persisted still sees
	// the event, and deriving history treats it as opaque data.
	fresh := New()
	if err := fresh.Restore(persisted); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := fresh.Events()[0]; got.Type != EventKBRecall {
		t.Fatalf("replayed type = %q", got.Type)
	}
	if msgs := fresh.DeriveHistory(); len(msgs) != 0 {
		t.Fatalf("kb/recall must not derive into messages: %+v", msgs)
	}
}

// TestKBAddEventAppendsAndReplays verifies the M4b kb/add event type
// (dispatch-m4b §3 / D3): explicit knowledge writes append a bounded summary
// event that survives the JSON round-trip and restart replay, and stays opaque
// to history derivation (an explicit write is a log fact, not conversation).
func TestKBAddEventAppendsAndReplays(t *testing.T) {
	var persisted []Event
	l := New()
	l.SetSink(func(ev Event) error {
		persisted = append(persisted, ev)
		return nil
	})
	if _, err := l.Append(EventKBAdd, NewKBAdd("kb-9", "架构决策记录", "decision", []string{"架构"}, "manual:abc", 1)); err != nil {
		t.Fatalf("append: %v", err)
	}
	ev := l.Events()[0]
	if ev.Type != EventKBAdd || ev.Version != EventVersion {
		t.Fatalf("event = %+v, want type %q version %d", ev, EventKBAdd, EventVersion)
	}
	var d kbAddData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.EntryID != "kb-9" || d.Title != "架构决策记录" || d.Type != "decision" || d.Version != 1 || len(d.Tags) != 1 {
		t.Fatalf("payload = %+v", d)
	}
	if len(persisted) != 1 || persisted[0].Type != EventKBAdd {
		t.Fatalf("sink (append path) = %+v", persisted)
	}
	fresh := New()
	if err := fresh.Restore(persisted); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := fresh.Events()[0]; got.Type != EventKBAdd {
		t.Fatalf("replayed type = %q", got.Type)
	}
	if msgs := fresh.DeriveHistory(); len(msgs) != 0 {
		t.Fatalf("kb/add must not derive into messages: %+v", msgs)
	}
}

// TestToolResultSpillRecordsLocator verifies a spilled tool/result event keeps
// the structured spill record (locator + byte count) alongside the truncated
// output, and that deriving history still yields the model-visible text (which
// embeds the locator notice, D3).
func TestToolResultSpillRecordsLocator(t *testing.T) {
	l := New()
	l.Append(EventUserMessage, NewUserMessage("read it"))
	l.Append(EventAssistantMessage, NewAssistantMessage("", []llm.ToolCall{
		{ID: "call_9", Name: "read_file", Arguments: `{"path":"/big"}`},
	}, "tool_calls"))
	l.Append(EventToolResult, NewToolResult("call_9", "read_file", "head...[truncated; see spill]", &SpillRef{
		Locator: `D:\data\spill\s-x-7.txt`,
		Bytes:   100000,
	}))

	var d toolResultData
	if err := json.Unmarshal(l.Events()[2].Data, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Spill == nil || d.Spill.Locator != `D:\data\spill\s-x-7.txt` || d.Spill.Bytes != 100000 {
		t.Fatalf("spill record = %+v", d.Spill)
	}
	msgs := l.DeriveHistory()
	if msgs[2].Content != "head...[truncated; see spill]" {
		t.Fatalf("derived tool content = %q", msgs[2].Content)
	}
}
