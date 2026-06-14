package app

import (
	"math"
	"testing"

	"telemetry-handler/lmu"
	"telemetry-handler/lmu/wire"
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

func TestFrameToTelemetryMapsPlayer(t *testing.T) {
	var player wire.VehicleTelemetry
	player.ID = 42
	copy(player.VehicleName[:], "GT3 #91")
	player.EngineRPM = 7500
	player.EngineMaxRPM = 9000
	player.Gear = 3
	player.UnfilteredThrottle = 1
	player.UnfilteredBrake = 0.5
	player.UnfilteredSteering = -0.5
	player.Fuel = 63.2
	player.LapNumber = 4
	player.PhysicalSteeringWheelRange = 540
	player.LocalVel = wire.Vec3{X: 30, Y: 0, Z: 40} // |v| = 50
	player.LocalAccel = wire.Vec3{X: 1, Y: 2, Z: 3}
	player.Pos = wire.Vec3{X: 100, Y: 5, Z: 200}
	player.EngineTorque = 480
	// Front-left tire at 353.15 K -> 80 C average.
	player.Wheels[0].Temperature = [3]float64{353.15, 353.15, 353.15}

	other := wire.VehicleTelemetry{ID: 7}
	copy(other.VehicleName[:], "AI Car")

	f := wire.Frame{
		PlayerID:  42,
		PlayerIdx: 1,
		Vehicles: []wire.Vehicle{
			{Telemetry: other},
			{Telemetry: player},
		},
	}
	copy(f.ScoringInfo.TrackName[:], "Spa")
	f.ScoringInfo.CurrentET = 123.5

	tel := frameToTelemetry(&f)
	if tel.IsRaceOn != 1 || tel.CurrentEngineRpm != 7500 || tel.EngineMaxRpm != 9000 {
		t.Errorf("engine fields wrong: %+v", tel)
	}
	if math.Abs(float64(tel.Speed)-50) > 1e-3 {
		t.Errorf("speed = %v, want 50 (|LocalVel|)", tel.Speed)
	}
	if tel.Accel != 255 || tel.Brake != 127 || tel.Steer != -63 {
		t.Errorf("pedals/steer wrong: accel=%d brake=%d steer=%d", tel.Accel, tel.Brake, tel.Steer)
	}
	if tel.AccelerationX != 1 || tel.AccelerationZ != 3 || tel.PositionX != 100 || tel.PositionZ != 200 {
		t.Errorf("accel/pos vectors wrong: %+v", tel)
	}
	if math.Abs(float64(tel.TireTempFrontLeft)-80) > 1e-2 {
		t.Errorf("FL tire temp = %v, want ~80C", tel.TireTempFrontLeft)
	}
	if tel.Torque != 480 {
		t.Errorf("torque = %v, want 480", tel.Torque)
	}

	meta := frameToMeta(&f)
	if meta.Car != "GT3 #91" || meta.Track != "Spa" || meta.NumVehicles != 2 || meta.SteeringRangeDeg != 540 {
		t.Errorf("meta wrong: %+v", meta)
	}
	if math.Abs(meta.SessionTime-123.5) > 1e-6 {
		t.Errorf("session time = %v, want 123.5", meta.SessionTime)
	}
}

// TestFrameRoundTripThroughTransport simulates the full live path: marshal a
// frame, chunk it, feed the chunks through the reassembler, decode, and confirm
// the player car survives end to end.
func TestFrameRoundTripThroughTransport(t *testing.T) {
	var f wire.Frame
	f.PlayerIdx = 0
	f.PlayerID = 1
	for i := range 30 {
		var vt wire.VehicleTelemetry
		vt.ID = int32(i + 1)
		copy(vt.VehicleName[:], "car")
		vt.EngineRPM = float64(1000 * i)
		f.Vehicles = append(f.Vehicles, wire.Vehicle{Telemetry: vt})
	}
	copy(f.Vehicles[0].Telemetry.VehicleName[:], "Player")

	payload, err := wire.MarshalFrame(&f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	chunks := wire.Chunk(payload, f.Seq, 4096)
	var re wire.Reassembler
	var got wire.Frame
	done := false
	for _, c := range chunks {
		p, ok, err := re.Add(c)
		if err != nil {
			t.Fatalf("reassemble: %v", err)
		}
		if ok {
			got, err = wire.UnmarshalFrame(p)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			done = true
		}
	}
	if !done {
		t.Fatal("frame never reassembled")
	}
	if len(got.Vehicles) != 30 {
		t.Fatalf("got %d vehicles, want 30", len(got.Vehicles))
	}
	if tel := frameToTelemetry(&got); tel.CarOrdinal == 0 {
		t.Error("expected player CarOrdinal from name")
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
