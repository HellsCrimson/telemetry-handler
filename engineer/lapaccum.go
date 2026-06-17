package engineer

// This file holds the live, stateful per-lap accumulation — the reason the
// strategy engine lives in Go and sees every frame. It turns a stream of
// per-frame samples for one car into per-mini-sector resource usage (tire wear,
// fuel, energy, time, speeds), finalizing a lap when the lap number rolls over.
//
// Phase 2 runs one accumulator for the player car. It is deliberately structured
// around a single `sample` value (not the wire format) so a later phase can run
// one per car for Driver Vs., and so it is trivially unit-testable with synthetic
// samples.

// sample is the minimal slice of one car's telemetry the accumulator needs for a
// single frame. Extracting it (sampleFromVehicle) keeps the accumulator free of
// any wire-format dependency.
type sample struct {
	lap     int32      // current lap number (rollover marks a completed lap)
	frac    float64    // 0..1 position around the lap
	wear    [4]float64 // per-wheel tire wear reading
	fuel    float64    // liters remaining
	battery float64    // hybrid charge 0..1
	et      float64    // session elapsed time (s)
	speed   float64    // m/s
	posX    float64    // world X (for the driven line)
	posZ    float64    // world Z
}

// pathSamples is how many points a completed lap's driven line is downsampled to
// before crossing the bindings — enough to read the shape, small enough to send
// at the UI poll rate.
const pathSamples = 240

// wearIsFreshAtOne records how rF2 encodes tire wear: with mWear == 1.0 meaning a
// fresh tire and 0.0 fully worn, the amount CONSUMED across a mini-sector is
// (entry - exit). If real telemetry turns out to be the other way round, flip
// this single constant and the sign of the wear delta follows. (The Live Data /
// Coaching views show consumption as a positive number either way once correct.)
const wearIsFreshAtOne = true

// lapAccumulator folds per-frame samples for ONE car into per-mini-sector
// aggregates. Not goroutine-safe — the engineer's lock guards it.
type lapAccumulator struct {
	have   bool  // seen at least one sample
	lap    int32 // lap number of the in-progress lap
	sector int   // mini-sector currently being accumulated

	// entry snapshot for the current mini-sector
	enWear [4]float64
	enFuel float64
	enBatt float64
	enET   float64
	minSpd float64

	current []MiniSectorState // in-progress lap (len numMiniSectors)
	last    []MiniSectorState // last completed lap
	hasLast bool

	// curLapFull marks that the in-progress lap began by genuinely crossing the
	// start/finish line (the previous lap reached the final mini-sector before the
	// rollover), so it covers the whole track. The very first lap the engine sees
	// usually starts mid-track, and an out-lap from the garage begins wherever the
	// pit box is — both are partial and excluded from the best-lap reference.
	curLapFull bool

	// best* hold the fastest FULL lap seen (by summed mini-sector time), the
	// reference the Coaching/Driver Vs. views compare against.
	bestTime float64
	best     []MiniSectorState
	bestPath []Vec2

	// trackPath turns on driven-line capture (player + selected compare car — it's
	// heavy). When set, each update records the world position; on lap completion
	// the raw path is downsampled into lastPath/bestPath.
	trackPath bool
	curPath   []Vec2
	lastPath  []Vec2
}

// update folds one frame into the accumulator. It opens a mini-sector on first
// sight, closes-and-reopens on a mini-sector boundary, and closes the lap (moving
// `current` to `last`) when the lap number changes.
func (a *lapAccumulator) update(s sample) {
	idx := miniSectorIndex(s.frac)
	switch {
	case !a.have:
		a.have = true
		a.lap = s.lap
		a.current = make([]MiniSectorState, numMiniSectors)
		a.beginSector(idx, s)
	case s.lap != a.lap:
		a.closeSector(s)
		// A genuine lap completion crosses the start/finish line, so the finishing lap
		// must have reached the final mini-sector. Returning to the garage (or any
		// teleport) also bumps the lap number, but leaves the car mid-track — that is
		// NOT a completed lap and must not become the best/reference.
		endedAtLine := a.sector == numMiniSectors-1
		var donePath []Vec2
		if a.trackPath {
			donePath = downsample(a.curPath, pathSamples)
		}
		// Promote this completed lap to the best reference only if it was a full lap —
		// it both STARTED at the line (curLapFull) and ENDED by crossing it
		// (endedAtLine) — and is quicker than the stored best (by summed mini-sector
		// time). The end check is what rejects a lap abandoned in the garage, whose
		// partial sector sum would otherwise look like an impossibly fast lap.
		if lapTime := totalLapTime(a.current); a.curLapFull && endedAtLine && lapTime > 0 && (a.bestTime == 0 || lapTime < a.bestTime) {
			a.bestTime = lapTime
			a.best = a.current
			a.bestPath = donePath
		}
		a.last = a.current
		a.hasLast = true
		a.lastPath = donePath
		a.current = make([]MiniSectorState, numMiniSectors)
		a.lap = s.lap
		// The new lap is "full" only if this rollover was a real line crossing; an
		// out-lap that begins with a garage teleport starts mid-track and is partial.
		a.curLapFull = endedAtLine
		if a.trackPath {
			a.curPath = a.curPath[:0]
		}
		a.beginSector(idx, s)
	case idx != a.sector:
		a.closeSector(s)
		a.beginSector(idx, s)
	default:
		if s.speed < a.minSpd {
			a.minSpd = s.speed
		}
	}
	if a.trackPath {
		a.curPath = append(a.curPath, Vec2{X: s.posX, Z: s.posZ})
	}
}

// beginSector snapshots the entry state for mini-sector idx.
func (a *lapAccumulator) beginSector(idx int, s sample) {
	a.sector = idx
	a.enWear = s.wear
	a.enFuel = s.fuel
	a.enBatt = s.battery
	a.enET = s.et
	a.minSpd = s.speed
	a.current[idx].Index = idx
	a.current[idx].EntrySpeed = s.speed
}

// closeSector commits the deltas accumulated since beginSector into the current
// mini-sector. Uses += so jitter across a boundary accumulates rather than
// overwrites.
func (a *lapAccumulator) closeSector(s sample) {
	m := &a.current[a.sector]
	for i := range 4 {
		d := a.enWear[i] - s.wear[i]
		if !wearIsFreshAtOne {
			d = -d
		}
		if d > 0 { // ignore tiny negative noise (e.g. temp-driven wobble)
			m.TireWear[i] += d
		}
	}
	if used := a.enFuel - s.fuel; used > 0 {
		m.FuelUsed += used
	}
	m.BatteryUsed += a.enBatt - s.battery
	if s.et >= a.enET {
		m.TimeSpent += s.et - a.enET
	}
	m.ExitSpeed = s.speed
	m.MinSpeed = a.minSpd
}

// lastLap returns the most recent completed lap's mini-sectors, or nil if no lap
// has completed yet.
func (a *lapAccumulator) lastLap() []MiniSectorState {
	if !a.hasLast {
		return nil
	}
	return a.last
}

// lapInProgress returns the in-progress lap's mini-sectors (nil before the first
// sample).
func (a *lapAccumulator) lapInProgress() []MiniSectorState {
	if !a.have {
		return nil
	}
	return a.current
}

// lastLapPath returns the downsampled driven line of the last completed lap (nil
// when path capture is off or no lap has completed).
func (a *lapAccumulator) lastLapPath() []Vec2 {
	return a.lastPath
}

// bestLap / bestLapPath / bestLapTime return the fastest full lap's mini-sectors,
// driven line and time (the reference for comparisons). nil/0 until a full lap
// completes.
func (a *lapAccumulator) bestLap() []MiniSectorState { return a.best }
func (a *lapAccumulator) bestLapPath() []Vec2        { return a.bestPath }
func (a *lapAccumulator) bestLapTime() float64       { return a.bestTime }

// totalLapTime sums a lap's per-mini-sector time. Used to rank laps for the
// best-lap reference (it's consistent with how the engine measures, which is what
// matters for a like-for-like comparison).
func totalLapTime(sectors []MiniSectorState) float64 {
	var t float64
	for i := range sectors {
		t += sectors[i].TimeSpent
	}
	return t
}

// downsample reduces a path to at most n points by even-stride sampling, always
// keeping the last point so the loop closes. Returns the input unchanged when it
// already fits.
func downsample(path []Vec2, n int) []Vec2 {
	if n <= 0 || len(path) <= n {
		out := make([]Vec2, len(path))
		copy(out, path)
		return out
	}
	stride := float64(len(path)-1) / float64(n-1)
	out := make([]Vec2, 0, n)
	for i := range n {
		idx := int(float64(i) * stride)
		if idx >= len(path) {
			idx = len(path) - 1
		}
		out = append(out, path[idx])
	}
	return out
}
