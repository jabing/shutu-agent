// tools.go — the M6d-2 Consumer half of the interact seam (design.md §8
// Consumer / D2, dispatch-m6d-2 §3): interact_ask and interact_status are
// registered into the tools.Registry by the composition root (cmd/pa) when
// interact.enabled, and auto-whitelisted by config.applyDefaults the same way
// the job_*/subagent_*/skill_*/schedule_*/plan_*/spill_* tools are. They
// implement the tools.Tool method set structurally (Go structural typing), so
// this package never imports the tools package — the seam stays decoupled (D2).
//
// D7 is enforced by the registry: every Execute validates the model-generated
// arguments against the compiled JSON Schema below (additionalProperties:
// false; the required prompt/id fields) before this code runs; the checks are
// repeated here so a direct call can never bypass them.
//
// D3 event logging follows the M5a-2 tool-layer decision (ADR 决策 ① 实施说明 /
// dispatch-m6d-2 §3): interact_ask emits interact/request on a successful
// create and interact_status emits interact/status on a lookup — all through
// the injected onEvent sink (the composition root wires it to the session log),
// and each append happens inside a tool Execute — the serial main-loop path
// (D5). interact/resolve and interact/deny are emitted by the wiring layer's
// sensitive-tool gate (see cmd/pa), not by a tool.
package interact

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jabing/shutu-agent/internal/session"
)

// Tool names (whitelisted when interact.enabled; see config.interactToolNames).
const (
	ToolAskName    = "interact_ask"
	ToolStatusName = "interact_status"
)

// InteractTools bundles the shared state of the two interact_* tools: the
// Engine service and the event sink.
type InteractTools struct {
	e       Engine
	onEvent func(typ string, data any)
}

// NewInteractTools returns the shared interact-tool bundle bound to an Engine.
// onEvent, when non-nil, receives the interact/* event payloads; the
// composition root wires it to the session log (D3).
func NewInteractTools(e Engine, onEvent func(typ string, data any)) *InteractTools {
	return &InteractTools{e: e, onEvent: onEvent}
}

// Ask returns the interact_ask tool.
func (t *InteractTools) Ask() InteractAskTool { return InteractAskTool{t: t} }

// Status returns the interact_status tool.
func (t *InteractTools) Status() InteractStatusTool { return InteractStatusTool{t: t} }

// emit forwards one interact/* event payload to the injected sink (D3).
func (t *InteractTools) emit(typ string, data any) {
	if t.onEvent != nil {
		t.onEvent(typ, data)
	}
}

// boundArgs trims args to the engine's stored-args bound (maxArgsLen runes,
// engine.go) so an over-long payload can never make Request fail on a tool the
// caller is legitimately invoking — the full args the model sees stay in the
// tool/result event, while the request row only carries the bounded projection.
func boundArgs(args string) string {
	runes := []rune(args)
	if len(runes) > maxArgsLen {
		return string(runes[:maxArgsLen])
	}
	return args
}

// InteractAskTool raises a question or approval request for the user and
// returns the request id plus its current status. The CLI interaction happens
// on the user's terminal; the tool returns without blocking, so the model
// continues and the user answers in their own time.
type InteractAskTool struct {
	t *InteractTools
}

func (InteractAskTool) Name() string { return ToolAskName }

func (InteractAskTool) Description() string {
	return "ask the user a question or request approval; returns the request id and its current status (the CLI interaction happens on the user's terminal, the model continues)"
}

func (InteractAskTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "the user-facing question or the sensitive action to approve",
			},
		},
		"required":             []string{"prompt"},
		"additionalProperties": false,
	}
}

func (t InteractAskTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("interact_ask: %w", err)
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return "", fmt.Errorf("interact_ask: prompt is required")
	}
	req, err := t.t.e.Request(ctx, a.Prompt, ToolAskName, boundArgs(string(args)))
	if err != nil {
		return "", fmt.Errorf("interact_ask: %w", err)
	}
	// interact/request is a log-only fact (D3); the created request's id and
	// triggering tool are logged, and the returned text is what the loop logs
	// as tool/result.
	t.t.emit(session.EventInteractRequest, session.NewInteractRequest(req.ID, req.ToolName))
	return fmt.Sprintf("created approval request %s (status=%s); the user will answer on their terminal", req.ID, req.Status), nil
}

// InteractStatusTool looks up the current approval status of one request.
type InteractStatusTool struct {
	t *InteractTools
}

func (InteractStatusTool) Name() string { return ToolStatusName }

func (InteractStatusTool) Description() string {
	return "look up the current approval status of one request by its id (pending | approved | rejected | expired)"
}

func (InteractStatusTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "the request id returned by interact_ask or shown by the approval gate",
			},
		},
		"required":             []string{"id"},
		"additionalProperties": false,
	}
}

func (t InteractStatusTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("interact_status: %w", err)
	}
	if a.ID == "" {
		return "", fmt.Errorf("interact_status: id is required")
	}
	all, err := t.t.e.List(ctx)
	if err != nil {
		return "", fmt.Errorf("interact_status: %w", err)
	}
	for _, r := range all {
		if r.ID == a.ID {
			// interact/status is a log-only fact (D3).
			t.t.emit(session.EventInteractStatus, session.NewInteractStatus(r.ID, string(r.Status)))
			return fmt.Sprintf("request %s: status=%s", r.ID, r.Status), nil
		}
	}
	return "", fmt.Errorf("interact_status: unknown request %s", a.ID)
}
