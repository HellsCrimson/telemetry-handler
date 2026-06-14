// Package analysis turns a recorded Forza telemetry session into driving-coach
// feedback: a per-lap scorecard of aggregate metrics plus a list of individual
// events (lockups, wheelspin, coasting, ...) each pinned to a lap and track
// location with a suggested fix.
//
// It is deliberately decoupled from the rest of the app: it consumes its own
// Frame type (offset + parsed forza.Telemetry) and only imports forza, so it can
// be unit-tested with synthetic frames. The thresholds that drive the detectors
// are heuristics (Forza's slip-ratio units are not officially documented); they
// live as named constants in detectors.go so they are easy to tune.
package analysis

import (
	"math"
	"sort"

	"telemetry-handler/game/forza"
)

// Frame is the unit of analysis input: a parsed telemetry frame plus its offset
// from the start of the recording, in milliseconds. The app layer adapts its
// []ReplaySample into []Frame.
type Frame struct {
	OffsetMS  uint64
	Telemetry forza.Telemetry
}

// Severity ranks how strongly an event deserves the driver's attention.
type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityMinor Severity = "minor"
	SeverityMajor Severity = "major"
)

// EventKind identifies the detector that produced an event.
type EventKind string

const (
	KindBrakeLockup     EventKind = "brake_lockup"
	KindCornerWheelspin EventKind = "corner_wheelspin"
	KindUnderDriving    EventKind = "under_driving"
	KindCoasting        EventKind = "coasting"
	KindPedalOverlap    EventKind = "pedal_overlap"
	KindOverRev         EventKind = "over_rev"
	KindShortShift      EventKind = "short_shift"
)

// Position is the world location where an event began.
type Position struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// Event is a single detected coaching opportunity, coalesced from one or more
// consecutive offending frames.
type Event struct {
	Kind       EventKind `json:"kind"`
	Severity   Severity  `json:"severity"`
	Lap        int       `json:"lap"`
	OffsetMS   uint64    `json:"offset_ms"`   // span start, from the start of the recording
	DurationMS uint64    `json:"duration_ms"` // span length (0 for instantaneous events)
	Position   Position  `json:"position"`    // location at span start
	Distance   float32   `json:"distance"`    // DistanceTraveled at span start
	Speed      float32   `json:"speed"`       // km/h at span start
	Metric     float32   `json:"metric"`      // peak offending value (e.g. peak slip ratio)
	Message    string    `json:"message"`     // what happened
	Suggestion string    `json:"suggestion"`  // how to improve
}

// LapScore holds the aggregate metrics for one lap (or, when Lap is -1, the
// whole session). Percentages are 0..100; scores are 0..100 (higher is better).
type LapScore struct {
	Lap             int     `json:"lap"`
	DurationMS      uint64  `json:"duration_ms"`
	SampleCount     int     `json:"sample_count"`
	LapTime         float32 `json:"lap_time"` // seconds, from Forza CurrentLap; 0 if unknown
	WheelspinPct    float32 `json:"wheelspin_pct"`
	LockupPct       float32 `json:"lockup_pct"`
	CoastingPct     float32 `json:"coasting_pct"`
	ThrottleScore   float32 `json:"throttle_score"`
	LockupEvents    int     `json:"lockup_events"`
	WheelspinEvents int     `json:"wheelspin_events"`
	OverallScore    float32 `json:"overall_score"`
}

// Report is the full analysis result returned to the frontend.
type Report struct {
	Recording string     `json:"recording"`
	Laps      []LapScore `json:"laps"`
	Overall   LapScore   `json:"overall"`
	Events    []Event    `json:"events"`
	Notes     []string   `json:"notes"`
}

// lapSegment is a contiguous run of race-on frames sharing one LapNumber.
type lapSegment struct {
	lap    int
	frames []Frame
}

// Analyze runs every detector over the recording and assembles the report. It
// skips frames where the race is not on (menus/pause) and never panics on empty
// or degenerate input.
func Analyze(recording string, frames []Frame) Report {
	report := Report{Recording: recording, Laps: []LapScore{}, Events: []Event{}, Notes: []string{}}

	active := make([]Frame, 0, len(frames))
	for _, f := range frames {
		if f.Telemetry.IsRaceOn != 0 {
			active = append(active, f)
		}
	}
	if len(active) == 0 {
		report.Notes = append(report.Notes, "No active race frames found in this recording.")
		return report
	}

	segments := segmentLaps(active)
	for _, seg := range segments {
		dt := seg.frames[0].Telemetry.DrivetrainType
		events := runDetectors(seg, dt)
		report.Events = append(report.Events, events...)
		report.Laps = append(report.Laps, scoreLap(seg, events))
	}

	report.Overall = scoreOverall(active, report.Events)
	sort.SliceStable(report.Events, func(i, j int) bool {
		return report.Events[i].OffsetMS < report.Events[j].OffsetMS
	})
	return report
}

// segmentLaps splits frames into contiguous segments whenever LapNumber changes.
func segmentLaps(frames []Frame) []lapSegment {
	segments := make([]lapSegment, 0, 4)
	start := 0
	cur := frames[0].Telemetry.LapNumber
	for i := 1; i < len(frames); i++ {
		if frames[i].Telemetry.LapNumber != cur {
			segments = append(segments, lapSegment{lap: int(cur), frames: frames[start:i]})
			start = i
			cur = frames[i].Telemetry.LapNumber
		}
	}
	segments = append(segments, lapSegment{lap: int(cur), frames: frames[start:]})
	return segments
}

// span is the raw output of the coalescing pass before it becomes an Event.
type span struct {
	startIdx int
	endIdx   int // inclusive
	peak     float32
}

// matchFunc reports whether a frame is offending and, if so, the magnitude of
// the offence (used to track the peak across a coalesced span).
type matchFunc func(t forza.Telemetry) (bool, float32)

// coalesce walks a lap's frames and groups consecutive offending frames into
// spans. A run survives a gap of up to coalesceGapMS (a dropped frame or two),
// and a span is only emitted if it lasts at least minDurationMS — this is what
// stops a single noisy frame from becoming an event. peak is the largest
// magnitude seen in the span.
func coalesce(frames []Frame, minDurationMS uint64, match matchFunc) []span {
	spans := make([]span, 0)
	open := false
	var cur span
	var lastHitIdx int

	flush := func() {
		if !open {
			return
		}
		open = false
		dur := frames[cur.endIdx].OffsetMS - frames[cur.startIdx].OffsetMS
		if dur >= minDurationMS {
			spans = append(spans, cur)
		}
	}

	for i, f := range frames {
		hit, mag := match(f.Telemetry)
		if !hit {
			if open && frames[i].OffsetMS-frames[lastHitIdx].OffsetMS > coalesceGapMS {
				flush()
			}
			continue
		}
		if !open {
			open = true
			cur = span{startIdx: i, endIdx: i, peak: mag}
		} else if frames[i].OffsetMS-frames[lastHitIdx].OffsetMS > coalesceGapMS {
			flush()
			open = true
			cur = span{startIdx: i, endIdx: i, peak: mag}
		} else {
			cur.endIdx = i
			if absf(mag) > absf(cur.peak) {
				cur.peak = mag
			}
		}
		lastHitIdx = i
	}
	flush()
	return spans
}

// eventFromSpan builds an Event from a coalesced span on the given lap.
func eventFromSpan(frames []Frame, s span, lap int, kind EventKind, sev Severity, msg, suggestion string) Event {
	start := frames[s.startIdx]
	dur := frames[s.endIdx].OffsetMS - frames[s.startIdx].OffsetMS
	return Event{
		Kind:       kind,
		Severity:   sev,
		Lap:        lap,
		OffsetMS:   start.OffsetMS,
		DurationMS: dur,
		Position:   Position{X: start.Telemetry.PositionX, Y: start.Telemetry.PositionY, Z: start.Telemetry.PositionZ},
		Distance:   start.Telemetry.DistanceTraveled,
		Speed:      start.Telemetry.Speed * 3.6,
		Metric:     s.peak,
		Message:    msg,
		Suggestion: suggestion,
	}
}

// scoreLap computes the aggregate metrics for one lap segment.
func scoreLap(seg lapSegment, events []Event) LapScore {
	score := aggregate(seg.frames)
	score.Lap = seg.lap
	for _, e := range events {
		switch e.Kind {
		case KindBrakeLockup:
			score.LockupEvents++
		case KindCornerWheelspin:
			score.WheelspinEvents++
		}
	}
	// Forza reports the running lap time in CurrentLap; the last frame of the lap
	// holds the most complete value.
	score.LapTime = seg.frames[len(seg.frames)-1].Telemetry.CurrentLap
	return score
}

// scoreOverall aggregates across the whole session.
func scoreOverall(frames []Frame, events []Event) LapScore {
	score := aggregate(frames)
	score.Lap = -1
	score.LapTime = 0
	for _, e := range events {
		switch e.Kind {
		case KindBrakeLockup:
			score.LockupEvents++
		case KindCornerWheelspin:
			score.WheelspinEvents++
		}
	}
	return score
}

// aggregate computes the percentage/score metrics shared by lap and overall rows.
func aggregate(frames []Frame) LapScore {
	score := LapScore{SampleCount: len(frames)}
	if len(frames) == 0 {
		return score
	}
	score.DurationMS = frames[len(frames)-1].OffsetMS - frames[0].OffsetMS

	var moving, wheelspin, coasting int
	var braking, lockup int
	var throttleSum float64
	var throttleSamples int

	for _, f := range frames {
		t := f.Telemetry
		isMoving := t.Speed > movingSpeed
		if isMoving {
			moving++
			if anyDriveSlip(t, wheelspinSlip) {
				wheelspin++
			}
			if isCoasting(t) {
				coasting++
			}
			// Throttle-application score samples acceleration phases only.
			if t.AccelerationZ > 0 {
				throttleSum += float64(t.Accel) / 255
				throttleSamples++
			}
		}
		if t.Brake > brakeOnThreshold {
			braking++
			if anyWheelLockup(t, lockupSlip) {
				lockup++
			}
		}
	}

	score.WheelspinPct = pct(wheelspin, moving)
	score.CoastingPct = pct(coasting, moving)
	score.LockupPct = pct(lockup, braking)
	if throttleSamples > 0 {
		score.ThrottleScore = float32(throttleSum/float64(throttleSamples)) * 100
	}
	score.OverallScore = compositeScore(score)
	return score
}

// compositeScore folds the penalty metrics into a single 0..100 figure.
func compositeScore(s LapScore) float32 {
	penalty := s.LockupPct*lockupWeight +
		s.WheelspinPct*wheelspinWeight +
		s.CoastingPct*coastingWeight +
		(100-s.ThrottleScore)*throttleWeight
	return clamp01x100(100 - penalty)
}

func pct(n, total int) float32 {
	if total == 0 {
		return 0
	}
	return float32(n) / float32(total) * 100
}

func clamp01x100(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func absf(v float32) float32 {
	return float32(math.Abs(float64(v)))
}
