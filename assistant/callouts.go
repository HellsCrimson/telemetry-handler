package assistant

import (
	"fmt"
	"time"

	"telemetry-handler/engineer"
)

// Callout thresholds. These are the difference between an engineer and a
// nuisance, so they are deliberately conservative — a real one stays quiet.
const (
	// calloutMinGap is the global spacing between any two callouts.
	calloutMinGap = 25 * time.Second
	// calloutCooldown is how long the same rule waits before firing again.
	calloutCooldown = 90 * time.Second
	// calloutLapCooldown is shorter, because a lap summary is due every lap and
	// most laps are close to the general cooldown.
	calloutLapCooldown = 30 * time.Second
	// closeGap is the interval (seconds) at which a rival counts as "on you".
	closeGap = 1.2
	// fuelWarnLaps is the remaining-laps margin that triggers a fuel warning.
	fuelWarnLaps = 3.0
	// tyreWarnWear is the worn fraction at which tyres get a mention.
	tyreWarnWear = 0.70
	// rainOnset is the rain level that counts as "it's starting".
	rainOnset = 0.15
)

// Callouts turns changes in the session into unprompted radio messages.
//
// It is rule-based rather than model-driven on purpose: a callout has to be
// instant, free, and never wrong, and none of those survive putting a language
// model in the loop for something the driver did not ask for.
type Callouts struct {
	lastAny  time.Time
	lastRule map[string]time.Time

	// Previous values, for edge detection — a callout fires on a change, not on
	// a state, or it would repeat every tick.
	started    bool
	prevSC     bool
	prevYellow bool
	prevRain   float64
	prevLaps   int
	prevStops  map[int32]int
}

func NewCallouts() *Callouts {
	return &Callouts{lastRule: map[string]time.Time{}, prevStops: map[int32]int{}}
}

// Observe returns the messages to say now, if any. It is called on a timer with
// the latest snapshot; now is passed in so the rate limiting is testable.
func (c *Callouts) Observe(st engineer.SessionState, now time.Time) []string {
	if !st.Available {
		return nil
	}
	player := playerCar(st)
	if player == nil {
		return nil
	}

	// Collect candidates, then let the rate limiter pick, so a burst of changes
	// does not produce a monologue.
	type candidate struct{ rule, msg string }
	var cands []candidate
	add := func(rule, msg string) { cands = append(cands, candidate{rule, msg}) }

	if c.started {
		// The lap summary goes first so that when a lap crossing coincides with
		// something else, the summary is what gets the slot.
		c.detectLap(st, *player, add)
		c.detectFlags(st, add)
		c.detectRivalStops(st, *player, add)
		c.detectRain(st, add)
	}
	c.detectFuel(st, *player, add)
	c.detectTyres(*player, add)
	c.detectTraffic(st, *player, add)

	c.remember(st, *player)

	// The driver is busy in the pits or under a stop; nothing here is urgent.
	if player.InPits {
		return nil
	}

	var out []string
	for _, cand := range cands {
		if now.Sub(c.lastAny) < calloutMinGap {
			break
		}
		if last, ok := c.lastRule[cand.rule]; ok && now.Sub(last) < cooldownFor(cand.rule) {
			continue
		}
		out = append(out, cand.msg)
		c.lastRule[cand.rule] = now
		c.lastAny = now
	}
	return out
}

// detectLap gives the driver the debrief a real engineer offers as they cross
// the line: the lap they just did, how it compares, and where they stand.
func (c *Callouts) detectLap(st engineer.SessionState, player engineer.CarState, add func(string, string)) {
	if player.TotalLaps <= c.prevLaps || player.LastLap <= 0 {
		return
	}
	msg := "That's a " + lapSpoken(player.LastLap)
	if player.BestLap > 0 {
		switch delta := player.LastLap - player.BestLap; {
		case delta <= 0.05:
			msg += ", your best yet"
		default:
			msg += ", " + deltaSpoken(delta) + " off your best"
		}
	}
	msg += fmt.Sprintf(". P%d", player.Place)
	if laps, _, ok := fuelRange(st, player); ok {
		msg += fmt.Sprintf(", fuel for %.0f more laps", laps)
	}
	add("lap", msg+".")
}

func cooldownFor(rule string) time.Duration {
	if rule == "lap" {
		return calloutLapCooldown
	}
	return calloutCooldown
}

func (c *Callouts) detectFlags(st engineer.SessionState, add func(string, string)) {
	switch {
	case st.Flags.SCActive && !c.prevSC:
		add("sc", "Safety car, safety car. Slow down.")
	case !st.Flags.SCActive && c.prevSC:
		add("sc", "Safety car in this lap, get ready to go.")
	case st.Flags.Yellow && !c.prevYellow:
		add("yellow", "Yellow flag, be careful.")
	}
}

func (c *Callouts) detectRivalStops(st engineer.SessionState, player engineer.CarState, add func(string, string)) {
	ahead, behind := neighbours(st, player)
	for _, car := range append(ahead, behind...) {
		if prev, ok := c.prevStops[car.ID]; ok && car.NumPitstops > prev {
			add(fmt.Sprintf("pit-%d", car.ID),
				fmt.Sprintf("%s has pitted from P%d.", orUnknown(car.Driver), car.Place))
		}
	}
}

func (c *Callouts) detectRain(st engineer.SessionState, add func(string, string)) {
	if st.Weather.Raining >= rainOnset && c.prevRain < rainOnset {
		add("rain", "Rain is starting. Expect less grip.")
	}
	if st.Weather.Raining < rainOnset && c.prevRain >= rainOnset {
		add("rain", "Rain is easing off.")
	}
}

// detectFuel warns when the tank itself is nearly empty.
//
// It deliberately does NOT compare the tank against the rest of the race. In
// endurance racing the tank never covers the full distance — that is what pit
// stops are for — so such a comparison is true from the first lap of every race
// and fires an alarm about nothing. "Will I make it to the end" is a strategy
// question to ask the engineer, not something to shout unprompted.
func (c *Callouts) detectFuel(st engineer.SessionState, player engineer.CarState, add func(string, string)) {
	laps, _, ok := fuelRange(st, player)
	if !ok {
		return
	}
	if laps <= fuelWarnLaps {
		add("fuel", fmt.Sprintf("About %.0f laps of fuel left.", laps))
	}
}

func (c *Callouts) detectTyres(player engineer.CarState, add func(string, string)) {
	worst := 0.0
	for _, t := range player.Tires {
		if worn := 1 - t.Wear; worn > worst {
			worst = worn
		}
	}
	if worst >= tyreWarnWear {
		add("tyres", fmt.Sprintf("Tyres are at %.0f percent wear. Think about a stop.", worst*100))
	}
}

func (c *Callouts) detectTraffic(st engineer.SessionState, player engineer.CarState, add func(string, string)) {
	ahead, behind := neighbours(st, player)
	if len(behind) > 0 {
		if gap := gapBetween(player, behind[0]); gap > 0 && gap <= closeGap {
			add("behind", fmt.Sprintf("%s is within %.1f behind you.", orUnknown(behind[0].Driver), gap))
		}
	}
	if len(ahead) > 0 {
		if gap := gapBetween(ahead[0], player); gap > 0 && gap <= closeGap {
			add("ahead", fmt.Sprintf("You're %.1f off %s ahead.", gap, orUnknown(ahead[0].Driver)))
		}
	}
}

func (c *Callouts) remember(st engineer.SessionState, player engineer.CarState) {
	c.prevSC = st.Flags.SCActive
	c.prevYellow = st.Flags.Yellow
	c.prevRain = st.Weather.Raining
	c.prevLaps = player.TotalLaps
	for _, car := range st.Cars {
		c.prevStops[car.ID] = car.NumPitstops
	}
	c.started = true
}

// lapSpoken renders a lap time the way it is said on the radio ("1:35.2").
// Kokoro reads that form correctly; three decimals would only be noise aloud.
func lapSpoken(sec float64) string {
	m := int(sec) / 60
	return fmt.Sprintf("%d:%04.1f", m, sec-float64(m*60))
}

// deltaSpoken renders a time difference as an engineer says it: tenths under a
// second, otherwise seconds.
func deltaSpoken(d float64) string {
	if d < 0.95 {
		switch tenths := int(d*10 + 0.5); tenths {
		case 0, 1:
			return "a tenth"
		default:
			return fmt.Sprintf("%d tenths", tenths)
		}
	}
	return fmt.Sprintf("%.1f seconds", d)
}
