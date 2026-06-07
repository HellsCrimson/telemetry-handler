package analysis

import "telemetry-handler/forza"

// Tuning constants. Forza's slip-ratio units are not officially documented, so
// these thresholds are heuristics calibrated to feel right; tune them here.
const (
	// Pedal thresholds are on Forza's 0..255 scale.
	brakeOnThreshold    uint8 = 40  // brake considered "applied"
	throttleOnThreshold uint8 = 40  // throttle considered "applied"
	underThrottleMax    uint8 = 200 // below this is "not full throttle" for under-driving
	coastPedal          uint8 = 15  // pedal below this counts as released
	overlapPedal        uint8 = 25  // both pedals above this is an overlap
	overlapHeavy        uint8 = 150 // both pedals above this is a heavy overlap

	// Steering is on Forza's -127..127 scale. We use magnitude to tell straights
	// from corners.
	steerCorner int8 = 20
	// coastSteerMax is the steering magnitude below which the car counts as going
	// straight for coasting purposes. Coasting while still turning more than this
	// can be legitimate (you cannot be flat mid-corner), so it is not flagged.
	coastSteerMax int8 = 12

	// Slip ratios (dimensionless). Negative = the wheel turns slower than the
	// ground (lockup); positive = faster (wheelspin).
	lockupSlip    float32 = 0.20 // |slip| beyond this under braking is a lockup
	lockupMajor   float32 = 0.50 // lockup severity escalates beyond this
	wheelspinSlip float32 = 0.18 // slip beyond this under power is wheelspin
	wheelspinMaj  float32 = 0.50

	// Combined slip ~1.0 is the grip limit; below gripMargin there is grip to
	// spare (used by the under-driving detector).
	gripMargin float32 = 0.85

	// Longitudinal acceleration (m/s^2) marking a genuine acceleration phase.
	accelPositive float32 = 1.0

	// Speeds in m/s (Forza Speed is m/s).
	movingSpeed float32 = 3.0
	cornerSpeed float32 = 10.0

	// RPM fraction of the redline.
	overRevRatio    float32 = 0.99
	shortShiftRatio float32 = 0.80

	// Coalescing.
	coalesceGapMS uint64 = 60 // tolerate a dropped frame or two within a span

	// Per-detector minimum span durations (ms).
	minLockupMS    uint64 = 80
	minWheelspinMS uint64 = 80
	minUnderMS     uint64 = 250
	minCoastMS     uint64 = 300
	minOverlapMS   uint64 = 120
	minOverRevMS   uint64 = 200

	// Composite-score weights (penalty per percentage point).
	lockupWeight    float32 = 0.6
	wheelspinWeight float32 = 0.5
	coastingWeight  float32 = 0.2
	throttleWeight  float32 = 0.3
)

// runDetectors runs every detector over a single lap segment.
func runDetectors(seg lapSegment, dt int32) []Event {
	frames := seg.frames
	lap := seg.lap
	events := make([]Event, 0)

	events = append(events, detectBrakeLockup(frames, lap)...)
	events = append(events, detectCornerWheelspin(frames, lap, dt)...)
	events = append(events, detectUnderDriving(frames, lap, dt)...)
	events = append(events, detectCoasting(frames, lap)...)
	events = append(events, detectPedalOverlap(frames, lap)...)
	events = append(events, detectOverRev(frames, lap)...)
	events = append(events, detectShortShift(frames, lap)...)

	return events
}

func detectBrakeLockup(frames []Frame, lap int) []Event {
	spans := coalesce(frames, minLockupMS, func(t forza.Telemetry) (bool, float32) {
		if t.Brake <= brakeOnThreshold || t.Speed <= movingSpeed {
			return false, 0
		}
		worst := minWheelSlip(t) // most negative slip
		if worst < -lockupSlip {
			return true, worst
		}
		return false, 0
	})
	out := make([]Event, 0, len(spans))
	for _, s := range spans {
		sev := SeverityMinor
		if s.peak < -lockupMajor {
			sev = SeverityMajor
		}
		out = append(out, eventFromSpan(frames, s, lap, KindBrakeLockup, sev,
			"Wheels locked under braking.",
			"Ease the initial brake pressure (or brake a touch earlier) and trail off smoothly as the tire loads."))
	}
	return out
}

func detectCornerWheelspin(frames []Frame, lap int, dt int32) []Event {
	spans := coalesce(frames, minWheelspinMS, func(t forza.Telemetry) (bool, float32) {
		if t.Accel <= throttleOnThreshold || t.Speed <= movingSpeed {
			return false, 0
		}
		if absSteer(t.Steer) <= steerCorner {
			return false, 0
		}
		peak := maxDriveSlip(t, dt)
		if peak > wheelspinSlip {
			return true, peak
		}
		return false, 0
	})
	out := make([]Event, 0, len(spans))
	for _, s := range spans {
		sev := SeverityMinor
		if s.peak > wheelspinMaj {
			sev = SeverityMajor
		}
		out = append(out, eventFromSpan(frames, s, lap, KindCornerWheelspin, sev,
			"Drive wheels spinning on corner exit.",
			"Feed the throttle more progressively and wait for the car to straighten before going to full power."))
	}
	return out
}

func detectUnderDriving(frames []Frame, lap int, dt int32) []Event {
	spans := coalesce(frames, minUnderMS, func(t forza.Telemetry) (bool, float32) {
		if t.Speed <= cornerSpeed {
			return false, 0
		}
		if t.Accel >= underThrottleMax {
			return false, 0
		}
		if t.AccelerationZ <= accelPositive {
			return false, 0
		}
		if absSteer(t.Steer) >= steerCorner {
			return false, 0
		}
		if maxDriveCombinedSlip(t, dt) >= gripMargin {
			return false, 0
		}
		// metric: how much throttle was left on the table (0..1).
		return true, 1 - float32(t.Accel)/255
	})
	out := make([]Event, 0, len(spans))
	for _, s := range spans {
		sev := SeverityMinor
		if s.peak < 0.25 {
			sev = SeverityInfo
		}
		out = append(out, eventFromSpan(frames, s, lap, KindUnderDriving, sev,
			"Grip available but throttle not fully applied on exit.",
			"The tires had grip to spare — get to full throttle earlier once the car is settled."))
	}
	return out
}

// isCoasting reports a "wasted coasting" frame: moving in a forward gear, with
// neither pedal applied, while essentially going straight. Coasting mid-corner
// is excluded because neutral throttle there can be a legitimate balancing
// phase rather than lost time. Shared by the detector and the scorecard so both
// count coasting the same way.
func isCoasting(t forza.Telemetry) bool {
	if t.Speed <= movingSpeed || t.Gear == 0 {
		return false
	}
	if absSteer(t.Steer) > coastSteerMax {
		return false
	}
	return t.Accel < coastPedal && t.Brake < coastPedal
}

func detectCoasting(frames []Frame, lap int) []Event {
	spans := coalesce(frames, minCoastMS, func(t forza.Telemetry) (bool, float32) {
		if isCoasting(t) {
			return true, 1
		}
		return false, 0
	})
	out := make([]Event, 0, len(spans))
	for _, s := range spans {
		dur := frames[s.endIdx].OffsetMS - frames[s.startIdx].OffsetMS
		sev := SeverityInfo
		if dur > 700 {
			sev = SeverityMinor
		}
		out = append(out, eventFromSpan(frames, s, lap, KindCoasting, sev,
			"Coasting with no throttle or brake while going (nearly) straight.",
			"Minimize coasting on straights and exits: stay on the brakes a little longer, then transition straight to throttle."))
	}
	return out
}

func detectPedalOverlap(frames []Frame, lap int) []Event {
	spans := coalesce(frames, minOverlapMS, func(t forza.Telemetry) (bool, float32) {
		if t.Accel > overlapPedal && t.Brake > overlapPedal {
			// metric: the smaller of the two — bigger means heavier overlap.
			return true, float32(min(t.Accel, t.Brake))
		}
		return false, 0
	})
	out := make([]Event, 0, len(spans))
	for _, s := range spans {
		sev := SeverityMinor
		if s.peak > float32(overlapHeavy) {
			sev = SeverityMajor
		}
		out = append(out, eventFromSpan(frames, s, lap, KindPedalOverlap, sev,
			"Throttle and brake applied at the same time.",
			"If unintentional, release one pedal — heavy overlap scrubs speed and overheats the brakes."))
	}
	return out
}

func detectOverRev(frames []Frame, lap int) []Event {
	spans := coalesce(frames, minOverRevMS, func(t forza.Telemetry) (bool, float32) {
		if t.EngineMaxRpm <= 0 || t.Accel <= throttleOnThreshold {
			return false, 0
		}
		ratio := t.CurrentEngineRpm / t.EngineMaxRpm
		if ratio > overRevRatio {
			return true, ratio
		}
		return false, 0
	})
	out := make([]Event, 0, len(spans))
	for _, s := range spans {
		out = append(out, eventFromSpan(frames, s, lap, KindOverRev, SeverityMinor,
			"Held against the rev limiter.",
			"Shift up sooner to stay in the power band instead of bouncing off the limiter."))
	}
	return out
}

// detectShortShift is gear-transition aware: it looks at each up-shift and flags
// the ones taken well below peak RPM while under power. It emits one zero-length
// event per offending shift rather than coalescing.
func detectShortShift(frames []Frame, lap int) []Event {
	out := make([]Event, 0)
	for i := 1; i < len(frames); i++ {
		prev := frames[i-1].Telemetry
		cur := frames[i].Telemetry
		if cur.Gear <= prev.Gear || prev.Gear == 0 {
			continue // not an up-shift (or into/out of reverse)
		}
		if prev.EngineMaxRpm <= 0 || prev.Accel <= throttleOnThreshold {
			continue
		}
		ratio := prev.CurrentEngineRpm / prev.EngineMaxRpm
		if ratio >= shortShiftRatio {
			continue
		}
		f := frames[i]
		out = append(out, Event{
			Kind:       KindShortShift,
			Severity:   SeverityInfo,
			Lap:        lap,
			OffsetMS:   f.OffsetMS,
			DurationMS: 0,
			Position:   Position{X: f.Telemetry.PositionX, Y: f.Telemetry.PositionY, Z: f.Telemetry.PositionZ},
			Distance:   f.Telemetry.DistanceTraveled,
			Speed:      f.Telemetry.Speed * 3.6,
			Metric:     ratio,
			Message:    "Up-shifted well below peak power.",
			Suggestion: "Hold the gear longer and shift nearer the redline when accelerating hard.",
		})
	}
	return out
}

// --- wheel helpers ---

// minWheelSlip returns the most negative longitudinal slip across all 4 wheels.
func minWheelSlip(t forza.Telemetry) float32 {
	m := t.TireSlipRatioFrontLeft
	for _, v := range []float32{t.TireSlipRatioFrontRight, t.TireSlipRatioRearLeft, t.TireSlipRatioRearRight} {
		if v < m {
			m = v
		}
	}
	return m
}

// anyWheelLockup reports whether any wheel's slip is more negative than -thresh.
func anyWheelLockup(t forza.Telemetry, thresh float32) bool {
	return minWheelSlip(t) < -thresh
}

// driveSlips returns the longitudinal slip ratios of the driven wheels per
// DrivetrainType: 0=FWD (front), 1=RWD (rear), 2=AWD (all). Unknown values fall
// back to all four.
func driveSlips(t forza.Telemetry, dt int32) []float32 {
	front := []float32{t.TireSlipRatioFrontLeft, t.TireSlipRatioFrontRight}
	rear := []float32{t.TireSlipRatioRearLeft, t.TireSlipRatioRearRight}
	switch dt {
	case 0:
		return front
	case 1:
		return rear
	default:
		return append(front, rear...)
	}
}

// maxDriveSlip returns the largest (most positive) slip among the driven wheels.
func maxDriveSlip(t forza.Telemetry, dt int32) float32 {
	slips := driveSlips(t, dt)
	m := slips[0]
	for _, v := range slips[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

// anyDriveSlip reports whether any driven wheel spins beyond thresh. Drivetrain
// is read from the frame itself (used by the per-frame aggregate pass).
func anyDriveSlip(t forza.Telemetry, thresh float32) bool {
	return maxDriveSlip(t, t.DrivetrainType) > thresh
}

// maxDriveCombinedSlip returns the largest combined slip among the driven wheels.
func maxDriveCombinedSlip(t forza.Telemetry, dt int32) float32 {
	front := []float32{t.TireCombinedSlipFrontLeft, t.TireCombinedSlipFrontRight}
	rear := []float32{t.TireCombinedSlipRearLeft, t.TireCombinedSlipRearRight}
	var slips []float32
	switch dt {
	case 0:
		slips = front
	case 1:
		slips = rear
	default:
		slips = append(front, rear...)
	}
	m := slips[0]
	for _, v := range slips[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func absSteer(s int8) int8 {
	if s < 0 {
		return -s
	}
	return s
}
