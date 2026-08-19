// SQLite is the default KB backend, built on modernc.org/sqlite (pure Go,
// CGO-free; design.md §9). It uses the same shape as dsh-knowledge's
// local-provider: a knowledge_entries table plus a knowledge_fts FTS5 virtual
// table (unicode61 remove_diacritics 2), WAL, foreign keys and transactions.
// Search runs FTS5 BM25 first (title weighted high) and, when that under-fills
// topK, supplements with a Chinese bigram LIKE fallback over title/body/tags.
package kb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const kbSchema = `
CREATE TABLE IF NOT EXISTS knowledge_entries (
    id          TEXT    NOT NULL PRIMARY KEY,
    title       TEXT    NOT NULL,
    body        TEXT    NOT NULL,
    type        TEXT    NOT NULL CHECK(type IN ('preference','fact','decision','procedure','lesson')),
    tags        TEXT    NOT NULL,             -- JSON array of normalized tags
    scope       TEXT    NOT NULL DEFAULT '',  -- '' = global
    source      TEXT    NOT NULL DEFAULT '',
    confidence  REAL    NOT NULL CHECK(confidence >= 0 AND confidence <= 1),
    version     INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_kb_source ON knowledge_entries(source) WHERE source <> '';
CREATE INDEX IF NOT EXISTS idx_kb_scope_updated ON knowledge_entries(scope, updated_at DESC);
CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_fts USING fts5(
    knowledge_id UNINDEXED,
    title,
    body,
    tags,
    tokenize = 'unicode61 remove_diacritics 2'
);
`

// bm25Weights mirrors dsh-knowledge's bm25(knowledge_fts, 0.0, 4.0, 1.0, 0.5):
// the four weights apply to the four FTS columns in order (knowledge_id,
// title, body, tags), so title matches rank highest.
const bm25Weights = "bm25(knowledge_fts, 0.0, 4.0, 1.0, 0.5)"

// ftsRankSelect is the SELECT list shared by the FTS and fallback paths. The
// trailing column is the rank (BM25 for FTS rows, a fixed 3.0 for fallback
// rows — matching dsh-knowledge searchByTerms).
const entrySelect = `e.id, e.title, e.body, e.type, e.tags, e.scope, e.source, e.confidence, e.version`

// entryRow is one scanned knowledge_entries row plus its rank.
type entryRow struct {
	id, title, body, typ, tagsJSON, scope, source string
	confidence                                    float64
	version                                       int
	rank                                          float64
}

// SQLiteProvider implements KB on one SQLite database file.
type SQLiteProvider struct {
	db   *sql.DB
	path string // the database path; kept for Stats (/kb-status)
}

// OpenSQLite opens (creating if needed) the SQLite database at path, applies
// the schema and pragmas, and returns a ready provider. The parent directory
// is created when missing. A single connection keeps SQLite serialized and the
// pragmas deterministic (the caller is serial anyway, D5).
func OpenSQLite(path string) (*SQLiteProvider, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("kb: create data dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("kb: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("kb: %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(kbSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("kb: init schema: %w", err)
	}
	return &SQLiteProvider{db: db, path: path}, nil
}

// Search runs FTS5 BM25 first and supplements with the Chinese bigram LIKE
// fallback when the FTS result under-fills topK (design.md §8; dsh-knowledge
// local-provider search). An empty query returns no hits.
func (p *SQLiteProvider) Search(ctx context.Context, query string, opts SearchOpts) ([]Hit, error) {
	topK := normalizeTopK(opts.TopK)
	text := strings.TrimSpace(query)
	if text == "" {
		return []Hit{}, nil
	}
	scopeCond, scopeArgs := scopeCondition(opts.Scope)

	hits := []Hit{}
	// FTS5 BM25 path. A MATCH error (query syntax the tokenizer rejects) is
	// treated as "no FTS hits" so the fallback still runs (dsh try/catch → []).
	ftsSQL := `SELECT ` + entrySelect + `, ` + bm25Weights + ` AS rank
		FROM knowledge_fts
		JOIN knowledge_entries e ON e.id = knowledge_fts.knowledge_id
		WHERE knowledge_fts MATCH ?`
	if scopeCond != "" {
		ftsSQL += ` AND ` + scopeCond
	}
	ftsSQL += ` ORDER BY rank, e.updated_at DESC LIMIT ?`
	args := append([]any{toFtsQuery(text)}, append(scopeArgs, topK)...)
	if rows, err := p.db.QueryContext(ctx, ftsSQL, args...); err == nil {
		fts, scanErr := scanRows(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("kb: scan fts hits: %w", scanErr)
		}
		for _, r := range fts {
			hits = append(hits, Hit{Entry: entryFromRow(r), Score: rankToScore(r.rank)})
		}
	}

	if len(hits) < topK {
		supp, err := p.searchByTerms(ctx, text, scopeCond, scopeArgs, topK)
		if err != nil {
			return nil, fmt.Errorf("kb: fallback search: %w", err)
		}
		seen := make(map[string]bool, len(hits))
		for _, h := range hits {
			seen[h.Entry.ID] = true
		}
		for _, r := range supp {
			if len(hits) >= topK || seen[r.id] {
				continue
			}
			seen[r.id] = true
			hits = append(hits, Hit{Entry: entryFromRow(r), Score: rankToScore(r.rank)})
		}
	}
	return hits, nil
}

// searchByTerms runs the Chinese bigram LIKE fallback: every fallbackTerms
// term is matched against title/body/tags with an escaped LIKE, all OR-joined.
// Fallback rows carry a fixed rank of 3.0 (dsh-knowledge searchByTerms), which
// ranks them below any real FTS hit.
func (p *SQLiteProvider) searchByTerms(ctx context.Context, text, scopeCond string, scopeArgs []any, limit int) ([]entryRow, error) {
	terms := fallbackTerms(text)
	if len(terms) == 0 {
		return nil, nil
	}
	clauses := make([]string, 0, len(terms))
	args := make([]any, 0, len(terms)*3+len(scopeArgs)+1)
	for _, term := range terms {
		clauses = append(clauses, `(e.title LIKE ? ESCAPE '\' OR e.body LIKE ? ESCAPE '\' OR e.tags LIKE ? ESCAPE '\')`)
		like := "%" + escapeLike(term) + "%"
		args = append(args, like, like, like)
	}
	where := "1=1"
	if scopeCond != "" {
		where = scopeCond
	}
	sql := `SELECT ` + entrySelect + `, 3.0 AS rank
		FROM knowledge_entries e
		WHERE ` + where + ` AND (` + strings.Join(clauses, " OR ") + `)
		ORDER BY e.updated_at DESC
		LIMIT ?`
	args = append(scopeArgs, args...)
	args = append(args, limit)
	rows, err := p.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return scanRows(rows) // scanRows owns rows.Close
}

// Add writes one entry and syncs the FTS index in a single transaction. A
// draft whose Source matches an existing entry updates it with version+1
// (dispatch-m4a §2); otherwise a new entry is inserted at version 1. The
// provider owns identity: a caller-supplied ID/Version is ignored, and the
// returned Entry carries the assigned ID and Version (kb_add reports them so
// the model can open the entry with kb_read).
func (p *SQLiteProvider) Add(ctx context.Context, draft Entry) (Entry, error) {
	e, err := normalizeDraft(draft)
	if err != nil {
		return Entry{}, err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, fmt.Errorf("kb: begin add: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	if e.Source != "" {
		var id string
		var version int
		err := tx.QueryRowContext(ctx,
			`SELECT id, version FROM knowledge_entries WHERE source = ? ORDER BY updated_at DESC LIMIT 1`,
			e.Source).Scan(&id, &version)
		if err == nil {
			// Same source ⇒ update in place, version+1.
			e.ID, e.Version = id, version+1
			if _, err := tx.ExecContext(ctx, `UPDATE knowledge_entries SET
					title=?, body=?, type=?, tags=?, scope=?, confidence=?, version=?, updated_at=?
				WHERE id=?`,
				e.Title, e.Body, e.Type, tagsJSON(e.Tags), e.Scope, e.Confidence, e.Version,
				unixNano(time.Now().UTC()), id); err != nil {
				return Entry{}, fmt.Errorf("kb: update %s: %w", id, err)
			}
			if err := upsertFTS(ctx, tx, e); err != nil {
				return Entry{}, fmt.Errorf("kb: sync fts %s: %w", id, err)
			}
			if err := tx.Commit(); err != nil {
				return Entry{}, fmt.Errorf("kb: commit add: %w", err)
			}
			return e, nil
		}
		if err != sql.ErrNoRows {
			return Entry{}, fmt.Errorf("kb: find by source %q: %w", e.Source, err)
		}
	}

	id, err := newEntryID()
	if err != nil {
		return Entry{}, err
	}
	now := unixNano(time.Now().UTC())
	e.ID, e.Version = id, 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_entries
			(id, title, body, type, tags, scope, source, confidence, version, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.Title, e.Body, e.Type, tagsJSON(e.Tags), e.Scope, e.Source, e.Confidence,
		e.Version, now, now); err != nil {
		return Entry{}, fmt.Errorf("kb: insert %s: %w", e.ID, err)
	}
	if err := upsertFTS(ctx, tx, e); err != nil {
		return Entry{}, fmt.Errorf("kb: sync fts %s: %w", e.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, fmt.Errorf("kb: commit add: %w", err)
	}
	return e, nil
}

// Recall is a bounded search: it implements the retrieval logic of proactive
// recall; the injection orchestration (kb/recall event + context assembly)
// lands in M4b.
func (p *SQLiteProvider) Recall(ctx context.Context, query string, limit int) ([]Hit, error) {
	return p.Search(ctx, query, SearchOpts{TopK: limit})
}

// Get returns one full entry by id (kb_read, dispatch-m4b §1). An unknown id
// is an error so the model never mistakes a stale id for a live entry.
func (p *SQLiteProvider) Get(ctx context.Context, id string) (Entry, error) {
	r, err := scanOne(p.db.QueryRowContext(ctx,
		`SELECT `+entrySelect+` FROM knowledge_entries e WHERE e.id = ?`, id))
	if err == sql.ErrNoRows {
		return Entry{}, fmt.Errorf("kb: entry %q not found", id)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("kb: get %q: %w", id, err)
	}
	return entryFromRow(r), nil
}

// Stats reports entry count, database file size, and the most recent writes
// (dispatch-m4b §4 /kb-status).
func (p *SQLiteProvider) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_entries`).Scan(&st.EntryCount); err != nil {
		return Stats{}, fmt.Errorf("kb: count entries: %w", err)
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT title, type, updated_at FROM knowledge_entries ORDER BY updated_at DESC LIMIT 5`)
	if err != nil {
		return Stats{}, fmt.Errorf("kb: recent writes: %w", err)
	}
	for rows.Next() {
		var rw RecentWrite
		var updated int64
		if err := rows.Scan(&rw.Title, &rw.Type, &updated); err != nil {
			rows.Close()
			return Stats{}, fmt.Errorf("kb: scan recent write: %w", err)
		}
		rw.UpdatedAt = time.Unix(0, updated).UTC()
		st.Recent = append(st.Recent, rw)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Stats{}, fmt.Errorf("kb: read recent writes: %w", err)
	}
	rows.Close()

	st.DBPath = p.path
	if p.path != "" && p.path != ":memory:" {
		if info, err := os.Stat(p.path); err == nil {
			st.DBSize = info.Size()
		}
	}
	return st, nil
}

// Reindex rebuilds the FTS5 index from the entries table (dispatch-m4b §4
// /kb-reindex): it clears knowledge_fts and re-inserts every entry in one
// transaction, repairing a drifted or corrupted index. All entry rows are read
// into memory first so the write pass never competes with an open SELECT on
// the provider's single connection.
func (p *SQLiteProvider) Reindex(ctx context.Context) error {
	type ftsRow struct{ id, title, body, tags string }
	rows, err := p.db.QueryContext(ctx, `SELECT id, title, body, tags FROM knowledge_entries`)
	if err != nil {
		return fmt.Errorf("kb: read entries for reindex: %w", err)
	}
	var all []ftsRow
	for rows.Next() {
		var r ftsRow
		if err := rows.Scan(&r.id, &r.title, &r.body, &r.tags); err != nil {
			rows.Close()
			return fmt.Errorf("kb: scan reindex row: %w", err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("kb: read reindex rows: %w", err)
	}
	rows.Close()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("kb: begin reindex: %w", err)
	}
	defer tx.Rollback() // no-op after Commit
	if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge_fts`); err != nil {
		return fmt.Errorf("kb: clear fts: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO knowledge_fts(knowledge_id, title, body, tags) VALUES (?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("kb: prepare reindex: %w", err)
	}
	defer stmt.Close()
	for _, r := range all {
		if _, err := stmt.ExecContext(ctx, r.id, r.title, r.body, r.tags); err != nil {
			return fmt.Errorf("kb: insert fts %s: %w", r.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("kb: commit reindex: %w", err)
	}
	return nil
}

// Close releases the underlying database handle.
func (p *SQLiteProvider) Close() error {
	return p.db.Close()
}

// upsertFTS replaces one entry's FTS row (delete + insert), keeping the index
// in sync with the entries table.
func upsertFTS(ctx context.Context, tx *sql.Tx, e Entry) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge_fts WHERE knowledge_id = ?`, e.ID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO knowledge_fts(knowledge_id, title, body, tags) VALUES (?,?,?,?)`,
		e.ID, e.Title, e.Body, strings.Join(e.Tags, " "))
	return err
}

// scopeCondition returns the SQL condition filtering by scope, or "" when
// opts.Scope is empty (no filter, all scopes). Empty entry scope means global,
// so a "global" filter matches both the empty scope and the literal 'global'.
func scopeCondition(scope string) (string, []any) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "", nil
	}
	if scope == "global" {
		return "(e.scope = '' OR e.scope = 'global')", nil
	}
	return "e.scope = ?", []any{scope}
}

// scanRows drains a rows result into entryRows. Caller must not Close rows
// afterwards.
func scanRows(rows *sql.Rows) ([]entryRow, error) {
	defer rows.Close()
	var out []entryRow
	for rows.Next() {
		var r entryRow
		if err := rows.Scan(&r.id, &r.title, &r.body, &r.typ, &r.tagsJSON, &r.scope,
			&r.source, &r.confidence, &r.version, &r.rank); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// scanOne scans a single-row result into an entryRow (Get). The caller owns
// nothing: *sql.Row releases its connection on Scan.
func scanOne(row *sql.Row) (entryRow, error) {
	var r entryRow
	if err := row.Scan(&r.id, &r.title, &r.body, &r.typ, &r.tagsJSON, &r.scope,
		&r.source, &r.confidence, &r.version, &r.rank); err != nil {
		return entryRow{}, err
	}
	return r, nil
}

func entryFromRow(r entryRow) Entry {
	return Entry{
		ID:         r.id,
		Title:      r.title,
		Body:       r.body,
		Type:       r.typ,
		Tags:       parseTags(r.tagsJSON),
		Scope:      r.scope,
		Source:     r.source,
		Confidence: r.confidence,
		Version:    r.version,
	}
}

func tagsJSON(tags []string) string {
	b, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func parseTags(s string) []string {
	var tags []string
	if err := json.Unmarshal([]byte(s), &tags); err != nil {
		return nil
	}
	return tags
}

func newEntryID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("kb: generate entry id: %w", err)
	}
	return "kb-" + hex.EncodeToString(b[:]), nil
}

func unixNano(t time.Time) int64 { return t.UnixNano() }
