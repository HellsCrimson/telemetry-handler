package assistant

import (
	"strings"
	"testing"

	"telemetry-handler/engineer"
)

// sampleState builds a small race: the player P4 in a 7-car field, with the
// fuel/tyre/lap data the engineer is expected to reason over.
func sampleState() engineer.SessionState {
	car := func(id int32, place int, driver string, gapToLeader float64, stops int) engineer.CarState {
		return engineer.CarState{
			ID: id, Place: place, Driver: driver, Class: "LMP2",
			GapToLeader: gapToLeader, NumPitstops: stops, TotalLaps: 12,
			LastLap: 95.5, BestLap: 94.8,
		}
	}
	st := engineer.SessionState{
		Available: true, Game: "lmu", Track: "Sebring", TrackLength: 6019,
		SessionType: 10, SessionTime: 1200, SessionEndTime: 5400,
		Flags:   engineer.FlagState{Green: true},
		Weather: engineer.WeatherState{AmbientTemp: 24, TrackTemp: 31},
		Cars: []engineer.CarState{
			car(1, 1, "Leader", 0, 1),
			car(2, 2, "Second", 12.0, 1),
			car(3, 3, "Third", 20.5, 1),
			car(4, 4, "Player", 22.0, 1),
			car(5, 5, "Fifth", 23.1, 1),
			car(6, 6, "Sixth", 40.0, 2),
			car(7, 7, "Backmarker", 90.0, 0),
			car(8, 8, "Tail", 120.0, 0),
		},
		Events: []engineer.RaceEvent{{AtET: 900, Kind: "flag", Message: "Yellow in sector 2"}},
	}
	p := &st.Cars[3]
	p.IsPlayer = true
	p.CarName = "Oreca 07"
	p.Fuel = 40
	p.FuelCapacity = 75
	p.MiniSectors = []engineer.MiniSectorState{
		{FuelUsed: 2.0, TimeSpent: 47.5},
		{FuelUsed: 2.0, TimeSpent: 48.0},
	}
	for i := range p.Tires {
		p.Tires[i] = engineer.TireState{Wear: 0.8, Temp: [3]float64{80, 85, 82}, Compound: "Medium"}
	}
	return st
}

func TestBriefCarriesWhatTheEngineerNeeds(t *testing.T) {
	brief := Brief(sampleState())
	for _, want := range []string{
		"Sebring",       // track
		"race",          // session type
		"P4",            // the player's position
		"Oreca 07",      // car
		"40.0 L",        // fuel remaining
		"laps left",     // the derived fuel range
		"Medium",        // tyre compound
		"20% worn",      // wear, as consumed rather than remaining
		"Second",        // a car ahead
		"Fifth",         // a car behind
		"Yellow in sec", // the event timeline
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief is missing %q:\n%s", want, brief)
		}
	}
}

// The briefing must describe the cars the driver is racing, not the whole field —
// a full grid would crowd out everything else in the prompt.
func TestBriefTrimsToNearbyCars(t *testing.T) {
	brief := Brief(sampleState())
	if strings.Contains(brief, "Tail") {
		t.Errorf("a car 4 places away should not be in the brief:\n%s", brief)
	}
	if !strings.Contains(brief, "Leader") || !strings.Contains(brief, "Backmarker") {
		t.Errorf("cars 3 places away should be in the brief:\n%s", brief)
	}
}

// Gaps between arbitrary cars are derived from their gaps to the leader, which
// is the only interval the game reports for pairs.
func TestBriefReportsGapsBetweenNeighbours(t *testing.T) {
	brief := Brief(sampleState())
	if !strings.Contains(brief, "1.5s") { // player 22.0 - third 20.5
		t.Errorf("expected the 1.5s gap to the car ahead:\n%s", brief)
	}
	if !strings.Contains(brief, "1.1s") { // fifth 23.1 - player 22.0
		t.Errorf("expected the 1.1s gap to the car behind:\n%s", brief)
	}
}

func TestBriefWithoutSession(t *testing.T) {
	if got := Brief(engineer.SessionState{}); !strings.Contains(got, "No live session") {
		t.Errorf("expected a no-session brief, got %q", got)
	}
}

// A lap the app only partly saw burns less than a lap of fuel while still
// looking complete. Dividing by it would invent range — the failure that had the
// engineer announcing a fuel state that was not real.
func TestFuelRangeRejectsAPartiallyCapturedLap(t *testing.T) {
	st := sampleState()
	st.Cars[3].MiniSectors[0].TimeSpent = 0 // never driven under observation
	if _, _, ok := fuelRange(st, *playerCar(st)); ok {
		t.Error("a partial lap must not produce a fuel estimate")
	}
}

func TestFuelRangeNeedsACompletedLap(t *testing.T) {
	st := sampleState()
	st.Cars[3].MiniSectors = nil
	if _, _, ok := fuelRange(st, *playerCar(st)); ok {
		t.Error("no completed lap must not produce a fuel estimate")
	}
}

// With no trustworthy estimate the briefing simply omits the range, rather than
// telling the model something false.
func TestBriefOmitsFuelRangeWhenItIsNotTrustworthy(t *testing.T) {
	st := sampleState()
	st.Cars[3].MiniSectors = nil
	brief := Brief(st)
	if strings.Contains(brief, "laps left") {
		t.Errorf("brief should not claim a fuel range without a clean lap:\n%s", brief)
	}
	if !strings.Contains(brief, "40.0 L") {
		t.Errorf("the litres in the tank are still known:\n%s", brief)
	}
}

func TestFuelRangeUsesLastLapConsumption(t *testing.T) {
	st := sampleState()
	laps, per, ok := fuelRange(st, *playerCar(st))
	if !ok {
		t.Fatal("expected a fuel estimate")
	}
	if per != 4.0 { // 2.0 + 2.0 across the mini-sectors
		t.Errorf("per-lap consumption = %v, want 4", per)
	}
	if laps != 10 { // 40 L / 4 L per lap
		t.Errorf("laps remaining = %v, want 10", laps)
	}
}

// strategyState adds the REST-sourced extras and a best lap to compare against.
func strategyState() engineer.SessionState {
	st := sampleState()
	st.Corners = []string{"T1", "T2"}
	st.Strategy = engineer.StrategyState{
		Present:      true,
		FuelCapacity: 75,
		PitEstimate:  engineer.PitEstimate{Total: 32},
		PitMenu: []engineer.PitMenuEntry{
			{Name: "FL TIRE:", Current: "New Medium"},
			{Name: "VIRTUAL ENERGY:", Current: "80%"},
			{Name: "EMPTY:", Current: "  "},
		},
		Forecast: []engineer.ForecastPoint{
			{Node: "NODE_25", RainChance: 10, Temperature: 24, Sky: "Light Clouds"},
			{Node: "NODE_50", RainChance: 60, Temperature: 22, Sky: "Overcast"},
			{Node: "NODE_75", RainChance: 90, Temperature: 20, Sky: "Rain"},
			{Node: "FINISH", RainChance: 95, Temperature: 19, Sky: "Rain"},
		},
	}
	p := &st.Cars[3]
	p.MiniSectors[0].TimeSpent = 48.0 // 0.5s off the best
	p.MiniSectors[1].TimeSpent = 47.6 // 0.1s off
	p.BestSectors = []engineer.MiniSectorState{{TimeSpent: 47.5}, {TimeSpent: 47.5}}
	return st
}

// Without the staged menu the engineer proposes changes blind and cannot say
// what is already set for the stop.
func TestBriefCarriesTheStagedPitMenu(t *testing.T) {
	brief := Brief(strategyState())
	if !strings.Contains(brief, "FL TIRE: New Medium") || !strings.Contains(brief, "VIRTUAL ENERGY: 80%") {
		t.Errorf("staged pit menu missing:\n%s", brief)
	}
	if strings.Contains(brief, "EMPTY:") {
		t.Errorf("entries with no selection are noise:\n%s", brief)
	}
}

// "Do I need wets?" turns entirely on the forecast, but only the next few nodes
// matter — the rest is prompt weight.
func TestBriefCarriesTheNextForecastPoints(t *testing.T) {
	brief := Brief(strategyState())
	if !strings.Contains(brief, "60% rain") {
		t.Errorf("forecast missing:\n%s", brief)
	}
	if strings.Contains(brief, "FINISH") || strings.Contains(brief, "95%") {
		t.Errorf("forecast should be capped at %d points:\n%s", briefForecast, brief)
	}
}

// "How was that lap?" should be answerable with where the time actually went.
func TestBriefNamesWhereTheLastLapLostTime(t *testing.T) {
	brief := Brief(strategyState())
	if !strings.Contains(brief, "LAST LAP LOST") {
		t.Fatalf("sector losses missing:\n%s", brief)
	}
	if !strings.Contains(brief, "T1 +0.50s") {
		t.Errorf("the worst sector should be named and quantified:\n%s", brief)
	}
	// 0.1s is above the floor and should appear; a corner name beats an index.
	if !strings.Contains(brief, "T2 +0.10s") {
		t.Errorf("expected the second loss:\n%s", brief)
	}
}

func TestBriefIgnoresSectorNoise(t *testing.T) {
	st := strategyState()
	st.Cars[3].MiniSectors[0].TimeSpent = 47.51 // 10ms — not worth saying
	st.Cars[3].MiniSectors[1].TimeSpent = 47.52
	if brief := Brief(st); strings.Contains(brief, "LAST LAP LOST") {
		t.Errorf("sub-%.2fs deltas should be dropped:\n%s", sectorLossFloor, brief)
	}
}

// A lap with no best to compare against must not invent a comparison.
func TestBriefSkipsSectorLossesWithoutAReference(t *testing.T) {
	st := strategyState()
	st.Cars[3].BestSectors = nil
	if brief := Brief(st); strings.Contains(brief, "LAST LAP LOST") {
		t.Errorf("no reference lap means no comparison:\n%s", brief)
	}
}
