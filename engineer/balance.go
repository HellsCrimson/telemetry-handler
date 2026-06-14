package engineer

import "math"

// balance.go is the advisory chassis-balance heuristic. It watches the player car
// through corners and decides whether the car tends to understeer (front sliding
// more than the rear) or oversteer (rear sliding more). It can only ever be a
// hint: LMU exposes grip/slip but NOT the setup, so the proposal suggests a
// direction (brake bias, TC, throttle discipline) and never reads or changes
// actual values. ARB advice is impossible — ARB stiffness isn't in the telemetry.

// Tuning. These gate "is the car actually cornering" and how strongly to weight a
// new sample into the rolling average.
const (
	balanceMinSpeed   = 10.0 // m/s — ignore slow/stationary frames
	balanceMinSteer   = 0.10 // |steering| (−1..1) above which we treat it as cornering
	balanceEMA        = 0.02 // exponential weight for each new cornering sample
	balanceNeutralPad = 0.03 // |bias| within this is "Neutral"
)

// balanceTracker keeps a rolling front-vs-rear slip bias for the player car.
type balanceTracker struct {
	bias    float64 // running EMA: + = understeer, - = oversteer
	samples int
}

// reset clears the tracker (new session / source change).
func (b *balanceTracker) reset() { *b = balanceTracker{} }

// update folds one cornering frame into the bias. frontSlip/rearSlip are the
// average GripFract (fraction of the contact patch sliding) of the two front and
// two rear wheels; steering and speed gate whether we're cornering at all.
func (b *balanceTracker) update(frontSlip, rearSlip, steering, speed float64) {
	if speed < balanceMinSpeed || math.Abs(steering) < balanceMinSteer {
		return
	}
	sample := frontSlip - rearSlip // + = front sliding more = understeer
	b.bias += (sample - b.bias) * balanceEMA
	b.samples++
}

// state renders the current assessment + advisory proposal.
func (b *balanceTracker) state() BalanceState {
	s := BalanceState{Samples: b.samples, Bias: b.bias}
	if b.samples < 200 { // need a meaningful sample before judging
		s.Verdict = ""
		s.Proposal = "Gathering data through corners…"
		return s
	}
	switch {
	case b.bias > balanceNeutralPad:
		s.Verdict = "Understeer"
		s.Proposal = "Front washing out mid-corner. Try a little rear brake bias, ease TC, or be patient on entry."
	case b.bias < -balanceNeutralPad:
		s.Verdict = "Oversteer"
		s.Proposal = "Rear stepping out. Try a touch more front brake bias, raise TC a step, or smooth the throttle."
	default:
		s.Verdict = "Neutral"
		s.Proposal = "Balance looks settled through corners."
	}
	return s
}
