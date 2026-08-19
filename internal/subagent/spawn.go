// SpawnProvider is the default in-process subagent provider (ADR
// 2026-08-18-m5-agent-core.md 决策 ②: spawn-in-process). A spawn creates a
// brand-new independent child session and a brand-new independent loop instance
// to drive the child agent — the loop is a library, instantiated once per child
// (D4: the child is just "another session + another loop instance", composed
// here, never by modifying the loop). The parent_session lineage and the
// delegation depth are tracked in the provider's in-memory registry; the child
// session log is independent, so the parent session log is never polluted.
package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"personal-agent/internal/llm"
	"personal-agent/internal/loop"
	"personal-agent/internal/prompt"
	"personal-agent/internal/session"
	"personal-agent/internal/tools"
)

// Deps wires the SpawnProvider to the core components it reuses for every child
// (the composition root supplies them at construction, M5b-2 wiring; tests use
// a fake LLM). Log is the parent/host session log the provider is bound to —
// it is never appended to by the provider (the child owns an independent log);
// M5b-2's subagent/* event recording will surface the parent lineage through
// it. Each spawned child gets its own fresh session.New() log.
type Deps struct {
	Log    *session.Log
	LLM    llm.LLM
	Tools  *tools.Registry
	Prompt *prompt.Builder
	Model  string
}

// SpawnProvider spawns a brand-new child session + child loop for every Start.
// It is safe for concurrent use.
type SpawnProvider struct {
	deps Deps

	mu       sync.Mutex
	children map[string]*childRun
	nextID   int
	closed   bool
}

// childRun is the provider's per-child record: the independent child log, the
// parent lineage, the delegation depth, and the settle/cancel bookkeeping. It
// is never handed out; callers receive fresh values (Run closures,
// ChildSummary, ChildLog).
type childRun struct {
	id     string
	label  string
	parent string
	depth  int
	log    *session.Log
	cancel context.CancelFunc // cancels the child loop context (set in Start)
	done   chan struct{}      // closed once the child settles

	mu           sync.Mutex
	cancelReason string
	result       Result
	settled      bool
}

// NewSpawnProvider returns a SpawnProvider bound to the given core components.
func NewSpawnProvider(deps Deps) *SpawnProvider {
	return &SpawnProvider{deps: deps, children: map[string]*childRun{}}
}

// Name returns the provider name ("spawn"), the default subagent provider.
func (p *SpawnProvider) Name() string { return "spawn" }

// Capabilities declares what the spawn provider actually enforces: delegation
// depth (MaxDepth ⇒ ErrDepthExceeded). ToolFilter/Persona application and
// structured output are M5b-2 / later wiring, so they are not declared.
func (p *SpawnProvider) Capabilities() Capabilities {
	return Capabilities{DepthLimit: true}
}

// Start registers a brand-new child session (depth = parent depth + 1, tracked
// in the provider's registry), rejects an over-deep spawn, and drives the child
// with a fresh loop instance in a background goroutine. It does not block: the
// returned Run's Result awaits the terminal outcome. The child loop runs on an
// independent context, so it outlives Start's ctx; Cancel/Close cancel it.
func (p *SpawnProvider) Start(ctx context.Context, req StartRequest) (*Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.Prompt == "" {
		return nil, fmt.Errorf("%w: prompt is required", ErrInvalidRequest)
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrProviderClosed
	}
	parentDepth := 0
	if parent, ok := p.children[req.ParentSessionID]; ok {
		parentDepth = parent.depth
	}
	depth := parentDepth + 1
	if req.MaxDepth > 0 && depth > req.MaxDepth {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: depth %d exceeds max depth %d (parent %q)",
			ErrDepthExceeded, depth, req.MaxDepth, req.ParentSessionID)
	}
	p.nextID++
	id := fmt.Sprintf("spawn-%d", p.nextID)
	runCtx, cancel := context.WithCancel(context.Background())
	child := &childRun{
		id:     id,
		label:  req.Label,
		parent: req.ParentSessionID,
		depth:  depth,
		log:    session.New(),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	p.children[id] = child
	p.mu.Unlock()

	go p.runChild(child, req, runCtx)
	return &Run{
		ID:     id,
		Result: p.resultFunc(child),
		Cancel: p.cancelFunc(child),
	}, nil
}

// runChild drives one child agent to completion against its own independent
// log with its own loop instance, then settles the child's result. A panic is
// contained so a misbehaving child can never crash the provider (fail-open).
func (p *SpawnProvider) runChild(child *childRun, req StartRequest, runCtx context.Context) {
	defer close(child.done)
	defer func() {
		if r := recover(); r != nil {
			p.settle(child, Result{StopReason: StopError})
		}
	}()
	lp := loop.New(loop.Config{
		LLM:    p.deps.LLM,
		Log:    child.log,
		Tools:  p.deps.Tools,
		Prompt: p.deps.Prompt,
		Model:  p.deps.Model,
	})
	runErr := lp.Run(runCtx, req.Prompt)
	p.settle(child, p.deriveResult(child, runErr))
}

// settle records the first terminal result for a child. First-wins: a Close
// force-settle racing the child's own outcome is ignored.
func (p *SpawnProvider) settle(child *childRun, res Result) {
	child.mu.Lock()
	if !child.settled {
		child.result = res
		child.settled = true
	}
	child.mu.Unlock()
}

// deriveResult maps the child loop's outcome to the subagent Result (ADR 决策
// ②: completed | aborted | error | max-tokens | refusal). Output is the child's
// last non-empty assistant/message text, derived from the child's own log (D1).
func (p *SpawnProvider) deriveResult(child *childRun, runErr error) Result {
	last := lastAssistantEvent(child.log)
	child.mu.Lock()
	cancelled := child.cancelReason != ""
	child.mu.Unlock()
	switch {
	case cancelled:
		return Result{Output: last.text, StopReason: StopAborted}
	case runErr != nil:
		return Result{Output: last.text, StopReason: StopError}
	}
	return Result{Output: last.text, StopReason: mapStopReason(last.finishReason)}
}

// resultFunc returns the Run.Result closure for a child: it blocks until the
// child settles (or ctx is cancelled) and returns the terminal outcome.
func (p *SpawnProvider) resultFunc(child *childRun) func(ctx context.Context) (Result, error) {
	return func(ctx context.Context) (Result, error) {
		select {
		case <-child.done:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
		child.mu.Lock()
		defer child.mu.Unlock()
		return child.result, nil
	}
}

// cancelFunc returns the Run.Cancel closure for a child: it records the reason
// and cancels the child's loop context (synchronous and idempotent; a second
// cancel on the same live child is a no-op). It fails once the child has
// settled.
func (p *SpawnProvider) cancelFunc(child *childRun) func(reason string) error {
	return func(reason string) error {
		child.mu.Lock()
		if child.settled {
			child.mu.Unlock()
			return fmt.Errorf("subagent: %s: already finished", child.id)
		}
		if child.cancelReason == "" {
			child.cancelReason = reason
		}
		cancel := child.cancel
		child.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil
	}
}

// ListChildren returns a projection of every child this provider spawned under
// parentSessionID, sorted by id.
func (p *SpawnProvider) ListChildren(ctx context.Context, parentSessionID string) ([]ChildSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []ChildSummary
	for _, c := range p.children {
		if c.parent != parentSessionID {
			continue
		}
		c.mu.Lock()
		running := !c.settled
		c.mu.Unlock()
		out = append(out, ChildSummary{ID: c.id, Label: c.label, Running: running})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ChildLog returns the independent session log of a spawned child (for M5b-2
// wiring that inspects/persists a child session, and for tests asserting the
// child session is complete and replayable). The second return reports whether
// the child exists.
func (p *SpawnProvider) ChildLog(id string) (*session.Log, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.children[id]
	if !ok {
		return nil, false
	}
	return c.log, true
}

// Close cancels every live child and waits for all children to settle, so no
// background goroutine leaks (lifecycle reversible, ADR 决策 ②). Start after
// Close is rejected; Close is idempotent.
func (p *SpawnProvider) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	children := make([]*childRun, 0, len(p.children))
	for _, c := range p.children {
		children = append(children, c)
	}
	p.mu.Unlock()

	for _, c := range children {
		c.mu.Lock()
		if !c.settled && c.cancelReason == "" {
			c.cancelReason = "provider closed"
		}
		cancel := c.cancel
		c.mu.Unlock()
		if cancel != nil {
			cancel() // no-op for an already-settled child's context
		}
	}
	for _, c := range children {
		<-c.done
	}
	return nil
}

// assistantEvent is the derived projection of the most recent assistant/message
// row of a child session log.
type assistantEvent struct {
	text         string
	finishReason string
}

// lastAssistantEvent scans a child session log for the most recent
// assistant/message row, returning the last non-empty text and the finish
// reason (D1: the log is the source of truth — the result is derived, never
// stored separately).
func lastAssistantEvent(log *session.Log) assistantEvent {
	var ev assistantEvent
	for _, e := range log.Events() {
		if e.Type != session.EventAssistantMessage {
			continue
		}
		var d struct {
			Text         string `json:"text"`
			FinishReason string `json:"finishReason"`
		}
		if json.Unmarshal(e.Data, &d) != nil {
			continue
		}
		if d.Text != "" {
			ev.text = d.Text
		}
		ev.finishReason = d.FinishReason
	}
	return ev
}

// mapStopReason maps a model finish reason onto the subagent StopReason
// vocabulary (ADR 决策 ②). "length" is DeepSeek's max-token finish;
// content-filter finishes map to refusal. Anything else on a clean completion
// is completed.
func mapStopReason(finishReason string) string {
	switch finishReason {
	case "length", "max_tokens":
		return StopMaxTokens
	case "content_filter", "refusal":
		return StopRefusal
	default:
		return StopCompleted
	}
}
