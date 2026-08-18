// SQLite is the durable Store backend built on modernc.org/sqlite (pure Go,
// CGO-free; design.md §9). All sessions live in a single database file
// (dispatch-m2 §1: data/pa.db): a sessions table holding one row per session
// and an events table holding one row per appended event. Event rows store the
// type as TEXT, the payload as a JSON TEXT blob, and a version integer, so a
// new event type or payload version never requires migrating old rows (D8).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"personal-agent/internal/session"
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT    NOT NULL PRIMARY KEY,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
    session_id TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL,
    type       TEXT    NOT NULL,
    version    INTEGER NOT NULL,
    at         INTEGER NOT NULL,
    data       TEXT    NOT NULL,
    PRIMARY KEY (session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_events_session ON events (session_id, seq);
`

// SQLiteStore implements Store on one SQLite database file.
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLite opens (creating if needed) the SQLite database at path, applies
// the schema, and returns a ready store. The parent directory is created when
// missing. Time values are stored as Unix nanoseconds (UTC) in INTEGER columns.
func OpenSQLite(path string) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: create data dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One connection keeps SQLite serialized and makes the schema pragmas
	// deterministic; the agent loop is strictly serial anyway (D5).
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: pragma foreign_keys: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: pragma busy_timeout: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: init schema: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

// CreateSession inserts the session row, keeping any existing row untouched.
func (s *SQLiteStore) CreateSession(ctx context.Context, id string, createdAt time.Time) error {
	now := unixNano(createdAt)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, created_at, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		id, now, now); err != nil {
		return fmt.Errorf("store: create session %q: %w", id, err)
	}
	return nil
}

// AppendEvents durably appends events in one transaction: it materializes the
// session row when missing, inserts every event, and touches updated_at.
func (s *SQLiteStore) AppendEvents(ctx context.Context, sessionID string, events []session.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin append: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (id, created_at, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		sessionID, unixNano(now), unixNano(now)); err != nil {
		return fmt.Errorf("store: ensure session %q: %w", sessionID, err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO events (session_id, seq, type, version, at, data) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("store: prepare append: %w", err)
	}
	defer stmt.Close()
	for _, ev := range events {
		if _, err := stmt.ExecContext(ctx, sessionID, ev.Seq, ev.Type, ev.Version, unixNano(ev.At), string(ev.Data)); err != nil {
			return fmt.Errorf("store: append %s seq %d: %w", ev.Type, ev.Seq, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, unixNano(now), sessionID); err != nil {
		return fmt.Errorf("store: touch session %q: %w", sessionID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit append: %w", err)
	}
	return nil
}

// LoadSession replays all of a session's events in Seq order. An unknown
// session id yields ErrNotFound.
func (s *SQLiteStore) LoadSession(ctx context.Context, sessionID string) ([]session.Event, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("store: check session %q: %w", sessionID, err)
	}
	if exists == 0 {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, sessionID)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, type, version, at, data FROM events WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: load session %q: %w", sessionID, err)
	}
	defer rows.Close()
	var events []session.Event
	for rows.Next() {
		var ev session.Event
		var seq, version int64
		var at int64
		var data string
		if err := rows.Scan(&seq, &ev.Type, &version, &at, &data); err != nil {
			return nil, fmt.Errorf("store: scan event: %w", err)
		}
		ev.Seq = uint64(seq)
		ev.Version = int(version)
		ev.At = time.Unix(0, at).UTC()
		ev.Data = []byte(data)
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read events: %w", err)
	}
	return events, nil
}

// ListSessions returns every session's metadata, most recently updated first.
func (s *SQLiteStore) ListSessions(ctx context.Context) ([]SessionMeta, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.created_at, s.updated_at, COUNT(e.seq)
		FROM sessions s LEFT JOIN events e ON e.session_id = s.id
		GROUP BY s.id
		ORDER BY s.updated_at DESC, s.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer rows.Close()
	var metas []SessionMeta
	for rows.Next() {
		var m SessionMeta
		var created, updated int64
		var count int
		if err := rows.Scan(&m.ID, &created, &updated, &count); err != nil {
			return nil, fmt.Errorf("store: scan session meta: %w", err)
		}
		m.CreatedAt = time.Unix(0, created).UTC()
		m.UpdatedAt = time.Unix(0, updated).UTC()
		m.EventCount = count
		metas = append(metas, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read session metas: %w", err)
	}
	return metas, nil
}

// Close releases the underlying database handle.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func unixNano(t time.Time) int64 { return t.UnixNano() }
