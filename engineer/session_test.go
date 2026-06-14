package engineer

import (
	"math"
	"testing"

	"telemetry-handler/lmu/wire"
)

// putString copies a Go string into a fixed-width NUL-terminated byte field, the
// way the rF2 buffers store names. Used to build synthetic frames.
func putString(dst []byte, s string) {
	for i := range dst {
		dst[i] = 0
	}
	copy(dst, s)
}

// buildFrame assembles a minimal two-car LMU frame for the mapper tests: a player
// car (slot 7) leading and an AI car (slot 3) 1.5s behind.
func buildFrame() *wire.Frame {
	var player wire.Vehicle
	player.HasScoring = 1
	player.Telemetry.ID = 7
	player.Telemetry.Fuel = 42.5
	player.Telemetry.FuelCapacity = 100
	player.Telemetry.BatteryChargeFraction = 0.8
	putString(player.Telemetry.VehicleName[:], "Ferrari 499P")
	putString(player.Telemetry.FrontTireCompoundName[:], "Soft")
	putString(player.Telemetry.RearTireCompoundName[:], "Medium")
	// Front-left tire at ~90C (363.15K), worn to 0.7.
	player.Telemetry.Wheels[0].Temperature = [3]float64{363.15, 363.15, 363.15}
	player.Telemetry.Wheels[0].Pressure = 165
	player.Telemetry.Wheels[0].Wear = 0.7
	player.Telemetry.Wheels[0].BrakeTemp = 350
	player.Scoring.ID = 7
	player.Scoring.IsPlayer = 1
	player.Scoring.Place = 1
	player.Scoring.TotalLaps = 12
	player.Scoring.LapDist = 1500 // half-way round a 3000m lap
	player.Scoring.BestLapTime = 95.3
	player.Scoring.LastLapTime = 96.1
	player.Scoring.TimeBehindLeader = 0
	player.Scoring.TimeBehindNext = 0
	putString(player.Scoring.DriverName[:], "Partner")
	putString(player.Scoring.VehicleClass[:], "HYPERCAR")

	var ai wire.Vehicle
	ai.HasScoring = 1
	ai.Telemetry.ID = 3
	ai.Telemetry.Fuel = 38
	ai.Scoring.ID = 3
	ai.Scoring.Place = 2
	ai.Scoring.LapDist = 750
	ai.Scoring.TimeBehindLeader = 1.5
	ai.Scoring.TimeBehindNext = 1.5
	ai.Scoring.InPits = 1
	ai.Scoring.PitState = 3
	putString(ai.Scoring.DriverName[:], "AI Rival")
	putString(ai.Scoring.VehicleClass[:], "LMGT3")

	f := &wire.Frame{PlayerID: 7}
	f.ScoringInfo.LapDist = 3000
	f.ScoringInfo.Session = 10 // race
	f.ScoringInfo.CurrentET = 600
	f.ScoringInfo.MaxLaps = 30
	f.ScoringInfo.AmbientTemp = 24
	f.ScoringInfo.TrackTemp = 31
	putString(f.ScoringInfo.TrackName[:], "Le Mans")
	f.Weather.Raining = [9]float64{0.1, 0, 0, 0, 0, 0, 0, 0, 0.2}
	f.Weather.Cloudiness = 0.4
	f.Rules.SafetyCarExists = 1
	f.Vehicles = []wire.Vehicle{player, ai}
	return f
}

func TestMapLMUFrameSession(t *testing.T) {
	s := mapLMUFrame(buildFrame())
	if !s.Available {
		t.Fatal("expected Available")
	}
	if s.Game != "lmu" {
		t.Errorf("Game = %q, want lmu", s.Game)
	}
	if s.Track != "Le Mans" {
		t.Errorf("Track = %q", s.Track)
	}
	if s.TrackLength != 3000 {
		t.Errorf("TrackLength = %v", s.TrackLength)
	}
	if s.MaxLaps != 30 {
		t.Errorf("MaxLaps = %v", s.MaxLaps)
	}
	if s.PlayerID != 7 {
		t.Errorf("PlayerID = %v", s.PlayerID)
	}
	if len(s.Cars) != 2 {
		t.Fatalf("Cars = %d, want 2", len(s.Cars))
	}
	if !s.Flags.SafetyCar {
		t.Error("expected SafetyCar flag")
	}
	if s.Weather.AmbientTemp != 24 || s.Weather.TrackTemp != 31 {
		t.Errorf("weather temps = %v/%v", s.Weather.AmbientTemp, s.Weather.TrackTemp)
	}
	if s.Weather.RainGrid[8] != 0.2 {
		t.Errorf("rain grid not mapped: %v", s.Weather.RainGrid)
	}
}

func TestMapLMUFramePlayerCar(t *testing.T) {
	s := mapLMUFrame(buildFrame())
	p := s.Cars[0]
	if !p.IsPlayer {
		t.Error("expected car 0 to be player")
	}
	if p.Driver != "Partner" || p.Class != "HYPERCAR" {
		t.Errorf("driver/class = %q/%q", p.Driver, p.Class)
	}
	if p.CarName != "Ferrari 499P" {
		t.Errorf("CarName = %q", p.CarName)
	}
	if p.Place != 1 {
		t.Errorf("Place = %d", p.Place)
	}
	if p.Fuel != 42.5 || p.FuelCapacity != 100 {
		t.Errorf("fuel = %v/%v", p.Fuel, p.FuelCapacity)
	}
	if p.Battery != 0.8 {
		t.Errorf("Battery = %v", p.Battery)
	}
	// 1500m into a 3000m lap = halfway.
	if math.Abs(p.LapDistFrac-0.5) > 1e-9 {
		t.Errorf("LapDistFrac = %v, want 0.5", p.LapDistFrac)
	}
	if p.BestLap != 95.3 || p.LastLap != 96.1 {
		t.Errorf("lap times = %v/%v", p.BestLap, p.LastLap)
	}
	// Front-left compound = Soft, rear-left compound = Medium.
	if p.Tires[0].Compound != "Soft" || p.Tires[2].Compound != "Medium" {
		t.Errorf("compounds = %q/%q", p.Tires[0].Compound, p.Tires[2].Compound)
	}
	// 363.15K -> 90C.
	if math.Abs(p.Tires[0].Temp[0]-90) > 1e-6 {
		t.Errorf("FL temp = %v, want 90C", p.Tires[0].Temp[0])
	}
	if p.Tires[0].Wear != 0.7 || p.Tires[0].Pressure != 165 {
		t.Errorf("FL wear/pressure = %v/%v", p.Tires[0].Wear, p.Tires[0].Pressure)
	}
}

func TestMapLMUFrameRivalCar(t *testing.T) {
	s := mapLMUFrame(buildFrame())
	ai := s.Cars[1]
	if ai.IsPlayer {
		t.Error("rival should not be player")
	}
	if ai.GapToLeader != 1.5 {
		t.Errorf("GapToLeader = %v", ai.GapToLeader)
	}
	if !ai.InPits || ai.PitState != 3 {
		t.Errorf("pit state = %v/%d", ai.InPits, ai.PitState)
	}
	if math.Abs(ai.LapDistFrac-0.25) > 1e-9 {
		t.Errorf("LapDistFrac = %v, want 0.25", ai.LapDistFrac)
	}
}

func TestEngineerObserveSnapshot(t *testing.T) {
	e := New()
	if e.Snapshot().Available {
		t.Error("idle engineer should report unavailable")
	}
	e.Observe(buildFrame())
	if !e.Snapshot().Available {
		t.Error("expected Available after Observe")
	}
	// A nil frame (source switched to Forza) resets to idle.
	e.Observe(nil)
	if e.Snapshot().Available {
		t.Error("expected reset to unavailable after nil frame")
	}
}

// kToC unavailable readings stay 0 rather than going to -273.
func TestKToCZero(t *testing.T) {
	if got := kToC(0); got != 0 {
		t.Errorf("kToC(0) = %v, want 0", got)
	}
	if got := kToC(273.15); math.Abs(got) > 1e-9 {
		t.Errorf("kToC(273.15) = %v, want 0", got)
	}
}
