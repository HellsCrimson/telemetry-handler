package engineer

import "fmt"

// Corner names are DERIVED, not hand-authored: the strategist gets them for free.
// We have no corner IDs from LMU, but the reference lap's speed profile tells us
// where the corners are — a mini-sector noticeably slower than the lap's top speed
// is "in a corner". Consecutive slow mini-sectors are one corner, numbered T1, T2…
// in lap order; faster mini-sectors are straights (blank label). The result is
// persisted per track so the names stay stable across sessions.

// cornerSpeedRatio: a mini-sector whose minimum speed is below this fraction of
// the lap's top speed counts as part of a corner. 0.85 catches real corners while
// ignoring small lifts on a straight.
const cornerSpeedRatio = 0.85

// deriveCorners labels each mini-sector of a reference lap as a corner ("T1", "T2"
// …) or a straight (""). Returns a slice the same length as sectors.
func deriveCorners(sectors []MiniSectorState) []string {
	labels := make([]string, len(sectors))
	if len(sectors) == 0 {
		return labels
	}

	// Top speed = the fastest entry/exit reading anywhere on the lap (a proxy for
	// the straight-line speed).
	maxSpeed := 0.0
	for i := range sectors {
		if sectors[i].EntrySpeed > maxSpeed {
			maxSpeed = sectors[i].EntrySpeed
		}
		if sectors[i].ExitSpeed > maxSpeed {
			maxSpeed = sectors[i].ExitSpeed
		}
	}
	if maxSpeed <= 0 {
		return labels // no speed data yet
	}
	threshold := maxSpeed * cornerSpeedRatio

	corner := 0
	inCorner := false
	for i := range sectors {
		spd := sectors[i].MinSpeed
		if spd <= 0 {
			spd = sectors[i].EntrySpeed
		}
		if spd > 0 && spd < threshold {
			if !inCorner {
				corner++
				inCorner = true
			}
			labels[i] = fmt.Sprintf("T%d", corner)
		} else {
			inCorner = false
			labels[i] = ""
		}
	}
	return labels
}
