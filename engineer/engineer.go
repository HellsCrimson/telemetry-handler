package engineer

import (
	"math"
	"sync"

	"telemetry-handler/lmu/wire"
)

// Engineer is the live strategy engine. It observes every telemetry frame the
// receiver decodes and keeps the latest game-agnostic SessionState ready for the
// frontend to poll.
//
// It is the strategy counterpart to the overlay (another real-time per-frame
// consumer) and the analysis package (the same lap-segmentation idea, but run
// offline over a recording). Phase 1 only stores the latest mapped state; the
// stateful per-lap / per-mini-sector accumulation lands here in a later phase
// (Observe will then fold each frame into rolling per-car aggregates before
// storing the snapshot).
type Engineer struct {
	mu     sync.RWMutex
	state  SessionState
	accums map[int32]*lapAccumulator // per-car (by slot ID) mini-sector accumulation
	events []RaceEvent               // bounded race timeline
	det    *eventDetector            // frame-to-frame transition tracker
}

// New returns an idle Engineer. Its Snapshot reports Available=false until the
// first frame is observed.
func New() *Engineer {
	return &Engineer{accums: map[int32]*lapAccumulator{}, det: newEventDetector()}
}

// Observe folds one decoded LMU frame into the engine's state. It is called once
// per frame from the receiver goroutine, at the sidecar's full rate — this is the
// only place every frame is seen, which is why per-corner accumulation must live
// here rather than in the 5 Hz frontend poll. A nil frame (e.g. the source
// switched to Forza) resets the engine to idle.
func (e *Engineer) Observe(f *wire.Frame) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if f == nil {
		e.state = SessionState{}
		e.accums = map[int32]*lapAccumulator{}
		e.events = nil
		e.det.reset()
		return
	}
	state := mapLMUFrame(f)
	trackLen := f.ScoringInfo.LapDist
	playerID := int32(-1)
	if p, ok := f.Player(); ok {
		playerID = p.Telemetry.ID
	}

	// Accumulate per-mini-sector usage for EVERY car (Driver Vs. compares any
	// rival's last lap). Done here, not in the mapper, because it is stateful
	// across frames. The player car also captures the driven line and exposes the
	// in-progress lap.
	for i := range f.Vehicles {
		v := &f.Vehicles[i]
		id := v.Telemetry.ID
		a := e.accums[id]
		if a == nil {
			a = &lapAccumulator{}
			e.accums[id] = a
		}
		a.trackPath = id == playerID // only the player buffers a path
		a.update(sampleFromVehicle(v, trackLen))

		state.Cars[i].MiniSectors = a.lastLap()
		if id == playerID {
			state.Cars[i].LapInProgress = a.lapInProgress()
			state.Cars[i].LapPath = a.lastLapPath()
		}
	}

	// Generate race-timeline events from this frame's transitions.
	e.events = e.det.detect(f, state.Flags, e.events)
	state.Events = e.events

	e.state = state
}

// sampleFromVehicle extracts the per-frame slice the lap accumulator needs from a
// raw wire.Vehicle. Position around the lap comes from scoring's LapDist; if the
// car has no scoring row the fraction stays 0 (the accumulator still tracks the
// lap, just without sector segmentation).
func sampleFromVehicle(v *wire.Vehicle, trackLen float64) sample {
	vt := &v.Telemetry
	frac := 0.0
	if trackLen > 0 && v.HasScoring != 0 {
		frac = clamp01(v.Scoring.LapDist / trackLen)
	}
	speed := math.Sqrt(vt.LocalVel.X*vt.LocalVel.X + vt.LocalVel.Y*vt.LocalVel.Y + vt.LocalVel.Z*vt.LocalVel.Z)
	return sample{
		lap:     vt.LapNumber,
		frac:    frac,
		wear:    [4]float64{vt.Wheels[0].Wear, vt.Wheels[1].Wear, vt.Wheels[2].Wear, vt.Wheels[3].Wear},
		fuel:    vt.Fuel,
		battery: vt.BatteryChargeFraction,
		et:      vt.ElapsedTime,
		speed:   speed,
		posX:    vt.Pos.X,
		posZ:    vt.Pos.Z,
	}
}

// Snapshot returns the latest SessionState for the frontend. It is a value copy
// of the current state (the Cars slice is shared, but Observe always replaces the
// slice wholesale rather than mutating it, so readers never see a torn frame).
func (e *Engineer) Snapshot() SessionState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}
