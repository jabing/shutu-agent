// Package session implements the append-only event log that is the single
// source of truth for a conversation (D1). Model-visible history is always
// derived from the log (DeriveHistory); it is never stored separately.
package session

import (
	"encoding/json"
	"fmt"
	"strings"
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

	// M4 knowledge-base events (design.md §3): kb/recall lands with the M4a
	// kernel so the D3 logging mechanism exists before any orchestration;
	// kb/add arrives with M4b (explicit writes) and kb/extract with M4c
	// (post-answer extraction writeback). DeriveHistory ignores these types
	// (the recall is injected into context by the caller, design.md §8; the
	// extraction outcome is a log fact, not conversation), so adding them never
	// changes the turn/step structure (D4).
	EventKBRecall  = "kb/recall"
	EventKBAdd     = "kb/add"
	EventKBExtract = "kb/extract"

	// M5a background-job events (design.md §3 / ADR 2026-08-18-m5-agent-core.md
	// 决策 ① / dispatch-m5a-2): job/start lands when a job registers
	// successfully, job/status on a non-terminal transition (e.g.
	// running→stopping), job/done on a terminal settle. They are log-only
	// (D3): the model sees job state/output through the job_* tools' tool/result
	// events, and DeriveHistory treats these types as opaque data, so adding
	// them never changes the turn/step structure (D4). The payloads are pure
	// data projections — the session package never imports the jobs package.
	EventJobStart  = "job/start"
	EventJobStatus = "job/status"
	EventJobDone   = "job/done"

	// M5b subagent events (design.md §3 / ADR 2026-08-18-m5-agent-core.md
	// 决策 ② / dispatch-m5b-2 §1): subagent/start lands when a delegation
	// registers successfully, subagent/end when a child settles (observed on
	// the serial tool path, exactly once per child), subagent/report for an
	// explicit child→parent report. They are log-only (D3): the model sees
	// subagent state/output through the subagent_* tools' tool/result events,
	// and DeriveHistory treats these types as opaque data, so adding them
	// never changes the turn/step structure (D4). The payloads are pure data
	// projections — the session package never imports the subagent package.
	EventSubagentStart  = "subagent/start"
	EventSubagentEnd    = "subagent/end"
	EventSubagentReport = "subagent/report"
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
	// tagged pairs each derived message with the Seq of the event it came from,
	// so a compaction summary marker (user/message with surfaceOp.replace, M5c)
	// can drop the messages derived from its shadowed seq range and substitute
	// the summary in their place. Without such a marker the tagged pass rebuilds
	// exactly the same []llm.Message as a plain pass (no-replace behavior is
	// unchanged).
	type tagged struct {
		msg llm.Message
		seq uint64
	}
	var out []tagged
	var skipUntil uint64 // >0: also skip events whose Seq <= skipUntil (defensive forward skip)
	for _, ev := range events {
		if skipUntil != 0 && ev.Seq <= skipUntil {
			continue
		}
		switch ev.Type {
		case EventUserMessage:
			var d userMessageData
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			if op := d.SurfaceOp; op != nil && op.Op == surfaceReplaceOp {
				// The summary substitutes the shadowed surface range [Start, End].
				// Its events precede this marker in the append-only log (seq is
				// monotonic, so the marker's own Seq is > End), so drop the messages
				// already derived from that range and put the summary where they
				// began; then keep skipping any subsequent Seq in the range until
				// Seq > End (contract M5c-1a). Only Seq comparison is used — the
				// shadowed event contents are never parsed.
				start, end := op.Start, op.End
				if start >= 0 && end >= start {
					first, last := -1, -1
					for i, t := range out {
						if s := int64(t.seq); s >= start && s <= end {
							if first < 0 {
								first = i
							}
							last = i
						}
					}
					summary := llm.Message{Role: llm.RoleUser, Content: d.Text}
					if first >= 0 {
						head := out[:first]
						tail := out[last+1:]
						rebuilt := make([]tagged, 0, len(head)+1+len(tail))
						rebuilt = append(rebuilt, head...)
						rebuilt = append(rebuilt, tagged{msg: summary, seq: ev.Seq})
						rebuilt = append(rebuilt, tail...)
						out = rebuilt
					} else {
						out = append(out, tagged{msg: summary, seq: ev.Seq})
					}
					skipUntil = uint64(end)
					continue
				}
				// malformed range (negative Start or End < Start): no shadowing,
				// keep the message as a plain user turn.
			}
			out = append(out, tagged{msg: llm.Message{Role: llm.RoleUser, Content: d.Text}, seq: ev.Seq})
		case EventAssistantMessage:
			var d assistantMessageData
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			out = append(out, tagged{msg: llm.Message{
				Role:      llm.RoleAssistant,
				Content:   d.Text,
				ToolCalls: d.ToolCalls,
			}, seq: ev.Seq})
		case EventToolResult:
			var d toolResultData
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			out = append(out, tagged{msg: llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: d.CallID,
				Content:    d.Output,
			}, seq: ev.Seq})
		case EventToolError:
			var d toolErrorData
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			out = append(out, tagged{msg: llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: d.CallID,
				Content:    "Error: " + d.Error,
			}, seq: ev.Seq})
		}
	}
	msgs := make([]llm.Message, len(out))
	for i, t := range out {
		msgs[i] = t.msg
	}
	return msgs
}

// Payload structs for each v1 event type. Kept private: only the session
// package knows the on-disk shapes, and the loop builds them through the
// New* helpers below so model-visible inputs cannot be logged ad hoc.

type userMessageData struct {
	Text      string          `json:"text"`
	SurfaceOp *SurfaceReplace `json:"surfaceOp,omitempty"` // set by compaction summaries (M5c)
}

// surfaceReplaceOp is the only SurfaceReplace operation currently defined: the
// user/message carries a summary that substitutes the shadowed surface range
// [Start, End] (old events stay in the log, D1; derive() folds them out).
const surfaceReplaceOp = "replace"

// SurfaceReplace marks a user/message as a compaction summary that shadows the
// surface events whose Seq falls in [Start, End] (M5c, 决策 ③). Op is
// surfaceReplaceOp ("replace"); Start/End are the first/last shadowed event
// Seq, recorded at append time (Seq is monotonic, so End < the marker's Seq).
type SurfaceReplace struct {
	Op    string `json:"op"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
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

// NewUserMessageReplace builds a user/message payload for a compaction summary
// (M5c, 决策 ③): it carries the summary text plus a surfaceOp.replace marker
// shadowing the surface events whose Seq is in [start, end]. derive() then
// substitutes the summary for those events. The events themselves stay in the
// log (D1, append-only). NewUserMessage remains unchanged and is what normal
// turns use; only a compaction writes this payload.
func NewUserMessageReplace(text string, start, end int64) any {
	return userMessageData{Text: text, SurfaceOp: &SurfaceReplace{Op: surfaceReplaceOp, Start: start, End: end}}
}

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

// RecallHit is one knowledge-entry projection carried by a kb/recall event:
// the bounded summary the model is about to see. It is a plain data shape so
// the session package never depends on the kb package.
type RecallHit struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Snippet string   `json:"snippet,omitempty"` // bounded body fragment
	Type    string   `json:"type,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Scope   string   `json:"scope,omitempty"`
	Source  string   `json:"source,omitempty"`
	Score   float64  `json:"score"`
}

type kbRecallData struct {
	Query string      `json:"query"`
	Hits  []RecallHit `json:"hits,omitempty"`
}

// NewKBRecall builds the kb/recall payload (design.md §8 / D3). M4b's recall
// orchestration calls this immediately before injecting the recall into the
// model context, so the model-visible input is durably logged. DeriveHistory
// treats it as opaque data.
func NewKBRecall(query string, hits []RecallHit) any {
	return kbRecallData{Query: query, Hits: hits}
}

// kbAddData is the kb/add payload: a bounded summary of an explicit knowledge
// write (dispatch-m4b §3). Only the summary is logged, never the full body, so
// the log stays lean. DeriveHistory treats it as opaque data.
type kbAddData struct {
	EntryID string   `json:"entryId"`
	Title   string   `json:"title"`
	Type    string   `json:"type"`
	Tags    []string `json:"tags,omitempty"`
	Source  string   `json:"source,omitempty"`
	Version int      `json:"version"`
}

// NewKBAdd builds the kb/add payload recorded when an explicit write lands
// (dispatch-m4b §3 / D3).
func NewKBAdd(entryID, title, typ string, tags []string, source string, version int) any {
	return kbAddData{EntryID: entryID, Title: title, Type: typ, Tags: tags, Source: source, Version: version}
}

// kbExtractData is the kb/extract payload: the outcome of a post-answer
// extraction job (dispatch-m4c §2 / D3). Status is one of created | skipped |
// failed; Reason explains a skip or failure; IDs carries the ids of the entries
// created by a successful run. Only the summary is logged, never the model
// output or entry bodies. DeriveHistory treats it as opaque data.
type kbExtractData struct {
	Status  string   `json:"status"` // created | skipped | failed
	Session string   `json:"session,omitempty"`
	Turn    int      `json:"turn,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	IDs     []string `json:"ids,omitempty"` // created entry ids
}

// NewKBExtract builds the kb/extract payload recorded when the post-answer
// extraction writeback finishes for one session:turn (dispatch-m4c §2 / D3).
// status is created | skipped | failed.
func NewKBExtract(status, sessionID string, turn int, reason string, ids []string) any {
	return kbExtractData{Status: status, Session: sessionID, Turn: turn, Reason: reason, IDs: ids}
}

// jobStartData is the job/start payload: the registry-issued id plus the
// registration facts (kind/label/owner). DeriveHistory treats it as opaque
// data.
type jobStartData struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Label        string `json:"label"`
	OwnerSession string `json:"ownerSession,omitempty"`
}

// jobStatusData is the job/status payload: one observed non-terminal
// transition (e.g. running→stopping) with its kind-specific detail.
// DeriveHistory treats it as opaque data.
type jobStatusData struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// jobDoneData is the job/done payload: a terminal settle (completed/killed/
// failed) plus a bounded output summary. The log only ever carries the
// summary, never the full output (which the model sees through job_read's
// tool/result). DeriveHistory treats it as opaque data.
type jobDoneData struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Detail        string `json:"detail,omitempty"`
	OutputSummary string `json:"outputSummary,omitempty"`
}

// jobOutputSummaryMax bounds the job/done output summary (dispatch-m5a-2: 输出
// 只记摘要，有界), keeping the log lean regardless of what the caller passes.
const jobOutputSummaryMax = 200

// NewJobStart builds the job/start payload recorded when a job registers
// successfully (dispatch-m5a-2 §1 / D3).
func NewJobStart(id, kind, label, ownerSession string) any {
	return jobStartData{ID: id, Kind: kind, Label: label, OwnerSession: ownerSession}
}

// NewJobStatus builds the job/status payload recorded when a job's status
// transitions (e.g. running→stopping) (dispatch-m5a-2 §1 / D3).
func NewJobStatus(id, status, detail string) any {
	return jobStatusData{ID: id, Status: status, Detail: detail}
}

// NewJobDone builds the job/done payload recorded when a job settles
// terminally. output is bounded to a summary head by the constructor so the
// payload is always lean (dispatch-m5a-2 §1 / D3).
func NewJobDone(id, status, detail, output string) any {
	return jobDoneData{ID: id, Status: status, Detail: detail, OutputSummary: summaryHead(output)}
}

// summaryHead returns a bounded, whitespace-compacted head of s for a log
// summary (mirrors kb.Snippet's bound; kept here so the session package owns
// the on-disk bound it serializes). It is shared by job/done and subagent/end
// (both bounded to 200 runes, dispatch-m5a-2 §1 / dispatch-m5b-2 §1).
func summaryHead(s string) string {
	compact := strings.Join(strings.Fields(s), " ")
	runes := []rune(compact)
	if len(runes) > jobOutputSummaryMax {
		return string(runes[:jobOutputSummaryMax]) + "…"
	}
	return compact
}

// subagentStartData is the subagent/start payload: the provider-issued child
// session id, the provider name, the delegating parent session, and the
// delegation label. DeriveHistory treats it as opaque data.
type subagentStartData struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	ParentSession string `json:"parentSession,omitempty"`
	Label         string `json:"label,omitempty"`
}

// subagentEndData is the subagent/end payload: a terminal settle (stop reason
// from the subagent vocabulary: completed | aborted | error | max-tokens |
// refusal) plus a bounded output summary — the full output the model reads
// through subagent_status' tool/result. DeriveHistory treats it as opaque
// data.
type subagentEndData struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	StopReason    string `json:"stopReason"`
	OutputSummary string `json:"outputSummary,omitempty"`
}

// subagentReportData is the subagent/report payload: an explicit child→parent
// report (child session id + delegating parent session + report content).
// DeriveHistory treats it as opaque data.
type subagentReportData struct {
	ID            string `json:"id"`
	ParentSession string `json:"parentSession,omitempty"`
	Content       string `json:"content"`
}

// NewSubagentStart builds the subagent/start payload recorded when a
// delegation registers successfully (dispatch-m5b-2 §1 / D3).
func NewSubagentStart(childID, provider, parentSessionID, label string) any {
	return subagentStartData{ID: childID, Provider: provider, ParentSession: parentSessionID, Label: label}
}

// NewSubagentEnd builds the subagent/end payload recorded when a child settles
// (dispatch-m5b-2 §1 / D3). output is bounded to a summary head (200 runes,
// the same on-disk bound as job/done) so the payload is always lean.
func NewSubagentEnd(childID, provider, stopReason, outputSummary string) any {
	return subagentEndData{ID: childID, Provider: provider, StopReason: stopReason, OutputSummary: summaryHead(outputSummary)}
}

// NewSubagentReport builds the subagent/report payload recorded when a child
// explicitly reports to its parent session (dispatch-m5b-2 §1 / D3).
func NewSubagentReport(childID, parentSessionID, content string) any {
	return subagentReportData{ID: childID, ParentSession: parentSessionID, Content: content}
}
