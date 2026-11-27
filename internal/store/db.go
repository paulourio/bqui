package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver ("sqlite")
)

const dbFile = "bqui.db"

// schema is applied in order; the index is the schema version.
var schema = []string{
	`CREATE TABLE tabs (
		id TEXT PRIMARY KEY,
		position INTEGER NOT NULL,
		title TEXT NOT NULL,
		sql TEXT NOT NULL,
		file TEXT NOT NULL DEFAULT '',
		cursor_line INTEGER NOT NULL DEFAULT 0,
		cursor_col INTEGER NOT NULL DEFAULT 0,
		session_mode INTEGER NOT NULL DEFAULT 0,
		session_id TEXT NOT NULL DEFAULT '',
		last_job TEXT NOT NULL DEFAULT '',
		updated INTEGER NOT NULL
	)`,
	`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`CREATE TABLE runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		started INTEGER NOT NULL,
		project TEXT NOT NULL,
		job_id TEXT NOT NULL DEFAULT '',
		location TEXT NOT NULL DEFAULT '',
		sql TEXT NOT NULL,
		state TEXT NOT NULL,
		rows INTEGER NOT NULL DEFAULT 0,
		bytes INTEGER NOT NULL DEFAULT 0,
		elapsed_ms INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE VIRTUAL TABLE runs_fts USING fts5(sql, job_id, content='runs', content_rowid='id')`,
	`CREATE TRIGGER runs_ai AFTER INSERT ON runs BEGIN
		INSERT INTO runs_fts(rowid, sql, job_id) VALUES (new.id, new.sql, new.job_id);
	END`,
}

// DB is bqui's local database: editor tabs (sessions) and run history.
type DB struct {
	db *sql.DB
}

// DefaultDBPath returns ~/Library/Application Support/bqui/bqui.db (or the
// OS equivalent).
func DefaultDBPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("store: %w", err)
	}

	return filepath.Join(dir, appDir, dbFile), nil
}

// Open opens (and migrates) the database at path.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}

	db.SetMaxOpenConns(1) // one writer; reads are cheap and serialized

	if err := migrate(db); err != nil {
		_ = db.Close()

		return nil, err
	}

	return &DB{db: db}, nil
}

func migrate(db *sql.DB) error {
	ctx := context.Background()

	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("store: version: %w", err)
	}

	for i := version; i < len(schema); i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}

		if _, err := tx.ExecContext(ctx, schema[i]); err != nil {
			_ = tx.Rollback()

			return fmt.Errorf("store: migrate %d: %w", i, err)
		}

		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			_ = tx.Rollback()

			return fmt.Errorf("store: migrate %d: %w", i, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: migrate %d: %w", i, err)
		}
	}

	return nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// Tab is one editor tab's persisted state.
type Tab struct {
	ID          string
	Position    int
	Title       string
	SQL         string
	File        string // saved-query file the tab is linked to ("" if none)
	CursorLine  int
	CursorCol   int
	SessionMode bool   // run queries in a BigQuery session
	SessionID   string // active BigQuery session
	LastJob     string // project:location.job of the last run
}

// SaveTab inserts or updates a tab (autosave).
func (d *DB) SaveTab(ctx context.Context, t Tab) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO tabs (id, position, title, sql, file, cursor_line, cursor_col, session_mode, session_id, last_job, updated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET position=excluded.position, title=excluded.title, sql=excluded.sql,
			file=excluded.file, cursor_line=excluded.cursor_line, cursor_col=excluded.cursor_col,
			session_mode=excluded.session_mode, session_id=excluded.session_id, last_job=excluded.last_job,
			updated=excluded.updated`,
		t.ID, t.Position, t.Title, t.SQL, t.File, t.CursorLine, t.CursorCol, t.SessionMode, t.SessionID, t.LastJob, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: save tab: %w", err)
	}

	return nil
}

// DeleteTab removes a tab.
func (d *DB) DeleteTab(ctx context.Context, id string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM tabs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete tab: %w", err)
	}

	return nil
}

// Tabs returns all tabs in position order.
func (d *DB) Tabs(ctx context.Context) ([]Tab, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, position, title, sql, file, cursor_line, cursor_col, session_mode, session_id, last_job
		FROM tabs ORDER BY position, updated`)
	if err != nil {
		return nil, fmt.Errorf("store: tabs: %w", err)
	}
	defer rows.Close()

	var out []Tab

	for rows.Next() {
		var t Tab
		if err := rows.Scan(&t.ID, &t.Position, &t.Title, &t.SQL, &t.File, &t.CursorLine, &t.CursorCol, &t.SessionMode, &t.SessionID, &t.LastJob); err != nil {
			return nil, fmt.Errorf("store: tabs: %w", err)
		}

		out = append(out, t)
	}

	return out, rows.Err()
}

// SetMeta stores a small key/value (e.g. the active tab).
func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: meta: %w", err)
	}

	return nil
}

// Meta reads a key ("" if unset).
func (d *DB) Meta(ctx context.Context, key string) (string, error) {
	var v string

	err := d.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("store: meta: %w", err)
	}

	return v, nil
}

// Run is one local run-history entry.
type Run struct {
	ID       int64
	Started  time.Time
	Project  string
	JobID    string
	Location string
	SQL      string
	State    string // done, failed, cancelled
	Rows     int64
	Bytes    int64
	Elapsed  time.Duration
	Error    string
}

// RecordRun appends a run to the local history.
func (d *DB) RecordRun(ctx context.Context, r Run) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO runs (started, project, job_id, location, sql, state, rows, bytes, elapsed_ms, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Started.UnixMilli(), r.Project, r.JobID, r.Location, r.SQL, r.State, r.Rows, r.Bytes, r.Elapsed.Milliseconds(), r.Error)
	if err != nil {
		return fmt.Errorf("store: record run: %w", err)
	}

	return nil
}

// SearchRuns returns runs newest first; a non-empty query matches SQL text
// and job IDs with FTS5 (each word as a prefix).
func (d *DB) SearchRuns(ctx context.Context, query string, limit int) ([]Run, error) {
	q := `SELECT id, started, project, job_id, location, sql, state, rows, bytes, elapsed_ms, error FROM runs`
	args := []any{}

	if terms := ftsQuery(query); terms != "" {
		q += ` WHERE id IN (SELECT rowid FROM runs_fts WHERE runs_fts MATCH ?)`
		args = append(args, terms)
	}

	q += ` ORDER BY started DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: search runs: %w", err)
	}
	defer rows.Close()

	var out []Run

	for rows.Next() {
		var (
			r                Run
			started, elapsed int64
		)

		if err := rows.Scan(&r.ID, &started, &r.Project, &r.JobID, &r.Location, &r.SQL, &r.State, &r.Rows, &r.Bytes, &elapsed, &r.Error); err != nil {
			return nil, fmt.Errorf("store: search runs: %w", err)
		}

		r.Started, r.Elapsed = time.UnixMilli(started), time.Duration(elapsed)*time.Millisecond
		out = append(out, r)
	}

	return out, rows.Err()
}

// ftsQuery turns free text into an FTS5 query of quoted prefix terms, so
// user input can never be FTS syntax.
func ftsQuery(s string) string {
	var terms []string

	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		word := r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127

		return !word
	}) {
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"*`)
	}

	return strings.Join(terms, " ")
}
