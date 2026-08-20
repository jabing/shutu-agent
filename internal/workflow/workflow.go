// Package workflow orchestrates a declarative task DAG over subagents
// (D-GAP-2). The model submits tasks[] — each with an id, a prompt, and
// depends_on — and the engine topologically sorts, spawns ready tasks
// concurrently (bounded), feeds each dependent the bounded outputs of its
// dependencies, and returns a per-task report. No JS engine: the JSON DAG is
// the declarative form (用户拍板). The engine only orchestrates; the spawned
// subagents do the work.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// DefaultMaxConcurrent is the ready-task concurrency cap (D-GAP-2).
const DefaultMaxConcurrent = 4

// depOutputMax bounds the dependency-output summary fed to a dependent.
const depOutputMax = 2000 // runes per dependency

// ErrCycle reports a dependency cycle detected before execution (fail-closed).
var ErrCycle = errors.New("workflow: dependency cycle")

// Spawn is the subagent-launch capability (D2: 组合根注入闭包). It starts one
// fresh subagent with the given prompt and returns its terminal output text.
type Spawn func(ctx context.Context, prompt string) (string, error)

// Task is one DAG node.
type Task struct {
	ID        string   `json:"id"`         // unique node id
	Prompt    string   `json:"prompt"`     // the subagent task prompt
	DependsOn []string `json:"depends_on"` // prerequisite task IDs (may be empty)
}

// Spec is the full workflow request.
type Spec struct {
	Tasks []Task
}

// TaskStatus is one task's terminal outcome.
type TaskStatus string

const (
	StatusCompleted TaskStatus = "completed" // spawned and produced output
	StatusFailed    TaskStatus = "failed"    // spawn produced an error
)

// TaskReport is the bounded per-task result.
type TaskReport struct {
	ID     string
	Status TaskStatus
	Output string // bounded (≤4000 runes); "" on failure
	Error  string // bounded (≤2000 runes); "" on success
}

// Report is the workflow result, in dependency order (topological).
type Report struct {
	Tasks []TaskReport
}

// Engine runs DAGs.
type Engine struct {
	spawn         Spawn
	maxConcurrent int
}

// NewEngine returns an engine bound to a spawn capability with the given
// concurrency cap (<=0 → DefaultMaxConcurrent). A nil spawn is rejected.
func NewEngine(spawn Spawn, maxConcurrent int) (*Engine, error) {
	if spawn == nil {
		return nil, fmt.Errorf("workflow: engine requires a spawn capability")
	}
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	return &Engine{spawn: spawn, maxConcurrent: maxConcurrent}, nil
}

// Run validates the spec (empty tasks / duplicate ids / unknown depends_on
// fail closed), topologically sorts the DAG (a cycle → ErrCycle), then
// executes it layer by layer: every task with no unsatisfied dependency in one
// layer is spawned concurrently, bounded by the engine's concurrency cap. A
// failed dependency never blocks its dependents — they run with a "依赖 <id>
// 失败" note instead of the output summary. On ctx cancellation the engine
// stops scheduling further tasks and returns the reports collected so far
// (partial recovery) together with ctx.Err().
func (e *Engine) Run(ctx context.Context, spec Spec) (Report, error) {
	if len(spec.Tasks) == 0 {
		return Report{}, fmt.Errorf("workflow: no tasks")
	}
	byID := make(map[string]Task, len(spec.Tasks))
	for _, t := range spec.Tasks {
		if strings.TrimSpace(t.ID) == "" {
			return Report{}, fmt.Errorf("workflow: task id must be non-empty")
		}
		if _, dup := byID[t.ID]; dup {
			return Report{}, fmt.Errorf("workflow: duplicate task id %q", t.ID)
		}
		byID[t.ID] = t
	}
	for _, t := range spec.Tasks {
		for _, d := range t.DependsOn {
			if _, ok := byID[d]; !ok {
				return Report{}, fmt.Errorf("workflow: task %q depends on unknown task %q", t.ID, d)
			}
		}
	}

	order, err := topoSort(spec.Tasks)
	if err != nil {
		return Report{}, err
	}
	layers := buildLayers(spec.Tasks, byID, order)

	reports := make(map[string]TaskReport, len(spec.Tasks))
	completed := make(map[string]string, len(spec.Tasks)) // id → bounded output
	failed := make(map[string]bool, len(spec.Tasks))      // id → spawn failed

	for _, layer := range layers {
		if err := ctx.Err(); err != nil {
			return buildReport(order, reports), err
		}
		// The layer's tasks run concurrently, bounded by the semaphore
		// (maxConcurrent). The spawn capability observes ctx, so an in-flight
		// spawn returns promptly once ctx is cancelled; scheduling stops and
		// the completed reports are preserved (partial recovery).
		sem := make(chan struct{}, e.maxConcurrent)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var stop bool
		for _, id := range layer {
			if stop {
				break
			}
			t := byID[id]
			prompt := buildTaskPrompt(t, completed, failed)
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				stop = true
				break
			}
			if stop {
				break
			}
			wg.Add(1)
			go func(t Task, prompt string) {
				defer wg.Done()
				defer func() { <-sem }()
				rep := e.runTask(ctx, t, prompt)
				mu.Lock()
				reports[t.ID] = rep
				if rep.Status == StatusCompleted {
					completed[t.ID] = rep.Output
				} else {
					failed[t.ID] = true
				}
				mu.Unlock()
			}(t, prompt)
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return buildReport(order, reports), err
		}
	}
	return buildReport(order, reports), nil
}

// runTask spawns one task and produces its bounded report. A spawn error fails
// the task (bounded error text); the dependents still run with a failure note.
func (e *Engine) runTask(ctx context.Context, t Task, prompt string) TaskReport {
	out, err := e.spawn(ctx, prompt)
	if err != nil {
		return TaskReport{ID: t.ID, Status: StatusFailed, Error: boundRunes(err.Error(), 2000)}
	}
	return TaskReport{ID: t.ID, Status: StatusCompleted, Output: boundRunes(out, 4000)}
}

// topoSort returns the task ids in dependency order (dependencies first) via
// Kahn's algorithm, or ErrCycle when the DAG contains a cycle (fail-closed).
// The traversal is deterministic: zero-indegree ids are dequeued in the task
// order they were declared.
func topoSort(tasks []Task) ([]string, error) {
	adj := make(map[string][]string, len(tasks))
	indeg := make(map[string]int, len(tasks))
	for _, t := range tasks {
		indeg[t.ID] = 0
	}
	for _, t := range tasks {
		for _, d := range t.DependsOn {
			adj[d] = append(adj[d], t.ID)
			indeg[t.ID]++
		}
	}
	var queue []string
	for _, t := range tasks {
		if indeg[t.ID] == 0 {
			queue = append(queue, t.ID)
		}
	}
	order := make([]string, 0, len(tasks))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		for _, next := range adj[id] {
			indeg[next]--
			if indeg[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if len(order) != len(tasks) {
		return nil, ErrCycle
	}
	return order, nil
}

// buildLayers groups the topologically-sorted task ids into execution layers:
// layer 0 holds the tasks with no dependencies, and a task's layer is one more
// than the deepest layer of its dependencies. Every task in a layer has all of
// its dependencies satisfied once the previous layers completed.
func buildLayers(tasks []Task, byID map[string]Task, order []string) [][]string {
	layer := make(map[string]int, len(tasks))
	for _, id := range order {
		l := 0
		for _, d := range byID[id].DependsOn {
			if layer[d]+1 > l {
				l = layer[d] + 1
			}
		}
		layer[id] = l
	}
	maxL := 0
	for _, v := range layer {
		if v > maxL {
			maxL = v
		}
	}
	out := make([][]string, maxL+1)
	for _, id := range order {
		l := layer[id]
		out[l] = append(out[l], id)
	}
	return out
}

// buildTaskPrompt renders the prompt a task's spawn receives: the task's own
// prompt plus, per dependency in depends_on order, a bounded output summary (or
// a "依赖 <id> 失败" note when that dependency's spawn failed). The header only
// appears when the task has dependencies.
func buildTaskPrompt(t Task, completed map[string]string, failed map[string]bool) string {
	var sb strings.Builder
	sb.WriteString(t.Prompt)
	if len(t.DependsOn) == 0 {
		return sb.String()
	}
	sb.WriteString("\n\n（依赖任务输出摘要）")
	for _, d := range t.DependsOn {
		sb.WriteString("\n")
		sb.WriteString(d)
		sb.WriteString(":\n")
		if failed[d] {
			fmt.Fprintf(&sb, "（依赖 %s 失败）", d)
			continue
		}
		if out, ok := completed[d]; ok {
			sb.WriteString(boundRunes(out, depOutputMax))
		} else {
			fmt.Fprintf(&sb, "（依赖 %s 无输出）", d)
		}
	}
	return sb.String()
}

// buildReport assembles the Report in topological (dependency) order from the
// per-task reports recorded so far.
func buildReport(order []string, reports map[string]TaskReport) Report {
	out := make([]TaskReport, 0, len(order))
	for _, id := range order {
		if r, ok := reports[id]; ok {
			out = append(out, r)
		}
	}
	return Report{Tasks: out}
}

// boundRunes truncates s to at most max runes (append "…" when cut). A
// non-positive max yields an empty string.
func boundRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}
