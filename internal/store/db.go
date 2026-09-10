// Package store is Garrison's durable memory: the task journal, the cold
// metric tier, and the events worth keeping.
//
// SQLite through modernc.org/sqlite, which is pure Go. That matters here more
// than usual: the target is a native Windows binary, and a cgo dependency
// would mean an MSVC toolchain to build it — see ADR 0001.
//
// This package does I/O and sits beside services. It imports model and tasks
// and nothing else of ours.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB is the database and everything that reads or writes it.
type DB struct {
	sql  *sql.DB
	path string
}

// Open prepares the database, creating and migrating it if needed.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	// WAL so a reader never blocks the writer: the store's goroutine writes
	// while a view is reading history, and the alternative is a dashboard
	// that stutters whenever a task records a step.
	//
	// busy_timeout because two connections in one process can still collide
	// briefly, and failing a write for that is worse than waiting 5s.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	// One writer at a time. SQLite allows more and then serialises them
	// itself with lock contention; keeping the pool at one makes the queue
	// explicit and the failure modes fewer.
	handle.SetMaxOpenConns(1)

	db := &DB{sql: handle, path: path}
	if err := db.migrate(); err != nil {
		handle.Close()
		return nil, err
	}
	return db, nil
}

// Close releases the database.
func (d *DB) Close() error { return d.sql.Close() }

// Path is where the database lives, for an error message that can be acted on.
func (d *DB) Path() string { return d.path }

// migrations are applied in order and never edited once shipped.
//
// A migration that has run on somebody's machine is history: changing it means
// two installations disagree about what version 3 was, and the only way to
// find out is a query that fails months later. Add a new one instead.
var migrations = []string{
	`CREATE TABLE tasks (
		id          TEXT PRIMARY KEY,
		server      TEXT NOT NULL,
		kind        TEXT NOT NULL,
		trigger     TEXT NOT NULL,
		state       TEXT NOT NULL,
		cursor      INTEGER NOT NULL,
		steps       TEXT NOT NULL,
		err         TEXT NOT NULL DEFAULT '',
		history     TEXT NOT NULL DEFAULT '',
		queued_at   INTEGER,
		started_at  INTEGER,
		ended_at    INTEGER
	)`,
	`CREATE INDEX tasks_by_server ON tasks (server, started_at DESC)`,

	`CREATE TABLE metrics (
		server   TEXT NOT NULL,
		series   TEXT NOT NULL,
		at       INTEGER NOT NULL,
		mean     REAL NOT NULL,
		min      REAL NOT NULL,
		max      REAL NOT NULL,
		PRIMARY KEY (server, series, at)
	)`,

	`CREATE TABLE events (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		server   TEXT NOT NULL,
		at       INTEGER NOT NULL,
		kind     TEXT NOT NULL,
		player   TEXT NOT NULL DEFAULT '',
		steam_id TEXT NOT NULL DEFAULT '',
		text     TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX events_by_server ON events (server, at DESC)`,

	`CREATE TABLE sessions (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		server    TEXT NOT NULL,
		player    TEXT NOT NULL,
		steam_id  TEXT NOT NULL DEFAULT '',
		joined_at INTEGER NOT NULL,
		left_at   INTEGER
	)`,
	`CREATE INDEX sessions_by_server ON sessions (server, joined_at DESC)`,
}

func (d *DB) migrate() error {
	if _, err := d.sql.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("%s: creating the version table: %w", d.path, err)
	}

	var current int
	err := d.sql.QueryRow(`SELECT version FROM schema_version`).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := d.sql.Exec(`INSERT INTO schema_version (version) VALUES (0)`); err != nil {
			return fmt.Errorf("%s: recording version 0: %w", d.path, err)
		}
		current = 0
	} else if err != nil {
		return fmt.Errorf("%s: reading the schema version: %w", d.path, err)
	}

	if current > len(migrations) {
		// Downgrading is not supported and guessing would corrupt data.
		return fmt.Errorf("%s: was written by a newer Garrison (schema %d, this build knows %d)",
			d.path, current, len(migrations))
	}

	for i := current; i < len(migrations); i++ {
		tx, err := d.sql.Begin()
		if err != nil {
			return fmt.Errorf("%s: starting migration %d: %w", d.path, i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: migration %d: %w", d.path, i+1, err)
		}
		if _, err := tx.Exec(`UPDATE schema_version SET version = ?`, i+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: recording migration %d: %w", d.path, i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("%s: committing migration %d: %w", d.path, i+1, err)
		}
	}
	return nil
}

// Version is the schema version on disk, for diagnostics.
func (d *DB) Version() (int, error) {
	var v int
	if err := d.sql.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}
