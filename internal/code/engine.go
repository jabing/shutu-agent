package code

import (
	"context"
	"sync"
)

// engine is the default code-sandbox Service (ADR 决策 M6e): it owns the
// closed state and delegates execution to a Provider. The unexported concrete
// type (mirroring the schedule seam's engine) keeps the Engine interface the
// only public shape; NewEngine returns it as a concrete *engine that satisfies
// Engine.
type engine struct {
	prov Provider

	mu     sync.Mutex
	closed bool
}

// NewEngine returns an engine backed by prov; a nil prov selects the default
// local subprocess sandbox (NewLocalProvider). Each engine should own its
// provider: Close releases it.
func NewEngine(prov Provider) *engine {
	if prov == nil {
		prov = newLocalProvider()
	}
	return &engine{prov: prov}
}

// Run executes req through the Provider. A non-zero exit and a timeout are
// normal outcomes reported in Result; the error return signals the run did not
// happen (closed engine, cancelled context, or a provider failure).
func (e *engine) Run(ctx context.Context, req RunRequest) (Result, error) {
	if err := e.checkOpen(); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return e.prov.Run(ctx, req)
}

// checkOpen rejects operations on a closed engine.
func (e *engine) checkOpen() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrEngineClosed
	}
	return nil
}

// Close releases the Provider (if it implements closer) and marks the engine
// closed so further Run calls are rejected with ErrEngineClosed. It is
// idempotent.
func (e *engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	prov := e.prov
	e.mu.Unlock()
	if c, ok := prov.(closer); ok {
		return c.Close()
	}
	return nil
}
