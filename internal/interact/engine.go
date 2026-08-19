package interact

import (
	"context"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// maxArgsLen bounds the stored args JSON in runes. An over-long payload is
	// rejected at Request time so a request can never grow unbounded.
	maxArgsLen = 200
	// defaultPendingLimit caps the number of concurrently pending requests
	// (default 20); a Request that would exceed it is rejected.
	defaultPendingLimit = 20
	// defaultPollInterval is the Await poll cadence: a short sleep between List
	// probes. The caller drives Await on its own serial path, so the interval
	// trades a little latency for zero background machinery (D5).
	defaultPollInterval = 100 * time.Millisecond
)

// engine is the default approval Service implementation (ADR 决策 M6d): it owns
// validation — prompt legality, args bound, status legality, duplicate-resolution
// rejection, the pending cap — delegating storage to a Provider. It is safe for
// concurrent use; Close is idempotent and releases the Provider. The unexported
// concrete type keeps the Engine interface the only public shape; NewEngine
// returns it as a concrete *engine that satisfies Engine.
type engine struct {
	prov Provider

	mu           sync.Mutex
	closed       bool
	pendingLimit int
	poll         time.Duration
}

// NewEngine returns an engine backed by prov; a nil prov selects the default
// in-memory Provider (newMemProvider). Each engine should own its provider:
// Close releases it.
func NewEngine(prov Provider) *engine {
	if prov == nil {
		prov = newMemProvider()
	}
	return &engine{
		prov:         prov,
		pendingLimit: defaultPendingLimit,
		poll:         defaultPollInterval,
	}
}

// Request validates the prompt and args, applies the pending cap, and creates a
// pending request through the Provider, returning it with its provider-issued
// id.
func (e *engine) Request(ctx context.Context, prompt, toolName, args string) (Request, error) {
	if err := ctx.Err(); err != nil {
		return Request{}, err
	}
	if err := e.checkOpen(); err != nil {
		return Request{}, err
	}
	if prompt == "" {
		return Request{}, fmt.Errorf("%w: prompt is empty", ErrInvalidPrompt)
	}
	if utf8.RuneCountInString(args) > maxArgsLen {
		return Request{}, fmt.Errorf("%w: args exceed %d runes", ErrInvalidArgs, maxArgsLen)
	}
	all, err := e.prov.List(ctx)
	if err != nil {
		return Request{}, err
	}
	pending := 0
	for _, r := range all {
		if r.Status == StatusPending {
			pending++
		}
	}
	if pending >= e.pendingLimit {
		return Request{}, fmt.Errorf("%w: %d pending", ErrPendingLimit, e.pendingLimit)
	}
	return e.prov.Create(ctx, Request{
		Prompt:    prompt,
		ToolName:  toolName,
		Args:      args,
		Status:    StatusPending,
		CreatedAt: time.Now(),
	})
}

// Resolve records the user's decision (approved or rejected) for the request
// with id. An unknown id, a request already resolved and an invalid status are
// rejected; the resolved request is returned.
func (e *engine) Resolve(ctx context.Context, id string, status ApprovalStatus) (Request, error) {
	if err := ctx.Err(); err != nil {
		return Request{}, err
	}
	if err := e.checkOpen(); err != nil {
		return Request{}, err
	}
	if status != StatusApproved && status != StatusRejected {
		return Request{}, fmt.Errorf("%w: %q", ErrInvalidStatus, status)
	}
	r, err := e.findRequest(ctx, id)
	if err != nil {
		return Request{}, err
	}
	if r.Status != StatusPending {
		return Request{}, fmt.Errorf("%w: %s", ErrAlreadyResolved, id)
	}
	if err := e.prov.Resolve(ctx, id, status); err != nil {
		return Request{}, err
	}
	// Read the stored copy back so the returned request matches the record the
	// Provider persists (its ResolvedAt timestamp in particular).
	return e.findRequest(ctx, id)
}

// Await blocks until the request with id leaves pending — a Resolve made by the
// user, a context cancellation or a disappearing request. v1 has no background
// wait (D5): Await polls the Provider on a short interval, so a resolution made
// in another goroutine becomes visible on the next poll. An unknown id fails
// fast.
func (e *engine) Await(ctx context.Context, id string) (Request, error) {
	if err := ctx.Err(); err != nil {
		return Request{}, err
	}
	if err := e.checkOpen(); err != nil {
		return Request{}, err
	}
	timer := time.NewTimer(e.poll)
	defer timer.Stop()
	for {
		r, err := e.findRequest(ctx, id)
		if err != nil {
			return Request{}, err
		}
		if r.Status != StatusPending {
			return r, nil
		}
		timer.Reset(e.poll)
		select {
		case <-ctx.Done():
			return Request{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// List returns every current request, sorted by id.
func (e *engine) List(ctx context.Context) ([]Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.checkOpen(); err != nil {
		return nil, err
	}
	return e.prov.List(ctx)
}

// findRequest locates the request with id through the Provider; an unknown id
// is rejected.
func (e *engine) findRequest(ctx context.Context, id string) (Request, error) {
	all, err := e.prov.List(ctx)
	if err != nil {
		return Request{}, err
	}
	for _, r := range all {
		if r.ID == id {
			return r, nil
		}
	}
	return Request{}, fmt.Errorf("%w: %s", ErrUnknownRequest, id)
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

// Close releases the backend (if it implements closer) and marks the engine
// closed so every other operation is rejected. It is idempotent.
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
