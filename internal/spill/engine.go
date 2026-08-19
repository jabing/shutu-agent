package spill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"personal-agent/internal/session"
)

// defaultRecallLimit is the number of memos Recall returns when limit <= 0.
const defaultRecallLimit = 5

// engine is the default spill Service implementation (ADR 决策 M6c): it owns
// id issuance (content-hash), dedup, the default recall limit and the AutoSpill
// policy wiring, delegating storage to a Provider. It is safe for concurrent
// use; Close is idempotent and releases the Provider. The unexported concrete
// type keeps the Engine interface the only public shape; NewEngine returns it
// as a concrete *engine that satisfies Engine.
type engine struct {
	prov   Provider
	mu     sync.Mutex
	closed bool
}

// NewEngine returns an engine backed by prov; a nil prov selects the default
// in-memory Provider (newMemProvider). Each engine should own its provider:
// Close releases it.
func NewEngine(prov Provider) *engine {
	if prov == nil {
		prov = newMemProvider()
	}
	return &engine{prov: prov}
}

// memoID derives the idempotent memo id for content: a SHA-256 digest of the
// (already trimmed) content, so the same content always maps to the same memo —
// spilling it twice is idempotent and never duplicates.
func memoID(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "memo-" + hex.EncodeToString(sum[:])
}

// Spill writes content as a memo with source and returns it. The id is the
// content hash, so spilling the same content twice is idempotent — the second
// call returns the existing memo unchanged.
func (e *engine) Spill(ctx context.Context, content, source string) (Memo, error) {
	m, _, err := e.spill(ctx, content, source)
	return m, err
}

// spill stores content under its content-hash id. added reports whether this
// call actually stored a NEW memo: an existing memo with the same id (same
// content) is returned unchanged with added=false, so a re-spill never
// duplicates. AutoSpill uses added to count new memories.
func (e *engine) spill(ctx context.Context, content, source string) (Memo, bool, error) {
	if err := ctx.Err(); err != nil {
		return Memo{}, false, err
	}
	if err := e.checkOpen(); err != nil {
		return Memo{}, false, err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return Memo{}, false, errors.New("spill: empty content")
	}
	id := memoID(content)
	existing, err := e.prov.Get(ctx, id)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, ErrUnknownMemo) {
		return Memo{}, false, err
	}
	memo, err := e.prov.Add(ctx, Memo{ID: id, Content: content, Source: source, CreatedAt: time.Now().UTC()})
	if err != nil {
		return Memo{}, false, err
	}
	return memo, true, nil
}

// Recall returns up to limit memos whose content matches query (v1:
// case-insensitive substring). limit <= 0 means the default of 5.
func (e *engine) Recall(ctx context.Context, query string, limit int) ([]Memo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.checkOpen(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultRecallLimit
	}
	return e.prov.Search(ctx, query, limit)
}

// AutoSpill runs the v1 auto-sedimentation policy over the event log and
// returns the number of NEW memos added. The policy kernel (autoSpillCandidates,
// policy.go) is a pure function — it performs no side effects and never touches
// the Engine or Provider; AutoSpill only writes the candidates it produces,
// idempotently by content hash (re-running over the same events adds nothing).
// The wiring layer calls AutoSpill on its own serial path (D5).
func (e *engine) AutoSpill(ctx context.Context, events []session.Event) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := e.checkOpen(); err != nil {
		return 0, err
	}
	var added int
	for _, c := range autoSpillCandidates(events) {
		_, isNew, err := e.spill(ctx, c.content, c.source)
		if err != nil {
			return added, err
		}
		if isNew {
			added++
		}
	}
	return added, nil
}

// List returns every current memo, sorted by id.
func (e *engine) List(ctx context.Context) ([]Memo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.checkOpen(); err != nil {
		return nil, err
	}
	return e.prov.List(ctx)
}

// Remove deletes the memo with id; an unknown id is rejected.
func (e *engine) Remove(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.checkOpen(); err != nil {
		return err
	}
	return e.prov.Delete(ctx, id)
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
// closed so every further operation is rejected with ErrEngineClosed. It is
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
