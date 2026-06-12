package app

import (
	"testing"

	"telemetry-handler/lmu"
)

func TestLMUAdapterMapping(t *testing.T) {
	tel := lmuToTelemetry(lmu.Packet{
		EngineRPM:    7500,
		EngineMaxRPM: 9000,
		SpeedMS:      55.5,
		Gear:         3,
		Throttle:     1,
		Brake:        0.5,
		Clutch:       0,
		Steering:     -0.5,
		Fuel:         63.2,
		LapNumber:    4,
		VehicleName:  "GT3 #91",
	})

	if tel.IsRaceOn != 1 {
		t.Errorf("IsRaceOn = %d, want 1", tel.IsRaceOn)
	}
	if tel.CurrentEngineRpm != 7500 || tel.EngineMaxRpm != 9000 {
		t.Errorf("rpm = %v/%v", tel.CurrentEngineRpm, tel.EngineMaxRpm)
	}
	if tel.Speed != 55.5 {
		t.Errorf("speed = %v, want 55.5", tel.Speed)
	}
	if tel.Accel != 255 {
		t.Errorf("throttle byte = %d, want 255", tel.Accel)
	}
	if tel.Brake != 127 { // 0.5 * 255 = 127.5 truncated
		t.Errorf("brake byte = %d, want 127", tel.Brake)
	}
	if tel.Steer != -63 { // -0.5 * 127 = -63.5 truncated toward zero
		t.Errorf("steer = %d, want -63", tel.Steer)
	}
	if tel.Fuel != 63.2 {
		t.Errorf("fuel = %v", tel.Fuel)
	}
	if tel.LapNumber != 4 {
		t.Errorf("lap = %d", tel.LapNumber)
	}
	if tel.CarOrdinal == 0 {
		t.Error("expected non-zero CarOrdinal from vehicle name")
	}
}

func TestLMUGear(t *testing.T) {
	if g := lmuGear(-1); g != 0 { // reverse -> R
		t.Errorf("reverse gear = %d, want 0", g)
	}
	if g := lmuGear(0); g != lmuNeutralGear { // neutral -> sentinel
		t.Errorf("neutral gear = %d, want %d", g, lmuNeutralGear)
	}
	// Forward gears pass through unchanged and never collide with the sentinel,
	// so a sequential box that skips neutral on upshifts is never mislabeled "N".
	for _, in := range []int32{1, 2, 3, 4, 5, 6, 7, 8} {
		if g := lmuGear(in); g != uint8(in) {
			t.Errorf("forward gear %d = %d, want %d", in, g, in)
		}
		if uint8(in) == lmuNeutralGear {
			t.Errorf("forward gear %d collides with neutral sentinel", in)
		}
	}
}

func TestUnitConversionsClamp(t *testing.T) {
	if unitToByte(-0.2) != 0 || unitToByte(1.5) != 255 {
		t.Error("unitToByte clamp failed")
	}
	if unitToSteer(-2) != -127 || unitToSteer(2) != 127 {
		t.Error("unitToSteer clamp failed")
	}
}
