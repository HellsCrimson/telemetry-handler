package overlay

import (
	"testing"
	"time"

	"telemetry-handler/forza"
)

func TestFormatHUDFormatsTelemetry(t *testing.T) {
	now := time.Now()
	hud := FormatHUD(forza.Telemetry{
		Speed:            50,
		CurrentEngineRpm: 4200,
		EngineMaxRpm:     7000,
		Gear:             4,
		Accel:            255,
		Brake:            128,
		Clutch:           0,
	}, true, now, now)

	if !hud.Connected || hud.Stale {
		t.Fatalf("unexpected connection state: %+v", hud)
	}
	if hud.SpeedKPH != "180" || hud.Gear != "4" || hud.RPM != "4200" || hud.MaxRPM != "7000" {
		t.Fatalf("unexpected formatted values: %+v", hud)
	}
	if hud.RPMRatio != 0.6 {
		t.Fatalf("RPMRatio = %v, want 0.6", hud.RPMRatio)
	}
	if hud.Throttle != 1 || hud.Brake < 0.5 || hud.Brake > 0.51 || hud.Clutch != 0 {
		t.Fatalf("unexpected pedal ratios: %+v", hud)
	}
}

func TestFormatHUDClampsRPMRatio(t *testing.T) {
	now := time.Now()
	hud := FormatHUD(forza.Telemetry{CurrentEngineRpm: 9000, EngineMaxRpm: 7000}, true, now, now)

	if hud.RPMRatio != 1 {
		t.Fatalf("RPMRatio = %v, want 1", hud.RPMRatio)
	}
}

func TestFormatHUDHandlesMissingTelemetry(t *testing.T) {
	hud := FormatHUD(forza.Telemetry{}, false, time.Time{}, time.Now())

	if hud.Connected || !hud.Stale {
		t.Fatalf("unexpected missing state: %+v", hud)
	}
}

func TestFormatHUDHandlesStaleTelemetry(t *testing.T) {
	now := time.Now()
	hud := FormatHUD(forza.Telemetry{}, true, now.Add(-3*time.Second), now)

	if !hud.Connected || !hud.Stale {
		t.Fatalf("unexpected stale state: %+v", hud)
	}
}
