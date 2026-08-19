package interact

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// memProvider is the default in-memory Provider (ADR 决策 M6d). Every request
// lives in memory only — nothing is persisted and no files are touched — so a
// process restart clears the approval table by construction. It is safe for
// concurrent use and performs no validation beyond the seam's invariants: the
// Engine is the validation boundary, and Create/Resolve are called through by
// the Engine. A store-backed Provider can replace it without touching Engine or
// consumer code.
type memProvider struct {
	mu       sync.Mutex
	requests map[string]Request
	nextID   int
	closed   bool
}

// NewMemProvider returns a fresh in-memory Provider — the default backend for
// NewEngine. It is exported so wiring and tests can inject it explicitly or
// preload requests with controlled timestamps.
func NewMemProvider() Provider {
	return newMemProvider()
}

func newMemProvider() *memProvider {
	return &memProvider{requests: map[string]Request{}}
}

// Name identifies the provider in the registry ("memory").
func (m *memProvider) Name() string { return "memory" }

// List returns every current request, sorted by id.
func (m *memProvider) List(ctx context.Context) ([]Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrProviderClosed
	}
	out := make([]Request, 0, len(m.requests))
	for _, r := range m.requests {
		out = append(out, cloneRequest(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Create stores r under a fresh provider-issued id and returns the stored copy.
func (m *memProvider) Create(ctx context.Context, r Request) (Request, error) {
	if err := ctx.Err(); err != nil {
		return Request{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Request{}, ErrProviderClosed
	}
	m.nextID++
	r.ID = fmt.Sprintf("req-%d", m.nextID)
	m.requests[r.ID] = cloneRequest(r)
	return cloneRequest(r), nil
}

// Resolve marks the request with id as resolved with status. An unknown id and
// a request that is no longer pending are rejected; the stored record's
// ResolvedAt is stamped at resolution time.
func (m *memProvider) Resolve(ctx context.Context, id string, status ApprovalStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrProviderClosed
	}
	r, ok := m.requests[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownRequest, id)
	}
	if r.Status != StatusPending {
		return fmt.Errorf("%w: %s", ErrAlreadyResolved, id)
	}
	now := time.Now()
	r.Status = status
	r.ResolvedAt = &now
	m.requests[id] = r
	return nil
}

// Close marks the provider closed so no further operations are accepted. It is
// idempotent and releases nothing else (no goroutines live here).
func (m *memProvider) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// cloneRequest copies a Request so the returned value never aliases the
// record's ResolvedAt pointer.
func cloneRequest(r Request) Request {
	if r.ResolvedAt != nil {
		t := *r.ResolvedAt
		r.ResolvedAt = &t
	}
	return r
}
