package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// ReferenceLap is a persisted best lap. Sectors/Path are opaque JSON the caller
// marshals (the store doesn't import the engineer models). LapTime is in seconds.
type ReferenceLap struct {
	Track     string
	Car       string
	Class     string
	LapTime   float64
	Sectors   string // JSON []MiniSectorState
	Path      string // JSON []Vec2
	UpdatedAt int64
}

// SaveReferenceLap upserts a reference lap, keeping only the FASTER of the
// existing and the new lap for a (track, car). A slower lap is silently ignored.
func (s *Store) SaveReferenceLap(r ReferenceLap) error {
	_, err := s.db.Exec(
		`INSERT INTO reference_laps (track, car, class, lap_time, sectors, path, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(track, car) DO UPDATE SET
		   class=excluded.class, lap_time=excluded.lap_time, sectors=excluded.sectors,
		   path=excluded.path, updated_at=excluded.updated_at
		 WHERE excluded.lap_time < reference_laps.lap_time`,
		r.Track, r.Car, r.Class, r.LapTime, r.Sectors, r.Path, now())
	if err != nil {
		return fmt.Errorf("save reference lap: %w", err)
	}
	return nil
}

// GetReferenceLap returns the stored reference lap for a (track, car), or ok=false
// when none exists.
func (s *Store) GetReferenceLap(track, car string) (ReferenceLap, bool, error) {
	var r ReferenceLap
	err := s.db.QueryRow(
		`SELECT track, car, class, lap_time, sectors, path, updated_at
		   FROM reference_laps WHERE track=? AND car=?`, track, car).
		Scan(&r.Track, &r.Car, &r.Class, &r.LapTime, &r.Sectors, &r.Path, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ReferenceLap{}, false, nil
	}
	if err != nil {
		return ReferenceLap{}, false, fmt.Errorf("get reference lap: %w", err)
	}
	return r, true, nil
}

// SaveCorners stores the per-mini-sector corner labels (JSON []string) for a track.
func (s *Store) SaveCorners(track, labelsJSON string) error {
	_, err := s.db.Exec(
		`INSERT INTO track_corners (track, labels, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(track) DO UPDATE SET labels=excluded.labels, updated_at=excluded.updated_at`,
		track, labelsJSON, now())
	if err != nil {
		return fmt.Errorf("save corners: %w", err)
	}
	return nil
}

// GetCorners returns the stored corner labels JSON for a track, or ok=false.
func (s *Store) GetCorners(track string) (string, bool, error) {
	var labels string
	err := s.db.QueryRow(`SELECT labels FROM track_corners WHERE track=?`, track).Scan(&labels)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get corners: %w", err)
	}
	return labels, true, nil
}

// SessionRow is one stint/session in the history.
type SessionRow struct {
	ID        int64   `json:"id"`
	Track     string  `json:"track"`
	Car       string  `json:"car"`
	StartedAt int64   `json:"started_at"` // unix seconds
	EndedAt   int64   `json:"ended_at"`   // unix seconds
	Laps      int     `json:"laps"`
	BestLap   float64 `json:"best_lap"` // seconds
}

// StartSession inserts a new session row and returns its id.
func (s *Store) StartSession(track, car string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO sessions (track, car, started_at) VALUES (?, ?, ?)`, track, car, now())
	if err != nil {
		return 0, fmt.Errorf("start session: %w", err)
	}
	return res.LastInsertId()
}

// UpdateSession records the running lap count + best lap for a session.
func (s *Store) UpdateSession(id int64, laps int, bestLap float64) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET laps=?, best_lap=?, ended_at=? WHERE id=?`, laps, bestLap, now(), id)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	return nil
}

// ListSessions returns the most recent sessions, newest first.
func (s *Store) ListSessions(limit int) ([]SessionRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, track, car, started_at, ended_at, laps, best_lap
		   FROM sessions ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(&r.ID, &r.Track, &r.Car, &r.StartedAt, &r.EndedAt, &r.Laps, &r.BestLap); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordingRow indexes one saved recording with its descriptive metadata.
type RecordingRow struct {
	Name       string `json:"name"`
	Track      string `json:"track"`
	Car        string `json:"car"`
	Source     string `json:"source"`
	RecordedAt int64  `json:"recorded_at"` // unix seconds
	SizeBytes  int64  `json:"size_bytes"`
}

// UpsertRecording indexes (or refreshes) a recording's metadata.
func (s *Store) UpsertRecording(r RecordingRow) error {
	_, err := s.db.Exec(
		`INSERT INTO recordings (name, track, car, source, recorded_at, size_bytes)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   track=excluded.track, car=excluded.car, source=excluded.source,
		   recorded_at=excluded.recorded_at, size_bytes=excluded.size_bytes`,
		r.Name, r.Track, r.Car, r.Source, r.RecordedAt, r.SizeBytes)
	if err != nil {
		return fmt.Errorf("upsert recording: %w", err)
	}
	return nil
}

// ListRecordings returns indexed recordings, newest first.
func (s *Store) ListRecordings() ([]RecordingRow, error) {
	rows, err := s.db.Query(
		`SELECT name, track, car, source, recorded_at, size_bytes
		   FROM recordings ORDER BY recorded_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list recordings: %w", err)
	}
	defer rows.Close()
	var out []RecordingRow
	for rows.Next() {
		var r RecordingRow
		if err := rows.Scan(&r.Name, &r.Track, &r.Car, &r.Source, &r.RecordedAt, &r.SizeBytes); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
