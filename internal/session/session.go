// Package session implements the append-only event log that is the single
// source of truth for a conversation (D1). Model-visible history is always
// derived from the log (DeriveHistory); it is never stored separately.
package session

import (
	"encoding/json"
	"fmt"
	"time"

	"personal-agent/internal/llm"
)

// Event type discriminators (v1 vocabulary, see design.md §3).
const (
	EventUserMessage      = "user/message"
	EventAssistantChunk   = "assistant/chunk"
	EventAssistantMessage = "assistant/message"
	EventToolResult       = "tool/result"
	EventToolError        = "tool/error"
)

// EventVersion is the current event vocabulary version. It is stored per event
// (design.md D8) so a future event type or payload shape never requires
// migrating old rows: readers that do not understand a version keep the row as
// opaque data and derive history only from the types they know.
const EventVersion = 1

// Event is one append-only row of the session log. Seq is monotonically
// increasing and becomes the cross-restart primary key once persisted. Version
// carries the event vocabulary version (see EventVersion); Data is an opaque
// JSON blob whose shape is owned by Type.
type Event struct {
	Seq     uint64
	Type    string
	At      time.Time
	Version int
	Data    json.RawMessage
}

// Log is an in-memory append-only event log. It is not safe for concurrent
// use: the agent loop is strictly serial (D5).
type Log struct {
	events []Event
	seq    uint64
	sink   func(Event) error // optional durable sink (D8), called after each append
}

// New returns an empty in-memory log.
func New() *Log {
	return &Log{}
}

// SetSink installs an optional durable sink that receives every committed
// event (typically forwarding it to a store). A sink error rolls the event
// back out of the in-memory log and fails the Append, so the log never drifts
// from what was actually persisted (D1: the log is the source of truth).
func (l *Log) SetSink(sink func(Event) error) {
	l.sink = sink
}

// Restore rebuilds the log from scratch with previously persisted events
// (startup replay, D8). Events must arrive in strictly increasing Seq order;
// after a successful Restore the next Append continues after the last Seq.
// Restore never invokes the sink — replaying is loading, not appending.
func (l *Log) Restore(events []Event) error {
	l.events = nil
	l.seq = 0
	var last uint64
	for _, ev := range events {
		if ev.Seq <= last {
			return fmt.Errorf("session: non-monotonic seq %d after %d in replay", ev.Seq, last)
		}
		l.events = append(l.events, ev)
		last = ev.Seq
	}
	l.seq = last
	return nil
}

// Append marshals data and appends one event, assigning the next Seq, At and
// Version. When a durable sink is installed it is called with the committed
// event; a sink error rolls the event back and is returned.
func (l *Log) Append(typ string, data any) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	l.seq++
	ev := Event{Seq: l.seq, Type: typ, At: time.Now().UTC(), Version: EventVersion, Data: raw}
	l.events = append(l.events, ev)
	if l.sink != nil {
		if err := l.sink(ev); err != nil {
			l.events = l.events[:len(l.events)-1]
			return Event{}, fmt.Errorf("session: persist %s event: %w", typ, err)
		}
	}
	return ev, nil
}

// Events returns a snapshot copy of the current event log.
func (l *Log) Events() []Event {
	out := make([]Event, len(l.events))
	copy(out, l.events)
	return out
}

// NextSeq returns the Seq the next Append will assign (current Seq + 1). M3
// uses it to name a spill file after the tool/result event that will carry the
// locator.
func (l *Log) NextSeq() uint64 { return l.seq + 1 }

// DeriveHistory folds the log into model-visible messages (design.md §3:
// history is a pure derivation of the log). assistant/chunk rows are streaming
// fidelity records and are folded away in favor of the authoritative
// assistant/message row that closes the step.
func (l *Log) DeriveHistory() []llm.Message {
	return derive(l.events)
}

func derive(events []Event) []llm.Message {
	var msgs []llm.Message
	for _, ev := range events {
		switch ev.Type {
		case EventUserMessage:
			var d userMessageData
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: d.Text})
		case EventAssistantMessage:
			var d assistantMessageData
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			msgs = append(msgs, llm.Message{
				Role:      llm.RoleAssistant,
				Content:   d.Text,
				ToolCalls: d.ToolCalls,
			})
		case EventToolResult:
			var d toolResultData
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			msgs = append(msgs, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: d.CallID,
				Content:    d.Output,
			})
		case EventToolError:
			var d toolErrorData
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			msgs = append(msgs, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: d.CallID,
				Content:    "Error: " + d.Error,
			})
		}
	}
	return msgs
}

// Payload structs for each v1 event type. Kept private: only the session
// package knows the on-disk shapes, and the loop builds them through the
// New* helpers below so model-visible inputs cannot be logged ad hoc.

type userMessageData struct {
	Text string `json:"text"`
}

type assistantChunkData struct {
	Text string `json:"text"`
}

type assistantMessageData struct {
	Text         string         `json:"text"`
	ToolCalls    []llm.ToolCall `json:"toolCalls,omitempty"`
	FinishReason string         `json:"finishReason,omitempty"`
}

type toolResultData struct {
	CallID string    `json:"callId"`
	Name   string    `json:"name"`
	Output string    `json:"output"`
	Spill  *SpillRef `json:"spill,omitempty"` // set when the output was truncated and spilled
}

// SpillRef is recorded on a tool/result event when the tool output exceeded
// the output limit and the full text was spilled to disk. The locator is
// model-visible — the model reads the full file through it — so it must be
// logged (D3). Output already carries the truncation notice with the locator;
// this structured copy is for tooling/replay.
type SpillRef struct {
	Locator string `json:"locator"`
	Bytes   int    `json:"bytes"`
}

type toolErrorData struct {
	CallID string `json:"callId"`
	Name   string `json:"name"`
	Error  string `json:"error"`
}

// NewUserMessage builds the user/message payload.
func NewUserMessage(text string) any { return userMessageData{Text: text} }

// NewAssistantChunk builds one assistant/chunk payload (streaming fidelity).
func NewAssistantChunk(text string) any { return assistantChunkData{Text: text} }

// NewAssistantMessage builds the authoritative assistant/message payload that
// closes a step.
func NewAssistantMessage(text string, toolCalls []llm.ToolCall, finishReason string) any {
	return assistantMessageData{Text: text, ToolCalls: toolCalls, FinishReason: finishReason}
}

// NewToolResult builds one successful tool/result payload. spill is the
// truncation record (non-nil only when the output was spilled to disk, M3).
func NewToolResult(callID, name, output string, spill *SpillRef) any {
	return toolResultData{CallID: callID, Name: name, Output: output, Spill: spill}
}

// NewToolError builds one failed tool/error payload.
func NewToolError(callID, name, err string) any {
	return toolErrorData{CallID: callID, Name: name, Error: err}
}
