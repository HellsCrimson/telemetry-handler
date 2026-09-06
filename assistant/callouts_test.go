package assistant

import (
	"strings"
	"testing"
	"time"

	"telemetry-handler/engineer"
)

// racingState is the sample race with the player not in danger of anything, so
// each test can introduce exactly one condition.
func racingState() engineer.SessionState {
	st := sampleState()
	// Push the neighbours out of "on you" range and top the fuel up.
	st.Cars[2].GapToLeader = 10.0 // third
	st.Cars[4].GapToLeader = 34.0 // fifth
	p := &st.Cars[3]
	p.Fuel = 200
	for i := range p.Tires {
		p.Tires[i].Wear = 1.0
	}
	return st
}

func TestCalloutsStayQuietWhenNothingHappens(t *testing.T) {
	c := NewCallouts()
	now := time.Now()
	c.Observe(racingState(), now) // prime the edge detectors
	if msgs := c.Observe(racingState(), now.Add(time.Minute)); len(msgs) != 0 {
		t.Errorf("expected silence, got %q", msgs)
	}
}

// The safety car must be announced on deployment and then not repeated every
// tick — an engineer says it once.
func TestCalloutsSafetyCarIsEdgeTriggered(t *testing.T) {
	c := NewCallouts()
	now := time.Now()
	c.Observe(racingState(), now)

	sc := racingState()
	sc.Flags.SCActive = true
	msgs := c.Observe(sc, now.Add(time.Minute))
	if len(msgs) != 1 || !strings.Contains(strings.ToLower(msgs[0]), "safety car") {
		t.Fatalf("expected a safety car call, got %q", msgs)
	}
	if again := c.Observe(sc, now.Add(2*time.Minute)); len(again) != 0 {
		t.Errorf("the same state should not be announced twice, got %q", again)
	}
}

// The tank never covers a whole endurance race, so a full tank at the start must
// not be reported as a problem — the bug that had it announcing trouble on the
// opening laps.
func TestCalloutsDoNotWarnOnAFullTankAtRaceStart(t *testing.T) {
	c := NewCallouts()
	st := racingState()
	st.MaxLaps = 44
	st.Cars[3].TotalLaps = 0
	st.Cars[3].Fuel = 44 // 11 laps of range in a 44-lap race: normal, not a warning
	if msgs := c.Observe(st, time.Now()); len(msgs) != 0 {
		t.Errorf("a healthy tank must not raise an alarm, got %q", msgs)
	}
}

func TestCalloutsWarnOnLowFuel(t *testing.T) {
	c := NewCallouts()
	st := racingState()
	st.Cars[3].Fuel = 8 // ~2 laps at 4 L/lap
	msgs := c.Observe(st, time.Now())
	if len(msgs) != 1 || !strings.Contains(strings.ToLower(msgs[0]), "fuel") {
		t.Fatalf("expected a fuel warning, got %q", msgs)
	}
}

func TestCalloutsWarnOnWornTyres(t *testing.T) {
	c := NewCallouts()
	st := racingState()
	for i := range st.Cars[3].Tires {
		st.Cars[3].Tires[i].Wear = 0.2 // 80% worn
	}
	msgs := c.Observe(st, time.Now())
	if len(msgs) != 1 || !strings.Contains(strings.ToLower(msgs[0]), "tyres") {
		t.Fatalf("expected a tyre warning, got %q", msgs)
	}
}

// Repeating the same warning every few seconds is what makes an assistant
// unbearable, so a rule has to cool down.
func TestCalloutsRateLimitTheSameRule(t *testing.T) {
	c := NewCallouts()
	st := racingState()
	st.Cars[3].Fuel = 8
	now := time.Now()
	if msgs := c.Observe(st, now); len(msgs) != 1 {
		t.Fatalf("expected the first warning, got %q", msgs)
	}
	if msgs := c.Observe(st, now.Add(30*time.Second)); len(msgs) != 0 {
		t.Errorf("the rule should still be cooling down, got %q", msgs)
	}
	if msgs := c.Observe(st, now.Add(2*time.Minute)); len(msgs) != 1 {
		t.Errorf("the rule should fire again after its cooldown, got %q", msgs)
	}
}

// Two conditions at once must not produce a monologue.
func TestCalloutsSpaceOutDifferentRules(t *testing.T) {
	c := NewCallouts()
	st := racingState()
	st.Cars[3].Fuel = 8
	for i := range st.Cars[3].Tires {
		st.Cars[3].Tires[i].Wear = 0.2
	}
	if msgs := c.Observe(st, time.Now()); len(msgs) != 1 {
		t.Errorf("expected only one call at a time, got %q", msgs)
	}
}

// Nothing is urgent while the car is stationary in the pit box.
func TestCalloutsStayQuietInThePits(t *testing.T) {
	c := NewCallouts()
	st := racingState()
	st.Cars[3].Fuel = 8
	st.Cars[3].InPits = true
	if msgs := c.Observe(st, time.Now()); len(msgs) != 0 {
		t.Errorf("expected silence in the pits, got %q", msgs)
	}
}

func TestCalloutsAnnounceARivalPitting(t *testing.T) {
	c := NewCallouts()
	now := time.Now()
	c.Observe(racingState(), now)

	st := racingState()
	st.Cars[2].NumPitstops++ // the car ahead has stopped
	msgs := c.Observe(st, now.Add(time.Minute))
	if len(msgs) != 1 || !strings.Contains(msgs[0], "pitted") {
		t.Fatalf("expected a rival-pitted call, got %q", msgs)
	}
}

func TestCalloutsNeedALiveSession(t *testing.T) {
	if msgs := NewCallouts().Observe(engineer.SessionState{}, time.Now()); msgs != nil {
		t.Errorf("expected nothing without a session, got %q", msgs)
	}
}

// Crossing the line should get the debrief a real engineer gives: the lap, how
// it compares, and where the driver stands.
func TestCalloutsSummariseEachLap(t *testing.T) {
	c := NewCallouts()
	now := time.Now()
	c.Observe(racingState(), now)

	next := racingState()
	next.Cars[3].TotalLaps++
	next.Cars[3].LastLap = 95.5
	next.Cars[3].BestLap = 95.2
	msgs := c.Observe(next, now.Add(2*time.Minute))
	if len(msgs) != 1 {
		t.Fatalf("expected a lap summary, got %q", msgs)
	}
	for _, want := range []string{"1:35.5", "3 tenths off your best", "P4", "fuel for"} {
		if !strings.Contains(msgs[0], want) {
			t.Errorf("lap summary missing %q: %q", want, msgs[0])
		}
	}
}

func TestCalloutsCallOutAPersonalBest(t *testing.T) {
	c := NewCallouts()
	now := time.Now()
	c.Observe(racingState(), now)

	next := racingState()
	next.Cars[3].TotalLaps++
	next.Cars[3].LastLap = 94.0
	next.Cars[3].BestLap = 94.0
	msgs := c.Observe(next, now.Add(2*time.Minute))
	if len(msgs) != 1 || !strings.Contains(msgs[0], "best yet") {
		t.Fatalf("expected a personal-best call, got %q", msgs)
	}
}

// The lap rule must not be suppressed by the general cooldown, or it would only
// report every other lap.
func TestCalloutsSummariseConsecutiveLaps(t *testing.T) {
	c := NewCallouts()
	now := time.Now()
	st := racingState()
	c.Observe(st, now)

	for lap := 1; lap <= 2; lap++ {
		st.Cars[3].TotalLaps++
		st.Cars[3].LastLap = 95.5
		at := now.Add(time.Duration(lap) * 95 * time.Second)
		if msgs := c.Observe(st, at); len(msgs) != 1 {
			t.Errorf("lap %d produced %q, want one summary", lap, msgs)
		}
	}
}

func TestDeltaSpoken(t *testing.T) {
	cases := map[float64]string{0.04: "a tenth", 0.3: "3 tenths", 0.85: "9 tenths", 1.4: "1.4 seconds"}
	for d, want := range cases {
		if got := deltaSpoken(d); got != want {
			t.Errorf("deltaSpoken(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestLapSpoken(t *testing.T) {
	if got := lapSpoken(95.48); got != "1:35.5" {
		t.Errorf("lapSpoken(95.48) = %q", got)
	}
	if got := lapSpoken(125.0); got != "2:05.0" {
		t.Errorf("lapSpoken(125) = %q", got)
	}
}
