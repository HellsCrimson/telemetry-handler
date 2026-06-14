package store

import (
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestReferenceLapKeepsFastest(t *testing.T) {
	s := openTemp(t)

	if err := s.SaveReferenceLap(ReferenceLap{Track: "Le Mans", Car: "499P", Class: "HYPERCAR", LapTime: 210.5, Sectors: "[1]", Path: "[2]"}); err != nil {
		t.Fatal(err)
	}
	// A slower lap must NOT overwrite.
	if err := s.SaveReferenceLap(ReferenceLap{Track: "Le Mans", Car: "499P", LapTime: 215.0, Sectors: "[9]", Path: "[9]"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetReferenceLap("Le Mans", "499P")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.LapTime != 210.5 || got.Sectors != "[1]" {
		t.Errorf("slower lap overwrote: %+v", got)
	}
	// A faster lap MUST overwrite.
	if err := s.SaveReferenceLap(ReferenceLap{Track: "Le Mans", Car: "499P", LapTime: 208.0, Sectors: "[3]", Path: "[3]"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetReferenceLap("Le Mans", "499P")
	if got.LapTime != 208.0 || got.Sectors != "[3]" {
		t.Errorf("faster lap did not overwrite: %+v", got)
	}

	if _, ok, _ := s.GetReferenceLap("Spa", "499P"); ok {
		t.Error("unexpected reference for unknown track")
	}
}

func TestCornersRoundTrip(t *testing.T) {
	s := openTemp(t)
	if err := s.SaveCorners("Le Mans", `["T1","Str"]`); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetCorners("Le Mans")
	if err != nil || !ok || got != `["T1","Str"]` {
		t.Errorf("corners = %q ok=%v err=%v", got, ok, err)
	}
}

func TestSessionsAndRecordings(t *testing.T) {
	s := openTemp(t)
	id, err := s.StartSession("Spa", "GT3")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSession(id, 12, 138.4); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.ListSessions(10)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %d err=%v", len(sessions), err)
	}
	if sessions[0].Laps != 12 || sessions[0].BestLap != 138.4 {
		t.Errorf("session not updated: %+v", sessions[0])
	}

	if err := s.UpsertRecording(RecordingRow{Name: "lmu-1.fh6rec.gz", Track: "Spa", Car: "GT3", Source: "lmu", SizeBytes: 1000}); err != nil {
		t.Fatal(err)
	}
	recs, err := s.ListRecordings()
	if err != nil || len(recs) != 1 || recs[0].Track != "Spa" {
		t.Fatalf("recordings = %+v err=%v", recs, err)
	}
}
