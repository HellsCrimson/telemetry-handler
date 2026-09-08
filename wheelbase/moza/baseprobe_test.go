package moza

import (
	"errors"
	"strings"
	"testing"
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
	cmd, ok := LookupBaseCommand("interpolation")
	if !ok {
		t.Skip("interpolation not in the registry")
	}
	if cmd.Verified {
		t.Skip("interpolation is now verified; pick another unverified command for this test")
	}
	p := newEchoPort(t, map[uint8]int{})
	_, err := ProbeLower(probeClient(p), "interpolation", 1)
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
