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

func TestMiniSectorIndex(t *testing.T) {
	cases := []struct {
		frac float64
		want int
	}{
		{0, 0},
		{0.0499, 0},
		{0.05, 1},
		{0.5, numMiniSectors / 2},
		{0.999, numMiniSectors - 1},
		{1.0, numMiniSectors - 1}, // clamped
		{1.5, numMiniSectors - 1}, // clamped
		{-0.2, 0},                 // clamped
	}
	for _, c := range cases {
		if got := miniSectorIndex(c.frac); got != c.want {
			t.Errorf("miniSectorIndex(%v) = %d, want %d", c.frac, got, c.want)
		}
	}
}

func TestLapAccumulatorAccumulates(t *testing.T) {
	var a lapAccumulator
	// Drive one full lap across all mini-sectors, burning fuel and wearing tires
	// linearly, then cross the line (lap 2) to finalize lap 1. Each step advances
	// the lap fraction by one mini-sector.
	fuel := 100.0
	wear := 1.0 // 1.0 = fresh
	et := 0.0
	for step := 0; step <= numMiniSectors; step++ {
		frac := float64(step) / numMiniSectors
		lap := int32(1)
		if step == numMiniSectors {
			frac = 0 // crossed the line
			lap = 2
		}
		a.update(sample{lap: lap, frac: frac, wear: [4]float64{wear, wear, wear, wear}, fuel: fuel, et: et, speed: 50})
		fuel -= 1.0 // 1L per mini-sector
		wear -= 0.01
		et += 2.0
	}

	last := a.lastLap()
	if last == nil {
		t.Fatal("expected a completed lap")
	}
	if len(last) != numMiniSectors {
		t.Fatalf("len(last) = %d, want %d", len(last), numMiniSectors)
	}
	// Each closed mini-sector burned ~1L and ~0.01 wear per wheel.
	var totalFuel float64
	for _, m := range last {
		totalFuel += m.FuelUsed
	}
	if totalFuel < numMiniSectors-2 || totalFuel > numMiniSectors+2 {
		t.Errorf("total fuel over lap = %v, want ~%d", totalFuel, numMiniSectors)
	}
	// A representative mini-sector should show positive wear consumption.
	if last[5].TireWear[0] <= 0 {
		t.Errorf("mini-sector 5 FL wear = %v, want > 0", last[5].TireWear[0])
	}
	if last[5].TimeSpent <= 0 {
		t.Errorf("mini-sector 5 time = %v, want > 0", last[5].TimeSpent)
	}
}

func TestEventDetectorFlagsAndPits(t *testing.T) {
	e := New()
	// Baseline frame: green, no one pitting.
	f := buildFrame()
	f.Rules.SafetyCarExists = 0 // start with no SC so the first frame is green
	e.Observe(f)
	if len(e.Snapshot().Events) != 0 {
		t.Fatalf("baseline frame should emit no events, got %v", e.Snapshot().Events)
	}

	// Next frame: safety car deployed + the AI car enters the pits.
	f2 := buildFrame()
	f2.Rules.SafetyCarExists = 1
	f2.Rules.SafetyCarActive = 1
	f2.Vehicles[1].Scoring.PitState = 2 // entering
	f2.ScoringInfo.CurrentET = 610
	e.Observe(f2)

	events := e.Snapshot().Events
	if len(events) < 2 {
		t.Fatalf("expected SC + pit events, got %d: %+v", len(events), events)
	}
	var sawSC, sawPit bool
	for _, ev := range events {
		if ev.Kind == "flag" && ev.Message == "Safety car deployed" {
			sawSC = true
		}
		if ev.Kind == "pit" {
			sawPit = true
		}
	}
	if !sawSC || !sawPit {
		t.Errorf("missing events: sc=%v pit=%v (%+v)", sawSC, sawPit, events)
	}
}

func TestDownsample(t *testing.T) {
	var path []Vec2
	for i := range 1000 {
		path = append(path, Vec2{X: float64(i), Z: 0})
	}
	out := downsample(path, 240)
	if len(out) != 240 {
		t.Errorf("len = %d, want 240", len(out))
	}
	if out[0].X != 0 {
		t.Errorf("first point X = %v, want 0", out[0].X)
	}
	// A short path is returned unchanged.
	short := []Vec2{{X: 1}, {X: 2}}
	if got := downsample(short, 240); len(got) != 2 {
		t.Errorf("short path len = %d, want 2", len(got))
	}
}

func TestLapAccumulatorBestLap(t *testing.T) {
	var a lapAccumulator
	// Drive four laps with a single monotonic clock — the lap-number change at each
	// sector 0 is what rolls the lap over, exactly like real telemetry. Per-sector
	// time sets each lap's pace: lap 2 (1.5/sector = 30s) is the quickest. The first
	// lap is excluded from the best reference; the fourth just finalizes lap 3.
	secTime := []float64{2.0, 1.5, 2.0, 2.0}
	et := 0.0
	for lap := 1; lap <= 4; lap++ {
		st := secTime[lap-1]
		for s := range numMiniSectors {
			a.update(sample{lap: int32(lap), frac: float64(s) / numMiniSectors, et: et, speed: 50})
			et += st
		}
	}

	if a.bestLap() == nil {
		t.Fatal("expected a best lap")
	}
	if bt := a.bestLapTime(); bt < 29 || bt > 31 {
		t.Errorf("bestLapTime = %v, want ~30 (the middle lap)", bt)
	}
}

func TestLapAccumulatorNoLapYet(t *testing.T) {
	var a lapAccumulator
	a.update(sample{lap: 1, frac: 0.1, fuel: 50, speed: 40})
	if a.lastLap() != nil {
		t.Error("no completed lap should be available after one sample")
	}
	if a.lapInProgress() == nil {
		t.Error("in-progress lap should exist after the first sample")
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
