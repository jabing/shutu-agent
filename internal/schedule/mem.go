package schedule

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// memProvider is the default in-memory Provider (ADR 决策 M6a). Every schedule
// lives in memory only — nothing is persisted and no files are touched — so a
// process restart clears the schedule table by construction. It is safe for
// concurrent use and performs no validation: the Engine is the validation
// boundary (Create/Update/Delete are called through by the Engine).
type memProvider struct {
	mu        sync.Mutex
	schedules map[string]Schedule
	nextID    int
	closed    bool
}

// NewMemProvider returns a fresh in-memory Provider — the default backend for
// NewEngine. It is exported so wiring and tests can inject it explicitly or
// preload schedules with controlled timestamps.
func NewMemProvider() Provider {
	return newMemProvider()
}

func newMemProvider() *memProvider {
	return &memProvider{schedules: map[string]Schedule{}}
}

// Name identifies the provider in the registry ("memory").
func (m *memProvider) Name() string { return "memory" }

// List returns every current schedule, sorted by id.
func (m *memProvider) List(ctx context.Context) ([]Schedule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrProviderClosed
	}
	out := make([]Schedule, 0, len(m.schedules))
	for _, s := range m.schedules {
		out = append(out, cloneSchedule(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Create stores s under a fresh provider-issued id and returns the stored copy.
func (m *memProvider) Create(ctx context.Context, s Schedule) (Schedule, error) {
	if err := ctx.Err(); err != nil {
		return Schedule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Schedule{}, ErrProviderClosed
	}
	m.nextID++
	s.ID = fmt.Sprintf("sched-%d", m.nextID)
	m.schedules[s.ID] = cloneSchedule(s)
	return cloneSchedule(s), nil
}

// Update replaces the schedule with s.ID; an unknown id is rejected.
func (m *memProvider) Update(ctx context.Context, s Schedule) (Schedule, error) {
	if err := ctx.Err(); err != nil {
		return Schedule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Schedule{}, ErrProviderClosed
	}
	if _, ok := m.schedules[s.ID]; !ok {
		return Schedule{}, fmt.Errorf("%w: %s", ErrUnknownSchedule, s.ID)
	}
	m.schedules[s.ID] = cloneSchedule(s)
	return cloneSchedule(s), nil
}

// Delete removes the schedule with id; an unknown id is rejected.
func (m *memProvider) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrProviderClosed
	}
	if _, ok := m.schedules[id]; !ok {
		return fmt.Errorf("%w: %s", ErrUnknownSchedule, id)
	}
	delete(m.schedules, id)
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

// cloneSchedule copies a Schedule so the returned value never aliases the
// record's LastFire pointer.
func cloneSchedule(s Schedule) Schedule {
	if s.LastFire != nil {
		t := *s.LastFire
		s.LastFire = &t
	}
	return s
}
