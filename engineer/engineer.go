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
// offline over a recording). It folds each frame into rolling per-car aggregates,
// maintains the persisted reference lap + derived corner names, and tracks the
// chassis-balance heuristic, before storing the snapshot.
type Engineer struct {
	mu        sync.RWMutex
	state     SessionState
	accums    map[int32]*lapAccumulator // per-car (by slot ID) mini-sector accumulation
	events    []RaceEvent               // bounded race timeline
	det       *eventDetector            // frame-to-frame transition tracker
	compareID int32                     // rival selected for Driver Vs. line capture (-1 = none)
	balance   balanceTracker            // player chassis-balance heuristic

	// Reference lap (the player's persisted PB for the current track+car). Loaded
	// from the store on a context change and updated when the player beats it; the
	// app persists it when refDirty is set.
	ref      reference
	refTrack string
	refCar   string
	refClass string
	refDirty bool

	// Derived corner labels for the current track (from the reference lap), plus a
	// store-loaded fallback used before a reference lap exists this session.
	corners       []string
	cornersTrack  string
	cornersDirty  bool
	loadedCorners []string
	loadedTrack   string

	// curTrack/curCar/curClass is the context of the latest frame, so the app can
	// detect a track/car change and reload the reference from the store.
	curTrack string
	curCar   string
	curClass string
}

// reference is a stored/best lap held in memory for comparison + persistence.
type reference struct {
	time    float64
	sectors []MiniSectorState
	path    []Vec2
}

// New returns an idle Engineer. Its Snapshot reports Available=false until the
// first frame is observed.
func New() *Engineer {
	return &Engineer{accums: map[int32]*lapAccumulator{}, det: newEventDetector(), compareID: -1}
}

// SetCompareCar selects which rival's driven line the engine should buffer for the
// Driver Vs. line overlay (the player's is always buffered). Pass -1 for none.
// The buffer fills from the next lap the rival completes.
func (e *Engineer) SetCompareCar(id int32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.compareID = id
}

// ReferenceData is a reference lap crossing the app boundary (for persistence).
// The app marshals Sectors/Path to JSON for the store.
type ReferenceData struct {
	Track, Car, Class string
	Time              float64
	Sectors           []MiniSectorState
	Path              []Vec2
}

// CurrentContext returns the track/car/class of the latest frame, so the app can
// detect when to (re)load the reference lap from the store.
func (e *Engineer) CurrentContext() (track, car, class string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.curTrack, e.curCar, e.curClass
}

// SetReference loads a persisted reference lap (from the store) for a track+car.
// Pass time<=0 / nil to clear. It also derives corner labels from the lap so the
// names show from the session's first frame.
func (e *Engineer) SetReference(track, car, class string, time float64, sectors []MiniSectorState, path []Vec2) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ref = reference{time: time, sectors: sectors, path: path}
	e.refTrack, e.refCar, e.refClass = track, car, class
	e.refDirty = false // freshly loaded, nothing to save back
	if len(sectors) > 0 {
		e.corners = deriveCorners(sectors)
		e.cornersTrack = track
		e.cornersDirty = false
	}
}

// SetCorners provides store-loaded corner labels for a track, used as a fallback
// display before a reference lap exists this session.
func (e *Engineer) SetCorners(track string, labels []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.loadedCorners = labels
	e.loadedTrack = track
}

// TakeDirtyReference returns a reference lap that beat the stored one and needs
// persisting, clearing the dirty flag. ok is false when nothing changed.
func (e *Engineer) TakeDirtyReference() (ReferenceData, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.refDirty {
		return ReferenceData{}, false
	}
	e.refDirty = false
	return ReferenceData{
		Track: e.refTrack, Car: e.refCar, Class: e.refClass,
		Time: e.ref.time, Sectors: e.ref.sectors, Path: e.ref.path,
	}, true
}

// TakeDirtyCorners returns newly-derived corner labels that need persisting,
// clearing the dirty flag. ok is false when nothing changed.
func (e *Engineer) TakeDirtyCorners() (track string, labels []string, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cornersDirty {
		return "", nil, false
	}
	e.cornersDirty = false
	return e.cornersTrack, e.corners, true
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
		e.balance.reset()
		// keep compareID + reference: they should survive a brief source gap
		return
	}
	state := mapLMUFrame(f)
	trackLen := f.ScoringInfo.LapDist
	playerID := int32(-1)
	playerIdx := -1
	if p, ok := f.Player(); ok {
		playerID = p.Telemetry.ID
	}

	// Accumulate per-mini-sector usage for EVERY car (Driver Vs. compares any
	// rival's best lap). Done here, not in the mapper, because it is stateful
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
		// Buffer the driven line for the player and the selected compare car only.
		a.trackPath = id == playerID || id == e.compareID
		a.update(sampleFromVehicle(v, trackLen))

		state.Cars[i].MiniSectors = a.lastLap()
		state.Cars[i].BestSectors = a.bestLap()
		state.Cars[i].BestMeasured = a.bestLapTime()
		if a.trackPath {
			state.Cars[i].LapPath = a.lastLapPath()
			state.Cars[i].BestPath = a.bestLapPath()
		}
		if id == playerID {
			state.Cars[i].LapInProgress = a.lapInProgress()
			playerIdx = i
		}
	}

	// Reference lap, corner names and balance — all player-centric.
	if playerIdx >= 0 {
		e.curTrack = state.Track
		e.curCar = state.Cars[playerIdx].CarName
		e.curClass = state.Cars[playerIdx].Class
		e.updateReference(playerID)
		e.attachReference(&state, playerIdx)

		if p, ok := f.Player(); ok {
			e.balance.update(playerFrontRearSlip(&p.Telemetry))
		}
		state.Player.Balance = e.balance.state()
	}
	state.Corners = e.cornerLabels()

	// Generate race-timeline events from this frame's transitions.
	e.events = e.det.detect(f, state.Flags, e.events)
	state.Events = e.events

	e.state = state
}

// updateReference promotes the player's in-session best lap to the persisted
// reference when it's faster (or there's no reference yet), and re-derives corner
// names from it. Marks both dirty so the app saves them.
func (e *Engineer) updateReference(playerID int32) {
	a := e.accums[playerID]
	if a == nil {
		return
	}
	bt := a.bestLapTime()
	if bt <= 0 {
		return
	}
	if e.ref.time == 0 || bt < e.ref.time {
		e.ref = reference{time: bt, sectors: a.bestLap(), path: a.bestLapPath()}
		e.refTrack, e.refCar, e.refClass = e.curTrack, e.curCar, e.curClass
		e.refDirty = true
		e.corners = deriveCorners(e.ref.sectors)
		e.cornersTrack = e.curTrack
		e.cornersDirty = true
	}
}

// attachReference exposes the persisted reference lap as the player's "best" so
// the Coaching/Driver Vs. views compare against the all-time PB (which may be from
// a previous session), not just this session's best.
func (e *Engineer) attachReference(state *SessionState, playerIdx int) {
	if e.ref.time > 0 && e.refTrack == e.curTrack && e.refCar == e.curCar {
		state.Cars[playerIdx].BestSectors = e.ref.sectors
		state.Cars[playerIdx].BestPath = e.ref.path
		state.Cars[playerIdx].BestMeasured = e.ref.time
	}
}

// cornerLabels returns the corner labels to show: the reference-derived set when
// available for the current track, else the store-loaded fallback.
func (e *Engineer) cornerLabels() []string {
	if e.corners != nil && e.cornersTrack == e.curTrack {
		return e.corners
	}
	if e.loadedTrack == e.curTrack {
		return e.loadedCorners
	}
	return nil
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

// playerFrontRearSlip extracts the average front/rear wheel slip (GripFract =
// fraction of the contact patch sliding), the steering input and the speed — the
// four values the balance heuristic needs. Wheels are FL, FR, RL, RR.
func playerFrontRearSlip(vt *wire.VehicleTelemetry) (frontSlip, rearSlip, steering, speed float64) {
	frontSlip = (vt.Wheels[0].GripFract + vt.Wheels[1].GripFract) / 2
	rearSlip = (vt.Wheels[2].GripFract + vt.Wheels[3].GripFract) / 2
	steering = vt.UnfilteredSteering
	speed = math.Sqrt(vt.LocalVel.X*vt.LocalVel.X + vt.LocalVel.Y*vt.LocalVel.Y + vt.LocalVel.Z*vt.LocalVel.Z)
	return
}

// Snapshot returns the latest SessionState for the frontend. It is a value copy
// of the current state (the Cars slice is shared, but Observe always replaces the
// slice wholesale rather than mutating it, so readers never see a torn frame).
func (e *Engineer) Snapshot() SessionState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}
