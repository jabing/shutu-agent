package session

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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

// TestKBExtractEventAppendsAndReplays verifies the M4c kb/extract event type
// (dispatch-m4c §2 / D3): the post-answer extraction outcome (created /
// skipped / failed + reason) appends with the right vocabulary, survives the
// JSON round-trip and restart replay, and stays opaque to history derivation
// (an extraction outcome is a log fact, not conversation).
func TestKBExtractEventAppendsAndReplays(t *testing.T) {
	var persisted []Event
	l := New()
	l.SetSink(func(ev Event) error {
		persisted = append(persisted, ev)
		return nil
	})
	// A created outcome carries the written entry ids.
	if _, err := l.Append(EventKBExtract, NewKBExtract("created", "s1", 3, "", []string{"kb-1", "kb-2"})); err != nil {
		t.Fatalf("append created: %v", err)
	}
	// A failed outcome carries a reason.
	if _, err := l.Append(EventKBExtract, NewKBExtract("failed", "s1", 4, "extraction model returned invalid JSON", nil)); err != nil {
		t.Fatalf("append failed: %v", err)
	}
	if len(l.Events()) != 2 || l.Events()[0].Type != EventKBExtract || l.Events()[0].Version != EventVersion {
		t.Fatalf("events = %+v, want 2 kb/extract at version %d", l.Events(), EventVersion)
	}
	var d kbExtractData
	if err := json.Unmarshal(l.Events()[0].Data, &d); err != nil {
		t.Fatalf("unmarshal created: %v", err)
	}
	if d.Status != "created" || d.Session != "s1" || d.Turn != 3 || d.Reason != "" || len(d.IDs) != 2 || d.IDs[0] != "kb-1" {
		t.Fatalf("created payload = %+v", d)
	}
	var f kbExtractData
	if err := json.Unmarshal(l.Events()[1].Data, &f); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if f.Status != "failed" || !strings.Contains(f.Reason, "invalid JSON") {
		t.Fatalf("failed payload = %+v", f)
	}
	if len(persisted) != 2 || persisted[1].Type != EventKBExtract {
		t.Fatalf("sink (append path) = %+v", persisted)
	}
	fresh := New()
	if err := fresh.Restore(persisted); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := fresh.Events()[0]; got.Type != EventKBExtract {
		t.Fatalf("replayed type = %q", got.Type)
	}
	if msgs := fresh.DeriveHistory(); len(msgs) != 0 {
		t.Fatalf("kb/extract must not derive into messages: %+v", msgs)
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

// TestJobEventsAppendAndReplay verifies the M5a job/* event types
// (job/start, job/status, job/done — dispatch-m5a-2 §1 / D3): each appends
// with the right vocabulary, survives the JSON round-trip and restart replay,
// and stays opaque to history derivation (job state is surfaced to the model
// through the job_* tools' tool/result, not through these log-only events).
func TestJobEventsAppendAndReplay(t *testing.T) {
	var persisted []Event
	l := New()
	l.SetSink(func(ev Event) error {
		persisted = append(persisted, ev)
		return nil
	})
	if _, err := l.Append(EventJobStart, NewJobStart("bash-1", "bash", "echo hello", "s-abc")); err != nil {
		t.Fatalf("append job/start: %v", err)
	}
	if _, err := l.Append(EventJobStatus, NewJobStatus("bash-1", "stopping", "cancelled")); err != nil {
		t.Fatalf("append job/status: %v", err)
	}
	if _, err := l.Append(EventJobDone, NewJobDone("bash-1", "killed", "cancelled", strings.Repeat("very long output ", 50))); err != nil {
		t.Fatalf("append job/done: %v", err)
	}
	events := l.Events()
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	if events[0].Type != EventJobStart || events[1].Type != EventJobStatus || events[2].Type != EventJobDone {
		t.Fatalf("types = %q/%q/%q", events[0].Type, events[1].Type, events[2].Type)
	}
	for i, ev := range events {
		if ev.Version != EventVersion {
			t.Fatalf("event %d version = %d, want %d", i, ev.Version, EventVersion)
		}
	}
	// JSON round-trip of each payload.
	var st jobStartData
	if err := json.Unmarshal(events[0].Data, &st); err != nil {
		t.Fatalf("unmarshal job/start: %v", err)
	}
	if st.ID != "bash-1" || st.Kind != "bash" || st.Label != "echo hello" || st.OwnerSession != "s-abc" {
		t.Fatalf("job/start payload = %+v", st)
	}
	var ss jobStatusData
	if err := json.Unmarshal(events[1].Data, &ss); err != nil {
		t.Fatalf("unmarshal job/status: %v", err)
	}
	if ss.ID != "bash-1" || ss.Status != "stopping" || ss.Detail != "cancelled" {
		t.Fatalf("job/status payload = %+v", ss)
	}
	var sd jobDoneData
	if err := json.Unmarshal(events[2].Data, &sd); err != nil {
		t.Fatalf("unmarshal job/done: %v", err)
	}
	if sd.ID != "bash-1" || sd.Status != "killed" || sd.Detail != "cancelled" {
		t.Fatalf("job/done payload = %+v", sd)
	}
	// The output summary must be bounded (dispatch-m5a-2: 输出只记摘要，有界).
	if got := len([]rune(sd.OutputSummary)); got > jobOutputSummaryMax+1 {
		t.Fatalf("job/done summary = %d runes, want <= %d+ellipsis", got, jobOutputSummaryMax)
	}
	if !strings.Contains(sd.OutputSummary, "very long output") {
		t.Fatalf("job/done summary = %q, want it to carry the output head", sd.OutputSummary)
	}
	if len(persisted) != 3 || persisted[0].Type != EventJobStart {
		t.Fatalf("sink (append path) = %+v", persisted)
	}
	// Restart replay: a fresh log rebuilt from what was persisted still sees
	// every job event, and deriving history treats them all as opaque data.
	fresh := New()
	if err := fresh.Restore(persisted); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for i, want := range []string{EventJobStart, EventJobStatus, EventJobDone} {
		if got := fresh.Events()[i].Type; got != want {
			t.Fatalf("replayed type %d = %q, want %q", i, got, want)
		}
	}
	if msgs := fresh.DeriveHistory(); len(msgs) != 0 {
		t.Fatalf("job/* events must not derive into messages: %+v", msgs)
	}
}

// TestSubagentEventsAppendAndReplay verifies the M5b subagent/* event types
// (subagent/start, subagent/end, subagent/report — dispatch-m5b-2 §1 / D3):
// each appends with the right vocabulary, survives the JSON round-trip and
// restart replay, and stays opaque to history derivation (subagent state is
// surfaced to the model through the subagent_* tools' tool/result, not through
// these log-only events).
func TestSubagentEventsAppendAndReplay(t *testing.T) {
	var persisted []Event
	l := New()
	l.SetSink(func(ev Event) error {
		persisted = append(persisted, ev)
		return nil
	})
	if _, err := l.Append(EventSubagentStart, NewSubagentStart("spawn-1", "spawn", "s-abc", "researcher")); err != nil {
		t.Fatalf("append subagent/start: %v", err)
	}
	if _, err := l.Append(EventSubagentEnd, NewSubagentEnd("spawn-1", "spawn", "completed", strings.Repeat("very long output ", 50))); err != nil {
		t.Fatalf("append subagent/end: %v", err)
	}
	if _, err := l.Append(EventSubagentReport, NewSubagentReport("spawn-1", "s-abc", "done researching")); err != nil {
		t.Fatalf("append subagent/report: %v", err)
	}
	events := l.Events()
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	if events[0].Type != EventSubagentStart || events[1].Type != EventSubagentEnd || events[2].Type != EventSubagentReport {
		t.Fatalf("types = %q/%q/%q", events[0].Type, events[1].Type, events[2].Type)
	}
	for i, ev := range events {
		if ev.Version != EventVersion {
			t.Fatalf("event %d version = %d, want %d", i, ev.Version, EventVersion)
		}
	}
	// JSON round-trip of each payload.
	var st subagentStartData
	if err := json.Unmarshal(events[0].Data, &st); err != nil {
		t.Fatalf("unmarshal subagent/start: %v", err)
	}
	if st.ID != "spawn-1" || st.Provider != "spawn" || st.ParentSession != "s-abc" || st.Label != "researcher" {
		t.Fatalf("subagent/start payload = %+v", st)
	}
	var se subagentEndData
	if err := json.Unmarshal(events[1].Data, &se); err != nil {
		t.Fatalf("unmarshal subagent/end: %v", err)
	}
	if se.ID != "spawn-1" || se.Provider != "spawn" || se.StopReason != "completed" {
		t.Fatalf("subagent/end payload = %+v", se)
	}
	// The output summary must be bounded (dispatch-m5b-2 §1: 输出只记摘要，有界
	// 200 rune).
	if got := len([]rune(se.OutputSummary)); got > jobOutputSummaryMax+1 {
		t.Fatalf("subagent/end summary = %d runes, want <= %d+ellipsis", got, jobOutputSummaryMax)
	}
	if !strings.Contains(se.OutputSummary, "very long output") {
		t.Fatalf("subagent/end summary = %q, want it to carry the output head", se.OutputSummary)
	}
	var sr subagentReportData
	if err := json.Unmarshal(events[2].Data, &sr); err != nil {
		t.Fatalf("unmarshal subagent/report: %v", err)
	}
	if sr.ID != "spawn-1" || sr.ParentSession != "s-abc" || sr.Content != "done researching" {
		t.Fatalf("subagent/report payload = %+v", sr)
	}
	if len(persisted) != 3 || persisted[0].Type != EventSubagentStart {
		t.Fatalf("sink (append path) = %+v", persisted)
	}
	// Restart replay: a fresh log rebuilt from what was persisted still sees
	// every subagent event, and deriving history treats them all as opaque
	// data.
	fresh := New()
	if err := fresh.Restore(persisted); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for i, want := range []string{EventSubagentStart, EventSubagentEnd, EventSubagentReport} {
		if got := fresh.Events()[i].Type; got != want {
			t.Fatalf("replayed type %d = %q, want %q", i, got, want)
		}
	}
	if msgs := fresh.DeriveHistory(); len(msgs) != 0 {
		t.Fatalf("subagent/* events must not derive into messages: %+v", msgs)
	}
}

// M5c-1a compaction fold rule: a user/message carrying surfaceOp.replace
// substitutes a summary for the shadowed surface range [Start, End] in the
// derived history, while the shadowed events stay in the append-only log (D1).

func TestDeriveHistoryReplaceFoldsSummaryPlusTail(t *testing.T) {
	l := New()
	// Shadowed surface: seqs 1-4 (an old exchange).
	l.Append(EventUserMessage, NewUserMessage("old question"))
	l.Append(EventAssistantMessage, NewAssistantMessage("old answer", nil, "stop"))
	l.Append(EventUserMessage, NewUserMessage("old question 2"))
	l.Append(EventAssistantMessage, NewAssistantMessage("old answer 2", nil, "stop"))
	// Compaction summary marker appended after the shadowed range (D1).
	l.Append(EventUserMessage, NewUserMessageReplace("summarized", 1, 4))
	// Unshadowed tail continues after the compaction.
	l.Append(EventUserMessage, NewUserMessage("new question"))
	l.Append(EventAssistantMessage, NewAssistantMessage("new answer", nil, "stop"))

	msgs := l.DeriveHistory()
	if len(msgs) != 3 {
		t.Fatalf("derived %d messages, want 3: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != llm.RoleUser || msgs[0].Content != "summarized" {
		t.Fatalf("msg0 = %+v, want user summary", msgs[0])
	}
	if msgs[1].Role != llm.RoleUser || msgs[1].Content != "new question" {
		t.Fatalf("msg1 = %+v, want unshadowed tail user", msgs[1])
	}
	if msgs[2].Role != llm.RoleAssistant || msgs[2].Content != "new answer" {
		t.Fatalf("msg2 = %+v, want unshadowed tail assistant", msgs[2])
	}
	// D1: shadowed events are still physically in the log.
	if got := len(l.Events()); got != 7 {
		t.Fatalf("log events = %d, want 7 (append-only, shadowed events retained)", got)
	}
}

func TestDeriveHistoryWithoutReplaceUnchanged(t *testing.T) {
	l := New()
	l.Append(EventUserMessage, NewUserMessage("a"))
	l.Append(EventAssistantChunk, NewAssistantChunk("A"))
	l.Append(EventAssistantMessage, NewAssistantMessage("A", nil, "stop"))
	l.Append(EventUserMessage, NewUserMessage("b"))
	l.Append(EventAssistantMessage, NewAssistantMessage("B", []llm.ToolCall{
		{ID: "call_x", Name: "get_time", Arguments: `{}`},
	}, "tool_calls"))
	l.Append(EventToolResult, NewToolResult("call_x", "get_time", "12:00", nil))

	want := []llm.Message{
		{Role: llm.RoleUser, Content: "a"},
		{Role: llm.RoleAssistant, Content: "A"},
		{Role: llm.RoleUser, Content: "b"},
		{Role: llm.RoleAssistant, Content: "B", ToolCalls: []llm.ToolCall{{ID: "call_x", Name: "get_time", Arguments: `{}`}}},
		{Role: llm.RoleTool, ToolCallID: "call_x", Content: "12:00"},
	}
	if msgs := l.DeriveHistory(); !reflect.DeepEqual(msgs, want) {
		t.Fatalf("derived = %+v, want %+v (no replace marker must not change folding)", msgs, want)
	}
}

func TestDeriveHistoryReplaceShadowingMixedEvents(t *testing.T) {
	l := New()
	// Shadowed range spans user, assistant (with a tool call) and tool/result.
	l.Append(EventUserMessage, NewUserMessage("read the file")) // 1
	l.Append(EventAssistantMessage, NewAssistantMessage("", []llm.ToolCall{
		{ID: "call_1", Name: "read_file", Arguments: `{"path":"/tmp/x"}`},
	}, "tool_calls")) // 2
	l.Append(EventToolResult, NewToolResult("call_1", "read_file", "file contents", nil)) // 3
	l.Append(EventAssistantMessage, NewAssistantMessage("Here it is", nil, "stop"))       // 4
	l.Append(EventUserMessage, NewUserMessageReplace("compacted 1-4", 1, 4))              // 5
	l.Append(EventUserMessage, NewUserMessage("continue"))                                // 6
	l.Append(EventAssistantMessage, NewAssistantMessage("continuing", nil, "stop"))       // 7

	msgs := l.DeriveHistory()
	if len(msgs) != 3 {
		t.Fatalf("derived %d messages, want 3: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != llm.RoleUser || msgs[0].Content != "compacted 1-4" {
		t.Fatalf("msg0 = %+v, want summary over mixed shadowed events", msgs[0])
	}
	if msgs[1].Role != llm.RoleUser || msgs[1].Content != "continue" {
		t.Fatalf("msg1 = %+v, want tail user", msgs[1])
	}
	if msgs[2].Role != llm.RoleAssistant || msgs[2].Content != "continuing" {
		t.Fatalf("msg2 = %+v, want tail assistant", msgs[2])
	}
}

func TestDeriveHistoryReplaceEmptySummaryPreserved(t *testing.T) {
	l := New()
	l.Append(EventUserMessage, NewUserMessage("old"))                            // 1
	l.Append(EventAssistantMessage, NewAssistantMessage("old reply", nil, "stop")) // 2
	l.Append(EventUserMessage, NewUserMessageReplace("", 1, 2))                  // 3: empty summary text
	l.Append(EventUserMessage, NewUserMessage("new"))                            // 4

	msgs := l.DeriveHistory()
	if len(msgs) != 2 {
		t.Fatalf("derived %d messages, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != llm.RoleUser || msgs[0].Content != "" {
		t.Fatalf("msg0 = %+v, want preserved empty summary user message", msgs[0])
	}
	if msgs[1].Role != llm.RoleUser || msgs[1].Content != "new" {
		t.Fatalf("msg1 = %+v, want tail user", msgs[1])
	}
}

func TestNewUserMessageReplaceJSONRoundTrip(t *testing.T) {
	// surfaceOp serializes with the replace marker on the replace payload.
	raw, err := json.Marshal(NewUserMessageReplace("summary", 2, 7))
	if err != nil {
		t.Fatalf("marshal replace payload: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal replace payload: %v", err)
	}
	if m["text"] != "summary" {
		t.Fatalf("text = %v, want summary", m["text"])
	}
	so, ok := m["surfaceOp"].(map[string]any)
	if !ok {
		t.Fatalf("surfaceOp missing in %s", raw)
	}
	if so["op"] != "replace" || so["start"] != float64(2) || so["end"] != float64(7) {
		t.Fatalf("surfaceOp = %+v, want {op:replace start:2 end:7}", so)
	}

	// NewUserMessage stays surfaceOp-free (omitempty, backward compatible).
	plain, err := json.Marshal(NewUserMessage("hi"))
	if err != nil {
		t.Fatalf("marshal plain payload: %v", err)
	}
	var pm map[string]any
	if err := json.Unmarshal(plain, &pm); err != nil {
		t.Fatalf("unmarshal plain payload: %v", err)
	}
	if _, ok := pm["surfaceOp"]; ok {
		t.Fatalf("plain user/message payload must not carry surfaceOp: %s", plain)
	}

	// surfaceOp deserializes back into the typed payload.
	var d userMessageData
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("unmarshal into userMessageData: %v", err)
	}
	if d.Text != "summary" || d.SurfaceOp == nil || d.SurfaceOp.Op != "replace" ||
		d.SurfaceOp.Start != 2 || d.SurfaceOp.End != 7 {
		t.Fatalf("userMessageData = %+v", d)
	}

	// Full round trip through Append + Restore: the persisted surfaceOp payload
	// folds the shadowed range out after a restart replay.
	l := New()
	l.Append(EventUserMessage, NewUserMessage("x"))                            // 1
	l.Append(EventAssistantMessage, NewAssistantMessage("y", nil, "stop"))     // 2
	l.Append(EventUserMessage, NewUserMessageReplace("s", 1, 2))               // 3
	fresh := New()
	if err := fresh.Restore(l.Events()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	msgs := fresh.DeriveHistory()
	if len(msgs) != 1 || msgs[0].Role != llm.RoleUser || msgs[0].Content != "s" {
		t.Fatalf("round-trip derived = %+v, want [user s]", msgs)
	}
}
