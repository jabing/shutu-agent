package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"personal-agent/internal/session"
)

// Tool name constants (config.terminalToolNames whitelist corresponds to these).
const (
	ToolStartName  = "terminal_start"
	ToolWriteName  = "terminal_write"
	ToolReadName   = "terminal_read"
	ToolSignalName = "terminal_signal"
	ToolStopName   = "terminal_stop"
)

// toolViewportMax bounds every model-facing terminal text (dispatch-m9-2 §4).
const toolViewportMax = 8000

// TerminalAccess is implemented by the composition root (cmd/pa): it provides
// access to the current session's terminal and validates ownership.
type TerminalAccess interface {
	// Owner returns the current session id (owner of the terminal).
	Owner() string
	// GetActive returns the active session, or an error when there is none
	// or the owner does not match.
	GetActive() (*Session, error)
	// Start launches a fresh session; it errors with "already active" when a
	// session is already running.
	Start(opts SessionOpts) (*Session, error)
	// Stop shuts down the active session.
	Stop() error
}

// TerminalTools bundles the shared state of the five terminal_* tools.
type TerminalTools struct {
	acc     TerminalAccess
	onEvent func(typ string, data any)
}

// NewTerminalTools builds the shared tool state. onEvent receives the
// session.EventTerminalStart / session.EventTerminalStop notifications.
func NewTerminalTools(acc TerminalAccess, onEvent func(typ string, data any)) *TerminalTools {
	return &TerminalTools{acc: acc, onEvent: onEvent}
}

func (t *TerminalTools) Start() TerminalStartTool   { return TerminalStartTool{t: t} }
func (t *TerminalTools) Write() TerminalWriteTool   { return TerminalWriteTool{t: t} }
func (t *TerminalTools) Read() TerminalReadTool     { return TerminalReadTool{t: t} }
func (t *TerminalTools) Signal() TerminalSignalTool { return TerminalSignalTool{t: t} }
func (t *TerminalTools) Stop() TerminalStopTool     { return TerminalStopTool{t: t} }

// emit forwards a terminal lifecycle event to the registered callback.
func (t *TerminalTools) emit(typ string, data any) {
	if t.onEvent != nil {
		t.onEvent(typ, data)
	}
}

// TerminalStartTool starts a new terminal session.
type TerminalStartTool struct {
	t *TerminalTools
}

func (x TerminalStartTool) Name() string { return ToolStartName }

func (x TerminalStartTool) Description() string {
	return "Start a new terminal session; optionally run an initial command."
}

func (x TerminalStartTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "optional first command to run in the fresh session",
			},
		},
		"additionalProperties": false,
	}
}

func (x TerminalStartTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Command string `json:"command"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("terminal_start: %w", err)
		}
	}
	sess, err := x.t.acc.Start(SessionOpts{})
	if err != nil {
		return "", fmt.Errorf("terminal_start: %w", err)
	}
	out := "started terminal session " + sess.ID()
	if a.Command != "" {
		res, err := sess.Write(a.Command, true)
		if err != nil {
			return "", fmt.Errorf("terminal_start: %w", err)
		}
		out += "\nviewport:\n" + truncateView(res.Viewport, toolViewportMax)
	}
	x.t.emit(session.EventTerminalStart, session.NewTerminalStart(sess.ID(), x.t.acc.Owner()))
	return out, nil
}

// TerminalWriteTool writes text to the active terminal session.
type TerminalWriteTool struct {
	t *TerminalTools
}

func (x TerminalWriteTool) Name() string { return ToolWriteName }

func (x TerminalWriteTool) Description() string {
	return "Write text to the active terminal session, optionally submitting it."
}

func (x TerminalWriteTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type":      "string",
				"minLength": 1,
			},
			"submit": map[string]any{
				"type": "boolean",
			},
		},
		"required":             []string{"text"},
		"additionalProperties": false,
	}
}

func (x TerminalWriteTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text   string `json:"text"`
		Submit *bool  `json:"submit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("terminal_write: %w", err)
	}
	if a.Text == "" {
		return "", fmt.Errorf("terminal_write: text is required")
	}
	sess, err := x.t.acc.GetActive()
	if err != nil {
		return "", fmt.Errorf("terminal_write: %w", err)
	}
	submit := true
	if a.Submit != nil {
		submit = *a.Submit
	}
	res, err := sess.Write(a.Text, submit)
	if err != nil {
		return "", fmt.Errorf("terminal_write: %w", err)
	}
	out := fmt.Sprintf("wait=%s status=%s", res.Wait, res.Status.Kind)
	out += "\nviewport:\n" + truncateView(res.Viewport, toolViewportMax)
	if res.Truncated {
		out += "\n[viewport truncated]"
	}
	return out, nil
}

// TerminalReadTool reads output from the active terminal session.
type TerminalReadTool struct {
	t *TerminalTools
}

func (x TerminalReadTool) Name() string { return ToolReadName }

func (x TerminalReadTool) Description() string {
	return "Read output from the active terminal session."
}

func (x TerminalReadTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"offset": map[string]any{"type": "integer", "minimum": 0},
			"count":  map[string]any{"type": "integer", "minimum": 1},
		},
		"additionalProperties": false,
	}
}

func (x TerminalReadTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Offset int `json:"offset"`
		Count  int `json:"count"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("terminal_read: %w", err)
		}
	}
	if a.Offset < 0 {
		a.Offset = 0
	}
	if a.Count < 1 {
		a.Count = 500
	}
	sess, err := x.t.acc.GetActive()
	if err != nil {
		return "", fmt.Errorf("terminal_read: %w", err)
	}
	text, _ := sess.Read(a.Offset, a.Count)
	return truncateView(text, toolViewportMax), nil
}

// TerminalSignalTool sends a signal to the active terminal session.
type TerminalSignalTool struct {
	t *TerminalTools
}

func (x TerminalSignalTool) Name() string { return ToolSignalName }

func (x TerminalSignalTool) Description() string {
	return "Send a stop or interrupt signal to the active terminal session."
}

func (x TerminalSignalTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind": map[string]any{
				"type": "string",
				"enum": []string{"stop", "interrupt"},
			},
		},
		"required":             []string{"kind"},
		"additionalProperties": false,
	}
}

func (x TerminalSignalTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("terminal_signal: %w", err)
	}
	sess, err := x.t.acc.GetActive()
	if err != nil {
		return "", fmt.Errorf("terminal_signal: %w", err)
	}
	if err := sess.Signal(a.Kind); err != nil {
		return "", fmt.Errorf("terminal_signal: %w", err)
	}
	return "delivered " + a.Kind, nil
}

// TerminalStopTool stops the active terminal session.
type TerminalStopTool struct {
	t *TerminalTools
}

func (x TerminalStopTool) Name() string { return ToolStopName }

func (x TerminalStopTool) Description() string {
	return "Stop and close the active terminal session."
}

func (x TerminalStopTool) Schema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

func (x TerminalStopTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	sess, err := x.t.acc.GetActive()
	if err != nil {
		return "", fmt.Errorf("terminal_stop: %w", err)
	}
	id := sess.ID()
	if err := x.t.acc.Stop(); err != nil {
		return "", fmt.Errorf("terminal_stop: %w", err)
	}
	x.t.emit(session.EventTerminalStop, session.NewTerminalStop(id, "user"))
	return "terminal session stopped", nil
}

const truncateNotice = "\n[terminal output truncated]"

// truncateView shortens model-facing terminal text to at most maxBytes,
// backing off to a rune boundary and appending a truncated notice.
func truncateView(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return s
	}
	if len(s) <= maxBytes {
		return s
	}
	head := truncateUTF8(s, maxBytes-len(truncateNotice))
	return head + truncateNotice
}

// truncateUTF8 shortens s to at most maxBytes bytes without splitting a rune.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	s = s[:maxBytes]
	for !utf8.ValidString(s) {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
