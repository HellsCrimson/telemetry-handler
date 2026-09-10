package moza

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// echoPort answers every request by echoing the payload it was sent, and
// remembers the last value written per command id — a base that accepts writes.
type echoPort struct {
	fakePort
	values map[uint8]int
}

func newEchoPort(t *testing.T, initial map[uint8]int) *echoPort {
	p := &echoPort{values: map[uint8]int{}}
	for id, v := range initial {
		p.values[id] = v
	}
	p.reply = func(_ int, req []byte) []byte {
		id := req[4]
		switch req[2] {
		case baseWriteGroup:
			p.values[id] = int(req[5])<<8 | int(req[6])
			return replyTo(t, req, req[5:7])
		default:
			v := p.values[id]
			return replyTo(t, req, []byte{uint8(v >> 8), uint8(v)})
		}
	}
	return p
}

func probeClient(p *echoPort) *BaseClient { return newTestClient(&p.fakePort) }

// The happy path: lower it, confirm it took, put it back.
func TestProbeLowerWritesReadsBackAndRestores(t *testing.T) {
	p := newEchoPort(t, map[uint8]int{0x02: 450}) // ffb_strength 45%
	report, err := ProbeLower(probeClient(p), "ffb_strength", 20)
	if err != nil {
		t.Fatalf("ProbeLower: %v", err)
	}
	if report.Original != 45 {
		t.Errorf("original = %d, want 45", report.Original)
	}
	if !report.Verified || report.ReadBack != 20 {
		t.Errorf("read back %d, verified %v", report.ReadBack, report.Verified)
	}
	if !report.Restored || report.RestoreValue != 45 {
		t.Errorf("restore left %d (restored=%v), want 45", report.RestoreValue, report.Restored)
	}
	if p.values[0x02] != 450 {
		t.Errorf("the wheel was left at raw %d, want the original 450", p.values[0x02])
	}
}

// The probe only ever lowers. Raising force feedback, torque or angle on a wheel
// someone is sitting behind needs supervision this harness does not have.
func TestProbeLowerRefusesToRaise(t *testing.T) {
	p := newEchoPort(t, map[uint8]int{0x02: 450}) // 45%
	for _, target := range []int{45, 60} {
		_, err := ProbeLower(probeClient(p), "ffb_strength", target)
		if err == nil {
			t.Errorf("target %d: expected a refusal, the probe must only lower", target)
		}
		if p.values[0x02] != 450 {
			t.Fatalf("target %d: the wheel was written despite the refusal", target)
		}
	}
}

// An unconfirmed conversion means we do not know what number actually reaches
// the hardware, so "write 20" could land as 200.
func TestProbeLowerRefusesUnverifiedConversions(t *testing.T) {
	// natural_inertia_enabled is the registry's one deliberately unverified
	// command: listed by Boxflat, bound to none of its controls.
	cmd, ok := LookupBaseCommand("natural_inertia_enabled")
	if !ok {
		t.Skip("natural_inertia_enabled not in the registry")
	}
	if cmd.Verified {
		t.Skip("natural_inertia_enabled is now verified; pick another unverified command for this test")
	}
	p := newEchoPort(t, map[uint8]int{})
	_, err := ProbeLower(probeClient(p), "natural_inertia_enabled", 0)
	if err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Errorf("expected a refusal citing the unconfirmed conversion, got %v", err)
	}
}

// The restore must happen even when the verification read fails: leaving the
// wheel quieter than it was found is still leaving it changed.
func TestProbeLowerRestoresWhenTheReadBackFails(t *testing.T) {
	p := newEchoPort(t, map[uint8]int{0x02: 450})
	reads := 0
	base := p.reply
	p.reply = func(n int, req []byte) []byte {
		if req[2] == baseReadGroup {
			reads++
			if reads == 2 { // the read-back after the write
				return nil
			}
		}
		return base(n, req)
	}

	_, err := ProbeLower(probeClient(p), "ffb_strength", 20)
	if err == nil {
		t.Fatal("a failed read back should be reported")
	}
	if p.values[0x02] != 450 {
		t.Errorf("the wheel was left at raw %d after a failed read back; it must be restored", p.values[0x02])
	}
}

// A value outside the command's envelope never reaches the wire.
func TestProbeLowerValidatesTheTarget(t *testing.T) {
	p := newEchoPort(t, map[uint8]int{0x12: 80})
	if _, err := ProbeLower(probeClient(p), "torque", 10); err == nil {
		t.Error("below the torque floor should be refused")
	}
	if len(p.written) != 0 {
		t.Error("nothing should reach the wire when the target is invalid")
	}
}

func TestProbeLowerRejectsUnknownSetting(t *testing.T) {
	p := newEchoPort(t, map[uint8]int{})
	_, err := ProbeLower(probeClient(p), "flux_capacitor", 1)
	if err == nil || !errors.Is(err, err) || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected an unknown-setting error, got %v", err)
	}
}

// If the current value sits outside the command's declared range, the restore
// would fail validation and leave the wheel at the probe value. That must be
// caught before anything is written, not discovered afterwards.
func TestProbeLowerRefusesWhenTheCurrentValueIsOutOfRange(t *testing.T) {
	// A base reporting 150% FFB strength: either the range or the conversion is
	// wrong, and 150 cannot be written back.
	p := newEchoPort(t, map[uint8]int{0x02: 1500})
	_, err := ProbeLower(probeClient(p), "ffb_strength", 20)
	if err == nil {
		t.Fatal("expected a refusal: the original could not be restored")
	}
	if !strings.Contains(err.Error(), "outside its declared range") {
		t.Errorf("the error should say why, got %v", err)
	}
	if len(p.written) > 1 {
		t.Errorf("only the initial read should have happened, got %d writes", len(p.written))
	}
}

// The sweep restores each setting before touching the next, so at no point is
// more than one value away from where it started.
func TestProbeSweepRestoresEachBeforeTheNext(t *testing.T) {
	p := newEchoPort(t, map[uint8]int{
		0x01: 450, // limit_angle 900 deg
		0x02: 450, // ffb_strength 45%
		0x07: 830, // damper 83
		0x12: 100, // torque 100%
	})
	// Record how many settings were away from their original at each write.
	original := map[uint8]int{}
	for id, v := range p.values {
		original[id] = v
	}
	maxDisturbed := 0
	base := p.reply
	p.reply = func(n int, req []byte) []byte {
		out := base(n, req)
		disturbed := 0
		for id, want := range original {
			if p.values[id] != want {
				disturbed++
			}
		}
		if disturbed > maxDisturbed {
			maxDisturbed = disturbed
		}
		return out
	}

	result, err := ProbeSweep(probeClient(p), []string{"limit_angle", "ffb_strength", "damper", "torque"})
	if err != nil {
		t.Fatalf("ProbeSweep: %v", err)
	}
	if !result.Ok() {
		t.Fatalf("sweep did not succeed: %+v", result)
	}
	if maxDisturbed > 1 {
		t.Errorf("%d settings were away from their original at once; the sweep must restore each before the next", maxDisturbed)
	}
	for id, want := range original {
		if p.values[id] != want {
			t.Errorf("id %#02x left at %d, want %d", id, p.values[id], want)
		}
	}
}

// An unverified conversion is skipped rather than probed, and skipping is not a
// failure — it is the harness declining to write a number it cannot vouch for.
func TestProbeSweepSkipsRatherThanGuesses(t *testing.T) {
	p := newEchoPort(t, map[uint8]int{0x02: 450})
	result, err := ProbeSweep(probeClient(p), []string{"ffb_strength", "natural_inertia_enabled"})
	if err != nil {
		t.Fatalf("ProbeSweep: %v", err)
	}
	if len(result.Reports) != 1 {
		t.Errorf("expected one probe, got %d", len(result.Reports))
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Key != "natural_inertia_enabled" {
		t.Errorf("natural_inertia_enabled should have been skipped, got %+v", result.Skipped)
	}
	if !result.Ok() {
		t.Error("a skip is not a failure")
	}
}

// A setting already at its floor has nothing lower to write; that is a skip, not
// an error, and must not stop the sweep.
func TestProbeSweepSkipsWhatCannotBeLowered(t *testing.T) {
	p := newEchoPort(t, map[uint8]int{0x02: 0, 0x07: 830}) // ffb_strength already 0
	result, err := ProbeSweep(probeClient(p), []string{"ffb_strength", "damper"})
	if err != nil {
		t.Fatalf("ProbeSweep: %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Key != "ffb_strength" {
		t.Errorf("expected ffb_strength skipped, got %+v", result.Skipped)
	}
	if len(result.Reports) != 1 {
		t.Errorf("the sweep should have continued to damper, got %d probes", len(result.Reports))
	}
}

// If a probe leaves the wheel unrestored the sweep stops there. Continuing would
// pile changes onto a base already in an unknown state.
func TestProbeSweepStopsWhenOneIsNotRestored(t *testing.T) {
	p := newEchoPort(t, map[uint8]int{0x02: 450, 0x07: 830})
	writes := 0
	base := p.reply
	p.reply = func(n int, req []byte) []byte {
		if req[2] == baseWriteGroup {
			writes++
			if writes == 2 { // the restore of the first setting
				return nil
			}
		}
		return base(n, req)
	}

	result, err := ProbeSweep(probeClient(p), []string{"ffb_strength", "damper"})
	if err == nil {
		t.Fatal("expected the sweep to stop")
	}
	if result.Aborted != "ffb_strength" {
		t.Errorf("aborted on %q, want ffb_strength", result.Aborted)
	}
	if p.values[0x07] != 830 {
		t.Error("damper was probed after the abort; the sweep must stop")
	}
	if result.Ok() {
		t.Error("an aborted sweep is not a success")
	}
}

// A grouped apply is the same back-to-back pattern that overran the base on
// reads. A dropped write is worse than a dropped read, so the pacing has to be
// there too.
func TestApplySettingsPacesItself(t *testing.T) {
	p := basePort(t, nil)
	patch := SettingsPatch{"ffb_strength": 40, "damper": 30, "friction": 20, "spring": 10}

	start := time.Now()
	if _, err := newTestClient(p).ApplySettings(patch); err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}
	if elapsed, want := time.Since(start), time.Duration(len(patch))*writeGap/2; elapsed < want {
		t.Errorf("wrote %d settings in %v, faster than pacing allows (%v): the base will be overrun",
			len(patch), elapsed, want)
	}
}

// seedPort builds a stateful base holding the given settings, in display units.
func seedPort(t *testing.T, values map[string]int) *echoPort {
	t.Helper()
	initial := map[uint8]int{}
	for key, display := range values {
		cmd, ok := LookupBaseCommand(key)
		if !ok {
			t.Fatalf("unknown setting %q", key)
		}
		payload, err := cmd.encode(display)
		if err != nil {
			t.Fatalf("encode %s = %d: %v", key, display, err)
		}
		initial[cmd.ID[0]] = int(payload[0])<<8 | int(payload[1])
	}
	return newEchoPort(t, initial)
}

// firstWriteState is a plausible wheel: every setting in the first-write set at
// a value with room to be lowered.
func firstWriteState() map[string]int {
	return map[string]int{
		"limit_angle": 900, "torque": 100, "speed": 100,
		"ffb_strength": 60,
		"damper":       40, "friction": 40, "spring": 40, "inertia": 300,
		"speed_damping": 20,
	}
}

func TestProbeApplyLowersEverythingThenRestores(t *testing.T) {
	state := firstWriteState()
	p := seedPort(t, state)
	before := map[uint8]int{}
	for id, raw := range p.values {
		before[id] = raw
	}

	report, err := ProbeApply(probeClient(p), FirstWriteSet())
	if err != nil {
		t.Fatalf("ProbeApply: %v\n%s", err, report)
	}
	if !report.Ok() {
		t.Fatalf("probe not ok: failed=%v mismatched=%v restored=%v\n%s",
			report.Failed, report.Mismatched, report.Restored, report)
	}
	if len(report.Order) != len(state) {
		t.Errorf("wrote %v, want all %d settings", report.Order, len(state))
	}
	for key, original := range report.Originals {
		if original != state[key] {
			t.Errorf("%s read as %d, want %d", key, original, state[key])
		}
		if report.Targets[key] >= original {
			t.Errorf("%s target %d is not below %d — the probe must only lower", key, report.Targets[key], original)
		}
	}
	for id, raw := range before {
		if p.values[id] != raw {
			t.Errorf("id %#02x left at raw %d, want the original %d", id, p.values[id], raw)
		}
	}
}

// The failure the read-back exists to catch. A base that acknowledges a write
// and does not take it would otherwise leave the user believing in a change that
// never happened — an acknowledgement is not proof, so the probe reads.
func TestProbeApplyCatchesAnAcknowledgedWriteThatDidNotStick(t *testing.T) {
	damper, _ := LookupBaseCommand("damper")
	p := seedPort(t, firstWriteState())
	cooperative := p.reply
	p.reply = func(n int, req []byte) []byte {
		if req[2] == baseWriteGroup && req[4] == damper.ID[0] {
			return replyTo(t, req, req[5:7]) // acknowledged, but nothing stored
		}
		return cooperative(n, req)
	}

	report, err := ProbeApply(probeClient(p), FirstWriteSet())
	if err != nil {
		t.Fatalf("ProbeApply: %v", err)
	}
	if len(report.Failed) != 0 {
		t.Errorf("the write was acknowledged, so it should not be reported as failed: %v", report.Failed)
	}
	if len(report.Mismatched) != 1 || report.Mismatched[0] != "damper" {
		t.Errorf("mismatched = %v, want [damper]", report.Mismatched)
	}
	if report.Ok() {
		t.Error("a setting that did not read back must not count as a clean probe")
	}
	// The wheel is nonetheless as it was found — a write that never took cannot
	// have changed anything — and the restore confirms that by reading rather
	// than by assuming.
	if len(report.Unrestored) != 0 {
		t.Errorf("unrestored = %v; nothing was actually altered", report.Unrestored)
	}
	if got := p.values[0x02]; got != 600 {
		t.Errorf("ffb_strength left at raw %d, want the original 600", got)
	}
}

// A setting with nothing lower to write, or one the base does not answer, is
// skipped rather than failing the probe — neither is a fault.
func TestProbeApplySkipsRatherThanFails(t *testing.T) {
	state := firstWriteState()
	state["speed_damping"] = 0 // already at its floor
	p := seedPort(t, state)
	cooperative := p.reply
	spring, _ := LookupBaseCommand("spring")
	p.reply = func(n int, req []byte) []byte {
		if req[4] == spring.ID[0] {
			return nil // this base does not answer for spring
		}
		return cooperative(n, req)
	}

	report, err := ProbeApply(probeClient(p), FirstWriteSet())
	if err != nil {
		t.Fatalf("ProbeApply: %v\n%s", err, report)
	}
	skipped := map[string]bool{}
	for _, s := range report.Skipped {
		skipped[s.Key] = true
	}
	if !skipped["speed_damping"] || !skipped["spring"] {
		t.Errorf("skipped = %v, want speed_damping and spring", report.Skipped)
	}
	if _, wrote := report.Targets["spring"]; wrote {
		t.Error("a setting that could not be read must not be written")
	}
	if !report.Ok() {
		t.Errorf("skips are not failures: %v %v %v", report.Failed, report.Mismatched, report.Restored)
	}
}

// The guard that keeps an unconfirmed conversion off the wire: writing 20 when
// the hardware reads it as 200 is the accident the probe exists to avoid.
func TestProbeApplyRefusesUnverifiedConversions(t *testing.T) {
	var unverified string
	for _, cmd := range BaseCommands() {
		if !cmd.Verified {
			unverified = cmd.Key
			break
		}
	}
	if unverified == "" {
		t.Skip("every conversion is confirmed now")
	}
	p := seedPort(t, firstWriteState())
	report, err := ProbeApply(probeClient(p), []string{"ffb_strength", unverified})
	if err != nil {
		t.Fatalf("ProbeApply: %v", err)
	}
	if _, wrote := report.Targets[unverified]; wrote {
		t.Errorf("%s has an unconfirmed conversion and must not be written", unverified)
	}
}
