// Package store is the app's local persistence layer: a single SQLite database
// file (pure-Go driver, no cgo) holding data that should outlive a single run —
// reference laps, per-track corner names, session/stint history and a recordings
// index.
//
// Design notes:
//   - One file, opened once at startup, closed at shutdown (owned by app.Runtime).
//   - The driver is modernc.org/sqlite (pure Go) so the app still cross-compiles
//     for Windows/Linux with no C toolchain — consistent with the rest of the repo.
//   - The store deals in plain scalars + JSON blobs; it deliberately does NOT
//     import the engineer/recording packages, so persistence stays decoupled from
//     the in-memory models. Callers marshal their structs to JSON and back.
//   - Every write is best-effort from the caller's view: a persistence failure is
//     logged, never fatal (telemetry must keep flowing).
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite handle. Safe for concurrent use: database/sql pools
// connections and SQLite serializes writes.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies the
// schema. busy_timeout keeps brief write contention from erroring out.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// migrate creates the tables if they don't exist. New columns/tables are added
// here over time (CREATE ... IF NOT EXISTS keeps it idempotent).
func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS reference_laps (
    track      TEXT NOT NULL,
    car        TEXT NOT NULL,
    class      TEXT NOT NULL DEFAULT '',
    lap_time   REAL NOT NULL,
    sectors    TEXT NOT NULL,   -- JSON: []engineer.MiniSectorState
    path       TEXT NOT NULL,   -- JSON: []engineer.Vec2
    updated_at INTEGER NOT NULL, -- unix seconds
    PRIMARY KEY (track, car)
);

CREATE TABLE IF NOT EXISTS track_corners (
    track      TEXT PRIMARY KEY,
    labels     TEXT NOT NULL,   -- JSON: []string, one label per mini-sector
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    track      TEXT NOT NULL,
    car        TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    ended_at   INTEGER NOT NULL DEFAULT 0,
    laps       INTEGER NOT NULL DEFAULT 0,
    best_lap   REAL NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS recordings (
    name        TEXT PRIMARY KEY,
    track       TEXT NOT NULL DEFAULT '',
    car         TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT '',
    recorded_at INTEGER NOT NULL DEFAULT 0,
    size_bytes  INTEGER NOT NULL DEFAULT 0
);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate store: %w", err)
	}
	return nil
}

func now() int64 { return time.Now().Unix() }
