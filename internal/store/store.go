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
	// WorkspaceID is the owning workspace (P6 grouping), empty for the
	// ungrouped bucket.
	WorkspaceID string
}

// WorkspaceMeta is the durable metadata of one workspace (P6, dsh workspace
// grouping): a named bucket that sessions are created into. Sort is the
// display order in the sidebar's grouped view (ascending).
type WorkspaceMeta struct {
	ID    string
	Title string
	Sort  int
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
	// SetSessionWorkspace moves a session into a workspace; an empty
	// workspaceID returns it to the ungrouped bucket. ErrNotFound when the
	// session id has no row.
	SetSessionWorkspace(ctx context.Context, sessionID, workspaceID string) error
	// DeleteSession removes a session and all of its events. ErrNotFound when
	// the id has no row.
	DeleteSession(ctx context.Context, sessionID string) error

	// CreateWorkspace materializes a workspace row (idempotent). Sort is
	// appended at the end of the current order.
	CreateWorkspace(ctx context.Context, id, title string) error
	// ListWorkspaces returns every workspace's metadata, ordered by Sort then
	// creation.
	ListWorkspaces(ctx context.Context) ([]WorkspaceMeta, error)
	// SetWorkspaceTitle stores a workspace's title. ErrNotFound when the id
	// has no row.
	SetWorkspaceTitle(ctx context.Context, id, title string) error
	// DeleteWorkspace removes a workspace; its sessions return to the
	// ungrouped bucket. ErrNotFound when the id has no row.
	DeleteWorkspace(ctx context.Context, id string) error

	// Close releases the backend's resources.
	Close() error
}
