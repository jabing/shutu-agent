// tools.go — the GAP-3 Consumer half of the workflow seam (ADR
// 2026-08-20-standard-gaps.md D-GAP-2, 用户拍板 JSON DAG 声明式编排): the
// workflow_run tool is registered into the tools.Registry by the composition
// root (cmd/pa) when workflow.enabled, and auto-whitelisted by
// config.applyDefaults the same way the ralph/fs_search tools are. It
// implements the tools.Tool method set structurally (Go structural typing), so
// this package never imports the tools package — the seam stays decoupled. D7
// is enforced by the registry. D3 event logging lives here: a settled workflow
// run emits workflow/run.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"personal-agent/internal/session"
)

// WorkflowRunToolName is the task-DAG orchestration tool name (D-GAP-2).
const WorkflowRunToolName = "workflow_run"

// WorkflowRunTool bundles the workflow_run tool's shared state: the Engine it
// drives and the D3 event sink.
type WorkflowRunTool struct {
	eng     *Engine
	onEvent func(typ string, data any)
}

// NewWorkflowRunTool returns the workflow_run tool bound to an Engine. onEvent,
// when non-nil, receives the workflow/run payload; the composition root wires
// it to the session log (D3).
func NewWorkflowRunTool(eng *Engine, onEvent func(typ string, data any)) *WorkflowRunTool {
	return &WorkflowRunTool{eng: eng, onEvent: onEvent}
}

func (t *WorkflowRunTool) emit(typ string, data any) {
	if t.onEvent != nil {
		t.onEvent(typ, data)
	}
}

func (WorkflowRunTool) Name() string { return WorkflowRunToolName }

func (WorkflowRunTool) Description() string {
	return "提交任务 DAG，并发编排多个子代理（依赖在先后执行），返回逐任务结果"
}

func (WorkflowRunTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":     map[string]any{"type": "string", "minLength": 1},
						"prompt": map[string]any{"type": "string", "minLength": 1},
						"depends_on": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "string"},
						},
					},
					"required":             []string{"id", "prompt"},
					"additionalProperties": false,
				},
				"minItems":    1,
				"description": "task DAG nodes (unique ids; depends_on lists prerequisite ids)",
			},
		},
		"required":             []string{"tasks"},
		"additionalProperties": false,
	}
}

// Execute runs the model-submitted task DAG through the engine and renders the
// per-task summary. An empty tasks array is rejected; engine-level errors
// (ErrCycle, validation) are wrapped and passed through. The workflow/run
// event carries only the counts (D3 — 只记元数据, 不落输出全文).
func (t *WorkflowRunTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Tasks []Task `json:"tasks"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("workflow_run: %w", err)
	}
	if len(a.Tasks) == 0 {
		return "", fmt.Errorf("workflow_run: empty tasks")
	}
	rep, err := t.eng.Run(ctx, Spec{Tasks: a.Tasks})
	if err != nil {
		return "", fmt.Errorf("workflow_run: %w", err)
	}
	completed, failed := 0, 0
	for _, tr := range rep.Tasks {
		if tr.Status == StatusCompleted {
			completed++
		} else {
			failed++
		}
	}
	t.emit(session.EventWorkflowRun, session.NewWorkflowRun(len(rep.Tasks), completed, failed))
	return formatReport(rep), nil
}

// formatReport renders the per-task summary: a header with the task count and
// one indented block per task — id, status, and a bounded output (completed)
// or error (failed) line.
func formatReport(rep Report) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "workflow_run: %d tasks", len(rep.Tasks))
	for _, tr := range rep.Tasks {
		fmt.Fprintf(&sb, "\n  %s: %s", tr.ID, tr.Status)
		if tr.Status == StatusCompleted {
			fmt.Fprintf(&sb, "\n    output: %s", boundRunes(tr.Output, 400))
		} else {
			fmt.Fprintf(&sb, "\n    error: %s", boundRunes(tr.Error, 400))
		}
	}
	return sb.String()
}
