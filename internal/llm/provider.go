// Provider interface + multi-provider Registry (M8-2, ADR
// 2026-08-20-m8-message-model.md D2 / dispatch-m8-2 §2). The LLM seam is the
// D2 三件套: an interface (Provider), a registry (Registry), and consumers
// (loop/compaction/subagent) that depend only on the resolved llm.LLM — the
// composition root picks one provider and injects it, so swapping providers
// never touches a consumer.
package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Provider is an LLM backend (D2). Consumers (loop/composition root) depend
// only on this interface, never on a concrete provider.
type Provider interface {
	// ID returns the stable provider id ("deepseek" / "openai" / "anthropic").
	ID() string
	// Available reports whether the provider can be used: a cheap local check
	// (key/endpoint resolvable) that never performs a network call
	// (dispatch-m8-2 §2).
	Available() bool
	// Stream starts a chat request and returns an incremental reader honoring
	// ctx cancellation (D6).
	Stream(ctx context.Context, req ChatRequest) (StreamReader, error)
}

// Registry is the multi-provider registry (D2). Providers are registered by
// stable id at wiring time and selected by the composition root; consumers
// never see the registry (they hold the resolved provider via the LLM
// interface). Registration happens during wiring and selection during startup,
// both on the serial path (D5); the RWMutex guards against any future
// concurrent reader, mirroring web.Engine.
type Registry struct {
	mu    sync.RWMutex
	byID  map[string]Provider
	order []Provider
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{byID: make(map[string]Provider)}
}

// Register adds p under its stable id; a duplicate or empty id is an error.
func (r *Registry) Register(p Provider) error {
	if p == nil {
		return errors.New("llm: nil provider")
	}
	id := p.ID()
	if id == "" {
		return errors.New("llm: provider with empty id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[id]; dup {
		return fmt.Errorf("llm: duplicate provider id %q", id)
	}
	r.byID[id] = p
	r.order = append(r.order, p)
	return nil
}

// Get returns the provider registered under id, or an error when absent.
func (r *Registry) Get(id string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("llm: no such provider %q", id)
	}
	return p, nil
}

// List returns every registered provider in registration order (a copy, so
// callers never alias the registry's internal slice).
func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, len(r.order))
	copy(out, r.order)
	return out
}
