// Package store defines the persistence abstraction for the session log
// (design.md D8) and its SQLite backend. The store appends events durably and
// replays them at startup; the in-memory log is always rebuilt from the store,
// never the other way around (D1: history is a derived value).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jabing/shutu-agent/internal/session"
)

// ErrNotFound is returned when a session id has no row in the store.
var ErrNotFound = errors.New("store: session not found")

// SessionMeta is the durable metadata of one session.
type SessionMeta struct {
	ID         string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	EventCount int
	// Title is the accepted display title (fallback / LLM / user rename).
	// When empty the UI falls back to the first-user-message inference.
	Title string
	// TitleSource is the producer of the accepted title: "" | fallback | llm
	// | user. "user" pins the title so automatic work never re-revises it.
	TitleSource string
	// WorkspaceID is the owning workspace (P6 grouping), empty for the
	// ungrouped bucket.
	WorkspaceID string
	// ArchivedAt is non-zero once the session is archived (P6.2 dsh archive):
	// archived sessions leave the active sidebar list.
	ArchivedAt time.Time
	// Sort is the manual drag order (P6.2): zero means "fall back to updated
	// activity"; a drag sets the whole bucket's order.
	Sort int
	// FlatSort is the manual drag order for the flat (ungrouped) view
	// (P6.3): zero means "fall back to updated activity". It is independent
	// from the per-workspace Sort.
	FlatSort int
}

// SearchHit is one session that matched a body-text query, with the first
// matching line snippet (P6.3 remote search, dsh searchAcrossSessions).
type SearchHit struct {
	SessionID string
	UpdatedAt time.Time
	Title     string
	Snippet   string
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
	// GetSessionMeta returns one session's metadata. ErrNotFound when the id
	// has no row.
	GetSessionMeta(ctx context.Context, sessionID string) (SessionMeta, error)
	// SetSessionTitle stores (or clears) the accepted title for a session and
	// records its producer ("" | session.TitleSourceFallback |
	// session.TitleSourceLLM | session.TitleSourceUser). An empty title clears
	// the stored title and its source and returns to inference. ErrNotFound
	// when the id has no row.
	SetSessionTitle(ctx context.Context, sessionID, title, source string) error
	// SetSessionWorkspace moves a session into a workspace; an empty
	// workspaceID returns it to the ungrouped bucket. ErrNotFound when the
	// session id has no row.
	SetSessionWorkspace(ctx context.Context, sessionID, workspaceID string) error
	// ArchiveSession marks a session archived (dsh archive: it leaves the
	// active sidebar list). Unarchive clears the mark. ErrNotFound when the id
	// has no row.
	ArchiveSession(ctx context.Context, sessionID string, archived bool) error
	// ReorderSessions applies a manual order: every listed session is moved
	// into workspaceID (empty = ungrouped) and assigned sort 0..n-1 in list
	// order, so the grouped sidebar follows the drag. The group's other
	// sessions keep their sort untouched.
	ReorderSessions(ctx context.Context, workspaceID string, sessionIDs []string) error
	// ReorderSessionsFlat applies a manual order for the flat view: every
	// listed session takes flat_sort 0..n-1 in list order (workspace
	// membership is untouched).
	ReorderSessionsFlat(ctx context.Context, sessionIDs []string) error
	// SearchSessions finds sessions whose event bodies contain q (case- and
	// width-insensitive substring), returning one hit per session with the
	// first matching snippet, most recently updated first.
	SearchSessions(ctx context.Context, q string) ([]SearchHit, error)
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
	// ReorderWorkspaces applies a manual order: sort is rewritten 0..n-1 in
	// list order.
	ReorderWorkspaces(ctx context.Context, ids []string) error
	// DeleteWorkspace removes a workspace; its sessions return to the
	// ungrouped bucket. ErrNotFound when the id has no row.
	DeleteWorkspace(ctx context.Context, id string) error

	// GetSettings returns every persisted runtime setting (key → value). These
	// back the General-settings rows (Agent preset / permission preset /
	// default terminal) and are applied at startup by the composition root.
	GetSettings(ctx context.Context) (map[string]string, error)
	// SetSetting stores one runtime setting, replacing any previous value.
	SetSetting(ctx context.Context, key, value string) error
	// DeleteSetting removes one runtime setting row (no-op when absent).
	DeleteSetting(ctx context.Context, key string) error

	// Close releases the backend's resources.
	Close() error
}
