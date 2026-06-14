package analysis

import (
	"testing"

	"telemetry-handler/game/forza"
)

const frameStepMS = 16 // ~60 Hz, matches FH6

// baseTelemetry is a sane "driving normally" frame: race on, moving in 3rd gear,
// mid-range RPM, RWD, no slip, full-ish throttle, no brake, going straight.
func baseTelemetry() forza.Telemetry {
	return forza.Telemetry{
		IsRaceOn:         1,
		Speed:            40,
		Gear:             3,
		LapNumber:        1,
		DrivetrainType:   1, // RWD
		EngineMaxRpm:     7000,
		CurrentEngineRpm: 4500,
		Accel:            220,
		Brake:            0,
		Steer:            0,
		AccelerationZ:    0,
	}
}

// seq builds a sequence of n frames, applying mod to each telemetry frame. The
// offset advances by frameStepMS per frame; position advances so events carry a
// distinct location.
func seq(n int, mod func(i int, t *forza.Telemetry)) []Frame {
	frames := make([]Frame, n)
	for i := range n {
		t := baseTelemetry()
		t.PositionX = float32(i)
		t.DistanceTraveled = float32(i) * 10
		if mod != nil {
			mod(i, &t)
		}
		frames[i] = Frame{OffsetMS: uint64(i * frameStepMS), Telemetry: t}
	}
	return frames
}

func countKind(events []Event, kind EventKind) int {
	n := 0
	for _, e := range events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func firstOfKind(t *testing.T, events []Event, kind EventKind) Event {
	t.Helper()
	for _, e := range events {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("no event of kind %s found in %d events", kind, len(events))
	return Event{}
}

func TestBrakeLockupDetected(t *testing.T) {
	// 10 frames hard on the brakes with the front-left wheel locked.
	frames := seq(10, func(i int, t *forza.Telemetry) {
		t.Brake = 220
		t.Accel = 0
		t.TireSlipRatioFrontLeft = -0.6
	})
	r := Analyze("rec", frames)
	if got := countKind(r.Events, KindBrakeLockup); got != 1 {
		t.Fatalf("want 1 lockup event, got %d", got)
	}
	e := firstOfKind(t, r.Events, KindBrakeLockup)
	if e.Lap != 1 {
		t.Errorf("lap: want 1, got %d", e.Lap)
	}
	if e.Severity != SeverityMajor {
		t.Errorf("severity: want major (peak %.2f), got %s", e.Metric, e.Severity)
	}
	if e.Metric > -0.55 {
		t.Errorf("peak metric: want ~-0.6, got %.2f", e.Metric)
	}
	if r.Laps[0].LockupEvents != 1 {
		t.Errorf("lap LockupEvents: want 1, got %d", r.Laps[0].LockupEvents)
	}
}

func TestCornerWheelspinUsesDrivetrain(t *testing.T) {
	// RWD car spinning the REAR wheels mid-corner -> should fire.
	rwdRear := seq(10, func(i int, t *forza.Telemetry) {
		t.DrivetrainType = 1
		t.Accel = 230
		t.Steer = 40
		t.TireSlipRatioRearLeft = 0.6
	})
	if got := countKind(Analyze("r", rwdRear).Events, KindCornerWheelspin); got != 1 {
		t.Fatalf("RWD rear spin: want 1 wheelspin event, got %d", got)
	}

	// RWD car with the same slip on the FRONT (non-driven) wheels -> must not fire.
	rwdFront := seq(10, func(i int, t *forza.Telemetry) {
		t.DrivetrainType = 1
		t.Accel = 230
		t.Steer = 40
		t.TireSlipRatioFrontLeft = 0.6
	})
	if got := countKind(Analyze("r", rwdFront).Events, KindCornerWheelspin); got != 0 {
		t.Fatalf("RWD front slip: want 0 wheelspin events, got %d", got)
	}

	// FWD car spinning the FRONT (driven) wheels -> should fire.
	fwdFront := seq(10, func(i int, t *forza.Telemetry) {
		t.DrivetrainType = 0
		t.Accel = 230
		t.Steer = 40
		t.TireSlipRatioFrontLeft = 0.6
	})
	if got := countKind(Analyze("r", fwdFront).Events, KindCornerWheelspin); got != 1 {
		t.Fatalf("FWD front spin: want 1 wheelspin event, got %d", got)
	}
}

func TestUnderDriving(t *testing.T) {
	// Accelerating out of a corner with grip to spare but only partial throttle.
	under := seq(30, func(i int, t *forza.Telemetry) {
		t.Accel = 120
		t.AccelerationZ = 3
		t.Steer = 0
		t.TireCombinedSlipRearLeft = 0.4
		t.TireCombinedSlipRearRight = 0.4
	})
	if got := countKind(Analyze("r", under).Events, KindUnderDriving); got != 1 {
		t.Fatalf("under-driving: want 1 event, got %d", got)
	}

	// Same situation but already at full throttle -> nothing to coach.
	full := seq(30, func(i int, t *forza.Telemetry) {
		t.Accel = 240
		t.AccelerationZ = 3
		t.Steer = 0
		t.TireCombinedSlipRearLeft = 0.4
		t.TireCombinedSlipRearRight = 0.4
	})
	if got := countKind(Analyze("r", full).Events, KindUnderDriving); got != 0 {
		t.Fatalf("full throttle: want 0 under-driving events, got %d", got)
	}
}

func TestCoastingMinDuration(t *testing.T) {
	// A long coast (no pedals) crosses the min-duration threshold -> one event.
	long := seq(40, func(i int, t *forza.Telemetry) {
		t.Accel = 0
		t.Brake = 0
	})
	if got := countKind(Analyze("r", long).Events, KindCoasting); got != 1 {
		t.Fatalf("long coast: want 1 event, got %d", got)
	}

	// A 2-frame coast (~16ms) is below minCoastMS -> no event.
	short := seq(20, func(i int, t *forza.Telemetry) {
		if i == 5 || i == 6 {
			t.Accel = 0
			t.Brake = 0
		}
	})
	if got := countKind(Analyze("r", short).Events, KindCoasting); got != 0 {
		t.Fatalf("short coast: want 0 events, got %d", got)
	}
}

func TestCoastingIgnoresCornering(t *testing.T) {
	// Coasting while still steering hard (mid-corner) is legitimate -> no event.
	cornering := seq(40, func(i int, t *forza.Telemetry) {
		t.Accel = 0
		t.Brake = 0
		t.Steer = 60
	})
	if got := countKind(Analyze("r", cornering).Events, KindCoasting); got != 0 {
		t.Fatalf("cornering coast: want 0 events, got %d", got)
	}

	// The same coast while going straight is wasted time -> one event.
	straight := seq(40, func(i int, t *forza.Telemetry) {
		t.Accel = 0
		t.Brake = 0
		t.Steer = 0
	})
	if got := countKind(Analyze("r", straight).Events, KindCoasting); got != 1 {
		t.Fatalf("straight coast: want 1 event, got %d", got)
	}
}

func TestPedalOverlap(t *testing.T) {
	overlap := seq(20, func(i int, t *forza.Telemetry) {
		t.Accel = 200
		t.Brake = 200
	})
	e := firstOfKind(t, Analyze("r", overlap).Events, KindPedalOverlap)
	if e.Severity != SeverityMajor {
		t.Errorf("heavy overlap: want major, got %s", e.Severity)
	}

	// Throttle only -> no overlap.
	throttleOnly := seq(20, func(i int, t *forza.Telemetry) {
		t.Accel = 200
		t.Brake = 0
	})
	if got := countKind(Analyze("r", throttleOnly).Events, KindPedalOverlap); got != 0 {
		t.Fatalf("throttle only: want 0 overlap events, got %d", got)
	}
}

func TestShortShiftAndOverRev(t *testing.T) {
	// Up-shift at 60% RPM under power -> short shift.
	short := seq(10, func(i int, t *forza.Telemetry) {
		t.Accel = 230
		if i < 5 {
			t.Gear = 3
			t.CurrentEngineRpm = 4200 // 60% of 7000
		} else {
			t.Gear = 4
			t.CurrentEngineRpm = 3000
		}
	})
	if got := countKind(Analyze("r", short).Events, KindShortShift); got != 1 {
		t.Fatalf("short shift: want 1 event, got %d", got)
	}

	// Held at the limiter under power -> over-rev, no short shift.
	overRev := seq(20, func(i int, t *forza.Telemetry) {
		t.Accel = 230
		t.CurrentEngineRpm = 6990 // ~99.9%
	})
	rr := Analyze("r", overRev)
	if got := countKind(rr.Events, KindOverRev); got != 1 {
		t.Fatalf("over-rev: want 1 event, got %d", got)
	}
	if got := countKind(rr.Events, KindShortShift); got != 0 {
		t.Fatalf("over-rev: want 0 short-shift events, got %d", got)
	}

	// A normal shift at 95% RPM -> neither.
	normal := seq(10, func(i int, t *forza.Telemetry) {
		t.Accel = 230
		if i < 5 {
			t.Gear = 3
			t.CurrentEngineRpm = 6650 // 95%
		} else {
			t.Gear = 4
			t.CurrentEngineRpm = 4500
		}
	})
	nr := Analyze("r", normal)
	if got := countKind(nr.Events, KindShortShift); got != 0 {
		t.Errorf("normal shift: want 0 short-shift events, got %d", got)
	}
}

func TestLapSegmentation(t *testing.T) {
	// 30 frames across laps 1, 2, 3 (10 each), with a lockup only in lap 2.
	frames := seq(30, func(i int, t *forza.Telemetry) {
		t.LapNumber = uint16(i/10 + 1)
		if i >= 10 && i < 20 {
			t.Brake = 220
			t.Accel = 0
			t.TireSlipRatioFrontLeft = -0.6
		}
	})
	r := Analyze("rec", frames)
	if len(r.Laps) != 3 {
		t.Fatalf("want 3 lap rows, got %d", len(r.Laps))
	}
	if r.Laps[0].Lap != 1 || r.Laps[2].Lap != 3 {
		t.Errorf("lap numbering off: %+v", []int{r.Laps[0].Lap, r.Laps[1].Lap, r.Laps[2].Lap})
	}
	e := firstOfKind(t, r.Events, KindBrakeLockup)
	if e.Lap != 2 {
		t.Errorf("lockup lap: want 2, got %d", e.Lap)
	}
	if r.Overall.Lap != -1 {
		t.Errorf("overall lap: want -1, got %d", r.Overall.Lap)
	}
}

func TestCoalescingGapMergesAndSplits(t *testing.T) {
	// One offending run with a single dropped frame in the middle -> still 1 event.
	merged := seq(12, func(i int, t *forza.Telemetry) {
		t.Brake = 220
		t.Accel = 0
		t.TireSlipRatioFrontLeft = -0.6
		if i == 6 {
			t.TireSlipRatioFrontLeft = 0 // brief non-hit within coalesceGapMS
		}
	})
	if got := countKind(Analyze("r", merged).Events, KindBrakeLockup); got != 1 {
		t.Fatalf("merge: want 1 event, got %d", got)
	}

	// Two runs separated by a long clean stretch -> 2 events.
	split := seq(40, func(i int, t *forza.Telemetry) {
		if (i >= 0 && i < 10) || (i >= 25 && i < 35) {
			t.Brake = 220
			t.Accel = 0
			t.TireSlipRatioFrontLeft = -0.6
		}
	})
	if got := countKind(Analyze("r", split).Events, KindBrakeLockup); got != 2 {
		t.Fatalf("split: want 2 events, got %d", got)
	}
}

func TestIsRaceOffSkipped(t *testing.T) {
	// All frames carry an extreme lockup, but the race is off -> nothing detected.
	frames := seq(20, func(i int, t *forza.Telemetry) {
		t.IsRaceOn = 0
		t.Brake = 255
		t.TireSlipRatioFrontLeft = -1.0
	})
	r := Analyze("r", frames)
	if len(r.Events) != 0 {
		t.Fatalf("want 0 events from race-off frames, got %d", len(r.Events))
	}
	if len(r.Notes) == 0 {
		t.Errorf("want a note explaining no active frames")
	}
}

func TestEmptyAndNoRaceOn(t *testing.T) {
	if r := Analyze("r", nil); len(r.Events) != 0 || len(r.Notes) == 0 {
		t.Errorf("empty input: want no events and a note, got %+v", r)
	}
	off := seq(5, func(i int, t *forza.Telemetry) { t.IsRaceOn = 0 })
	if r := Analyze("r", off); len(r.Notes) == 0 {
		t.Errorf("all race-off: want a note")
	}
}

func TestScorecardPercentages(t *testing.T) {
	// Half the moving frames are coasting, half are normal driving.
	frames := seq(20, func(i int, t *forza.Telemetry) {
		if i%2 == 0 {
			t.Accel = 0
			t.Brake = 0
		}
	})
	r := Analyze("r", frames)
	if r.Overall.CoastingPct < 45 || r.Overall.CoastingPct > 55 {
		t.Errorf("coasting pct: want ~50, got %.1f", r.Overall.CoastingPct)
	}
	if r.Overall.OverallScore < 0 || r.Overall.OverallScore > 100 {
		t.Errorf("overall score out of range: %.1f", r.Overall.OverallScore)
	}
	if r.Overall.SampleCount != 20 {
		t.Errorf("sample count: want 20, got %d", r.Overall.SampleCount)
	}
}
