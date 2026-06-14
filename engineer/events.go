package engineer

import (
	"fmt"

	"telemetry-handler/game/lmu/wire"
)

// This file generates the race timeline. The engine compares each frame to the
// previous one and emits a RaceEvent when something noteworthy changes: a global
// flag/safety-car change, a car entering the pits, or a heavy contact. It is the
// data behind both the timeline list and the transient popups.

// maxEvents bounds the retained timeline so a long race can't grow it without
// limit; the oldest events fall off the front.
const maxEvents = 60

// contactMagnitudeThreshold is the impact force (Newtons, rF2
// mLastImpactMagnitude) above which a contact is worth reporting — filters out
// kerb strikes and gravel rumble.
const contactMagnitudeThreshold = 25000

// eventDetector holds the previous-frame state needed to spot transitions. It is
// owned by the Engineer and guarded by its lock.
type eventDetector struct {
	inited     bool
	prevGreen  bool
	prevSC     bool
	prevPit    map[int32]uint8   // car ID -> previous PitState
	prevImpact map[int32]float64 // car ID -> previous LastImpactET
}

func newEventDetector() *eventDetector {
	return &eventDetector{prevPit: map[int32]uint8{}, prevImpact: map[int32]float64{}}
}

// reset clears state when the session/source changes.
func (d *eventDetector) reset() {
	*d = *newEventDetector()
}

// detect appends any new events for this frame to events and returns the bounded
// slice. flags is the already-mapped flag state (avoids re-deriving it).
func (d *eventDetector) detect(f *wire.Frame, flags FlagState, events []RaceEvent) []RaceEvent {
	et := f.ScoringInfo.CurrentET

	if !d.inited {
		// First frame: record the baseline without emitting (everything would look
		// like a change otherwise).
		d.inited = true
		d.prevGreen = flags.Green
		d.prevSC = flags.SCActive
		d.snapshotCars(f)
		return events
	}

	// Global flag / safety-car transitions.
	if flags.SCActive && !d.prevSC {
		events = append(events, RaceEvent{AtET: et, Kind: "flag", CarID: -1, Message: "Safety car deployed"})
	} else if !flags.SCActive && d.prevSC {
		events = append(events, RaceEvent{AtET: et, Kind: "flag", CarID: -1, Message: "Safety car in this lap"})
	}
	if !flags.Green && d.prevGreen && !flags.SCActive {
		events = append(events, RaceEvent{AtET: et, Kind: "flag", CarID: -1, Message: "Yellow flag"})
	} else if flags.Green && !d.prevGreen {
		events = append(events, RaceEvent{AtET: et, Kind: "flag", CarID: -1, Message: "Track clear (green)"})
	}
	d.prevGreen = flags.Green
	d.prevSC = flags.SCActive

	// Per-car: pit entry and contact.
	for i := range f.Vehicles {
		v := &f.Vehicles[i]
		id := v.Telemetry.ID
		name := carLabel(v)

		if v.HasScoring != 0 {
			pit := v.Scoring.PitState
			// 2 = entering the pit lane: report the transition into it.
			if pit == 2 && d.prevPit[id] != 2 {
				events = append(events, RaceEvent{AtET: et, Kind: "pit", CarID: id, Message: fmt.Sprintf("%s pits", name)})
			}
			d.prevPit[id] = pit
		}

		// Contact: a new impact timestamp above the magnitude threshold.
		impactET := v.Telemetry.LastImpactET
		if impactET > d.prevImpact[id] && v.Telemetry.LastImpactMagnitude >= contactMagnitudeThreshold {
			events = append(events, RaceEvent{AtET: et, Kind: "contact", CarID: id, Message: fmt.Sprintf("%s contact", name)})
		}
		if impactET > d.prevImpact[id] {
			d.prevImpact[id] = impactET
		}
	}

	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	return events
}

// snapshotCars records the baseline per-car state on the first frame.
func (d *eventDetector) snapshotCars(f *wire.Frame) {
	for i := range f.Vehicles {
		v := &f.Vehicles[i]
		id := v.Telemetry.ID
		if v.HasScoring != 0 {
			d.prevPit[id] = v.Scoring.PitState
		}
		d.prevImpact[id] = v.Telemetry.LastImpactET
	}
}

// carLabel builds a short identifier for a car in event messages: the driver name
// if known, else the car name, else the slot ID.
func carLabel(v *wire.Vehicle) string {
	if v.HasScoring != 0 {
		if n := wire.GoString(v.Scoring.DriverName[:]); n != "" {
			return n
		}
		if n := wire.GoString(v.Scoring.VehicleName[:]); n != "" {
			return n
		}
	}
	if n := wire.GoString(v.Telemetry.VehicleName[:]); n != "" {
		return n
	}
	return fmt.Sprintf("Car %d", v.Telemetry.ID)
}
