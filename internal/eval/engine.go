package eval

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Sentinel errors.
var (
	ErrEngineClosed   = errors.New("eval: engine closed")
	ErrUnknownRecord  = errors.New("eval: unknown record")
	ErrProviderClosed = errors.New("eval: provider closed")
)

// recordOutputMax bounds the stored deliverable summary (D-EVAL-1).
const recordOutputMax = 4000

// boundRunes truncates s to at most max runes (append "…" when cut).
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

// Provider is one evaluation-record backend (D-EVAL-2): a dumb store the
// Engine calls through. Callers receive fresh value copies, never live state.
type Provider interface {
	Name() string
	// List returns every record, most recent first.
	List(ctx context.Context) ([]EvalRecord, error)
	// Get returns the record with id; an unknown id is rejected.
	Get(ctx context.Context, id string) (EvalRecord, error)
	// Put stores a record, returning it with any provider-issued id filled.
	Put(ctx context.Context, r EvalRecord) (EvalRecord, error)
}

// Engine is the evaluation Service (D-EVAL-2). Consumers depend only on this
// interface. Lifecycle: Evaluate judges a deliverable and records it; List/Get
// observe the history; Close releases the backend. Close is idempotent.
type Engine interface {
	// Evaluate runs the configured Evaluator over (output, criteria) and
	// records the outcome under a fresh engine-issued id ("eval-N"), bounding
	// the stored Output to recordOutputMax runes. An Evaluator error is
	// returned without recording.
	Evaluate(ctx context.Context, taskID, output string, criteria []string) (EvalRecord, error)
	// List returns every record, most recent first.
	List(ctx context.Context) ([]EvalRecord, error)
	// Get returns the record with id; an unknown id is rejected.
	Get(ctx context.Context, id string) (EvalRecord, error)
	// Close releases the backend and marks the engine closed. It is idempotent;
	// every other operation after Close is rejected with ErrEngineClosed.
	Close() error
}

// EngineOpts configures NewEngine.
type EngineOpts struct {
	Evaluator  Evaluator
	MaxRecords int // >0 caps stored history, evicting oldest; 0 → default (100)
}

// NewEngine builds an Engine backed by the default in-memory Provider.
// opts.Evaluator is required; a nil Evaluator is rejected. MaxRecords<=0
// selects the default cap of 100.
func NewEngine(opts EngineOpts) (Engine, error) {
	if opts.Evaluator == nil {
		return nil, errors.New("eval: engine requires an evaluator")
	}
	max := opts.MaxRecords
	if max <= 0 {
		max = 100
	}
	return &evalEngine{
		eval: opts.Evaluator,
		prov: newMemProvider(max),
		next: 1,
	}, nil
}

// evalEngine is the Engine's concrete implementation over a Provider.
type evalEngine struct {
	eval   Evaluator
	prov   Provider
	next   uint64
	mu     sync.Mutex
	closed bool
}

func (e *evalEngine) Evaluate(ctx context.Context, taskID, output string, criteria []string) (EvalRecord, error) {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return EvalRecord{}, ErrEngineClosed
	}

	verdict, reason, kind, err := e.eval.Evaluate(ctx, output, criteria)
	if err != nil {
		return EvalRecord{}, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return EvalRecord{}, ErrEngineClosed
	}
	rec := EvalRecord{
		ID:            fmt.Sprintf("eval-%d", e.next),
		TaskID:        taskID,
		Criteria:      append([]string(nil), criteria...),
		Output:        boundRunes(output, recordOutputMax),
		Verdict:       verdict,
		Reason:        reason,
		EvaluatorKind: kind,
		CreatedAt:     time.Now(),
	}
	e.next++
	return e.prov.Put(ctx, rec)
}

func (e *evalEngine) List(ctx context.Context) ([]EvalRecord, error) {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return nil, ErrEngineClosed
	}
	return e.prov.List(ctx)
}

func (e *evalEngine) Get(ctx context.Context, id string) (EvalRecord, error) {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return EvalRecord{}, ErrEngineClosed
	}
	return e.prov.Get(ctx, id)
}

func (e *evalEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	// memProvider holds no resources; other backends would release here.
	return nil
}

// memProvider is the default Provider: in-memory, most-recent-first ordering,
// capped at maxRecords (evicting the oldest on overflow).
type memProvider struct {
	mu      sync.Mutex
	records map[string]EvalRecord
	order   []string // insertion order, oldest first
	max     int
}

func newMemProvider(max int) *memProvider {
	return &memProvider{
		records: make(map[string]EvalRecord),
		max:     max,
	}
}

func (p *memProvider) Name() string { return "mem" }

func (p *memProvider) List(ctx context.Context) ([]EvalRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]EvalRecord, 0, len(p.order))
	for i := len(p.order) - 1; i >= 0; i-- {
		out = append(out, p.records[p.order[i]])
	}
	return out, nil
}

func (p *memProvider) Get(ctx context.Context, id string) (EvalRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.records[id]
	if !ok {
		return EvalRecord{}, ErrUnknownRecord
	}
	return r, nil
}

func (p *memProvider) Put(ctx context.Context, r EvalRecord) (EvalRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.records[r.ID]; ok {
		// Existing record: replace in place, keeping its insertion position.
		p.records[r.ID] = r
		return r, nil
	}
	p.records[r.ID] = r
	p.order = append(p.order, r.ID)
	if p.max > 0 && len(p.order) > p.max {
		oldest := p.order[0]
		p.order = p.order[1:]
		delete(p.records, oldest)
	}
	return r, nil
}
