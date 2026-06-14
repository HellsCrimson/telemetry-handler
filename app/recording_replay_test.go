package app

import (
	"encoding/json"
	"testing"
	"time"

	"telemetry-handler/config"
	"telemetry-handler/forza"
	"telemetry-handler/lmu"
	"telemetry-handler/recording"
)

// lmuPacketBytes marshals an lmu.Packet to its wire JSON, as the sidecar sends
// and the recorder stores it.
func lmuPacketBytes(t *testing.T, p lmu.Packet) []byte {
	t.Helper()
	p.Source = "lmu"
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal lmu packet: %v", err)
	}
	return b
}

// TestReplayRecordingDemux verifies a recording containing both Forza (binary)
// and LMU (JSON) packets replays each through the right decoder.
func TestReplayRecordingDemux(t *testing.T) {
	mgr, err := recording.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	status, err := mgr.Start("lmu")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	base := time.Unix(100, 0)
	// A 324-byte non-JSON buffer is a valid (zeroed) Forza packet.
	forzaPacket := make([]byte, forza.PacketSize)
	lmuPacket := lmuPacketBytes(t, lmu.Packet{
		VehicleName: "Test Car",
		TrackName:   "Test Track",
		EngineRPM:   4000,
		Gear:        3,
		Throttle:    1,
		LapNumber:   2,
	})
	if err := mgr.Record(forzaPacket, base); err != nil {
		t.Fatalf("record forza: %v", err)
	}
	if err := mgr.Record(lmuPacket, base.Add(20*time.Millisecond)); err != nil {
		t.Fatalf("record lmu: %v", err)
	}
	if _, err := mgr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	rt := NewRuntime(config.Default(), "", mgr, nil)
	samples, err := rt.ReplayRecording(status.Name, 0)
	if err != nil {
		t.Fatalf("ReplayRecording: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("len(samples) = %d, want 2", len(samples))
	}

	if samples[0].Source != "forza" {
		t.Errorf("sample[0].Source = %q, want forza", samples[0].Source)
	}
	if samples[1].Source != "lmu" {
		t.Errorf("sample[1].Source = %q, want lmu", samples[1].Source)
	}
	// The LMU frame must be mapped into the forza.Telemetry model and carry meta.
	if samples[1].Telemetry.CurrentEngineRpm != 4000 {
		t.Errorf("lmu rpm = %v, want 4000", samples[1].Telemetry.CurrentEngineRpm)
	}
	if samples[1].Meta.Car != "Test Car" || samples[1].Meta.Track != "Test Track" {
		t.Errorf("lmu meta = %+v, want car/track populated", samples[1].Meta)
	}
}

// TestAnalyzeRecordingLMU verifies coaching analysis runs over an LMU recording.
func TestAnalyzeRecordingLMU(t *testing.T) {
	mgr, err := recording.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	status, err := mgr.Start("lmu")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	base := time.Unix(0, 0)
	// Two laps of LMU frames so the analyzer has something to segment.
	for i := range 20 {
		lap := int32(1)
		if i >= 10 {
			lap = 2
		}
		pkt := lmuPacketBytes(t, lmu.Packet{
			VehicleName: "Test Car",
			EngineRPM:   5000,
			SpeedMS:     40,
			Gear:        4,
			Throttle:    1,
			LapNumber:   lap,
		})
		if err := mgr.Record(pkt, base.Add(time.Duration(i)*100*time.Millisecond)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if _, err := mgr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	rt := NewRuntime(config.Default(), "", mgr, nil)
	report, err := rt.AnalyzeRecording(status.Name, 0)
	if err != nil {
		t.Fatalf("AnalyzeRecording: %v", err)
	}
	// LMU frames are race-on (the adapter sets IsRaceOn=1), so they segment into
	// the two laps rather than being skipped.
	if len(report.Laps) != 2 {
		t.Fatalf("len(report.Laps) = %d, want 2", len(report.Laps))
	}
}
