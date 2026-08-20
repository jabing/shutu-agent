// Package store defines the persistence abstraction for the session log
// (design.md D8) and its SQLite backend. The store appends events durably and
// replays them at startup; the in-memory log is always rebuilt from the store,
// never the other way around (D1: history is a derived value).
package store

import (
	"context"
	"errors"
	"time"

	"personal-agent/internal/session"
)

// ErrNotFound is returned when a session id has no row in the store.
var ErrNotFound = errors.New("store: session not found")

// SessionMeta is the durable metadata of one session.
type SessionMeta struct {
	ID         string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	EventCount int
	// Title is the user-set title (renamed via the web sidebar, P2). When
	// empty the UI falls back to the first-user-message inference.
	Title string
}

// Store is the durable append-only event backend. The agent loop is strictly
// serial (D5), so callers never need their own locking, but implementations
// must not corrupt on concurrent use either.
type Store interface {
	// CreateSession materializes a session row (idempotent). createdAt is
	// recorded verbatim; updatedAt starts at createdAt.
	CreateSession(ctx context.Context, id string, createdAt time.Time) error
	// AppendEvents durably appends events to one session. Events must carry
	// strictly increasing Seq values not already present for that session. A
	// missing session row is materialized on first append.
	AppendEvents(ctx context.Context, sessionID string, events []session.Event) error
	// LoadSession replays all of a session's events in Seq order. It returns
	// ErrNotFound when the session id has no row.
	LoadSession(ctx context.Context, sessionID string) ([]session.Event, error)
	// ListSessions returns every session's metadata, most recently updated
	// first.
	ListSessions(ctx context.Context) ([]SessionMeta, error)
	// SetSessionTitle stores a user-set title for a session. A non-empty title
	// overrides the UI's first-user-message inference; an empty title clears the
	// override and returns to inference. ErrNotFound when the id has no row.
	SetSessionTitle(ctx context.Context, sessionID, title string) error
	// DeleteSession removes a session and all of its events. ErrNotFound when
	// the id has no row.
	DeleteSession(ctx context.Context, sessionID string) error
	// Close releases the backend's resources.
	Close() error
}
