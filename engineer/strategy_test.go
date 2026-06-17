package engineer

import "testing"

// TestApplyStrategyMerge checks that the REST overlay is exposed on the snapshot
// and that an authoritative fuel-tank capacity is applied to the player car only.
func TestApplyStrategyMerge(t *testing.T) {
	e := New()
	// Seed a minimal state as Observe would leave it: a player car and a rival.
	e.state = SessionState{
		Available: true,
		PlayerID:  5,
		Cars: []CarState{
			{ID: 5, IsPlayer: true},
			{ID: 6},
		},
	}

	e.ApplyStrategy(StrategyState{
		Present:      true,
		GamePhase:    "GPHASE_GREEN",
		FuelCapacity: 117,
		PitEstimate:  PitEstimate{Total: 12.5, Fuel: 8},
		Drivers:      []DriverUsage{{Driver: "Player", Fuel: 2.1}},
	})

	snap := e.Snapshot()
	if !snap.Strategy.Present || snap.Strategy.GamePhase != "GPHASE_GREEN" {
		t.Fatalf("strategy overlay not exposed: %+v", snap.Strategy)
	}
	if snap.Strategy.PitEstimate.Total != 12.5 {
		t.Errorf("pit estimate not exposed: %+v", snap.Strategy.PitEstimate)
	}
	if snap.Cars[0].FuelCapacity != 117 {
		t.Errorf("player fuel capacity = %v, want 117", snap.Cars[0].FuelCapacity)
	}
	if snap.Cars[1].FuelCapacity != 0 {
		t.Errorf("rival fuel capacity should stay 0, got %v", snap.Cars[1].FuelCapacity)
	}
}

// TestApplyStrategyClear checks that an empty overlay clears a prior one.
func TestApplyStrategyClear(t *testing.T) {
	e := New()
	e.state = SessionState{Cars: []CarState{{ID: 5, IsPlayer: true}}}
	e.ApplyStrategy(StrategyState{Present: true, FuelCapacity: 100})
	if !e.Snapshot().Strategy.Present {
		t.Fatalf("expected strategy present after apply")
	}
	e.ApplyStrategy(StrategyState{}) // unavailable poll
	if e.Snapshot().Strategy.Present {
		t.Errorf("expected strategy cleared")
	}
}

// TestObserveNilClearsStrategy checks that a source switch (nil frame) resets the
// strategy overlay on the snapshot along with the rest of the state.
func TestObserveNilClearsStrategy(t *testing.T) {
	e := New()
	e.state = SessionState{Cars: []CarState{{ID: 5, IsPlayer: true}}}
	e.ApplyStrategy(StrategyState{Present: true, FuelCapacity: 100})
	e.Observe(nil)
	if e.Snapshot().Strategy.Present {
		t.Errorf("nil frame should clear the strategy overlay")
	}
}
