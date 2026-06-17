package app

import (
	"testing"

	"telemetry-handler/game/lmu/rest"
)

func TestRestToStrategyNilAndUnavailable(t *testing.T) {
	if restToStrategy(nil).Present {
		t.Errorf("nil snapshot should map to absent strategy")
	}
	if restToStrategy(&rest.Snapshot{Available: false}).Present {
		t.Errorf("unavailable snapshot should map to absent strategy")
	}
}

func TestRestToStrategyMapping(t *testing.T) {
	snap := &rest.Snapshot{
		Available:   true,
		GameState:   &rest.GameState{GamePhase: "GPHASE_GREEN", PitState: "EXITING"},
		PitEstimate: &rest.PitEstimate{Total: 12.5, Fuel: 8, Tires: 3, VE: 1.5},
		Condition:   &rest.VehicleCondition{FuelCapacity: 117},
		Usage: map[string][]rest.UsageEntry{
			"Player": {
				{Stint: 1, Lap: 1, VE: 0.9, Fuel: 2.0},
				{Stint: 2, Lap: 5, VE: 0.31, Fuel: 2.4}, // latest entry wins
			},
			"Rival": {{Stint: 1, VE: 1.0}},
			"Empty": {},
		},
		Forecast: []rest.ForecastNode{
			{Session: "PRACTICE", Node: "FINISH", Values: map[string]rest.ForecastValue{
				"WNV_RAIN_CHANCE": {CurrentValue: 5, StringValue: "5%"},
				"WNV_TEMPERATURE": {CurrentValue: 16, StringValue: "16 °"},
				"WNV_SKY":         {CurrentValue: 1, StringValue: "Light Clouds"},
				"WNV_WINDSPEED":   {CurrentValue: 11, StringValue: "39.6 kph"},
			}},
		},
		PitMenu: []rest.PitMenuItem{
			{Name: "VIRTUAL ENERGY:", CurrentSetting: 1, Settings: []string{"0% 0 laps", "31% 6 laps"}},
			{Name: "OUT OF RANGE:", CurrentSetting: 9, Settings: []string{"only"}},
		},
	}

	s := restToStrategy(snap)
	if !s.Present || s.GamePhase != "GPHASE_GREEN" || s.PitState != "EXITING" {
		t.Fatalf("game state not mapped: %+v", s)
	}
	if s.PitEstimate.Total != 12.5 || s.PitEstimate.VE != 1.5 {
		t.Errorf("pit estimate not mapped: %+v", s.PitEstimate)
	}
	if s.FuelCapacity != 117 {
		t.Errorf("fuel capacity = %v, want 117", s.FuelCapacity)
	}
	if len(s.Forecast) != 1 {
		t.Fatalf("forecast len = %d", len(s.Forecast))
	}
	f := s.Forecast[0]
	if f.RainChance != 5 || f.Temperature != 16 || f.Sky != "Light Clouds" || f.WindSpeed != 11 {
		t.Errorf("forecast point not mapped: %+v", f)
	}
	// Drivers: latest stint entry per driver; empty list skipped.
	if len(s.Drivers) != 2 {
		t.Fatalf("drivers len = %d, want 2 (Empty skipped): %+v", len(s.Drivers), s.Drivers)
	}
	for _, d := range s.Drivers {
		if d.Driver == "Player" && (d.Fuel != 2.4 || d.Stint != 2 || d.VE != 0.31) {
			t.Errorf("player usage not latest entry: %+v", d)
		}
	}
	// Pit menu: current resolved; out-of-range index → empty string, not a panic.
	if len(s.PitMenu) != 2 || s.PitMenu[0].Current != "31% 6 laps" || s.PitMenu[1].Current != "" {
		t.Errorf("pit menu not mapped: %+v", s.PitMenu)
	}
}
