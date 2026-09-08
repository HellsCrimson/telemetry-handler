package moza

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fakePort is a scripted serial device: it records what was written and replies
// with whatever the script says, so the whole transaction layer is testable
// without a wheelbase plugged in.
type fakePort struct {
	written [][]byte
	// reply builds the bytes to return for the nth request. Returning nil means
	// the base said nothing.
	reply func(n int, request []byte) []byte
	// pending holds bytes not yet handed to the reader, so a reply can be split
	// across several reads.
	pending []byte
	// chunk caps how many bytes a single read returns; 0 means all of them.
	chunk    int
	writeErr error
}

func (p *fakePort) WriteFrame(frame []byte) error {
	if p.writeErr != nil {
		return p.writeErr
	}
	cp := make([]byte, len(frame))
	copy(cp, frame)
	p.written = append(p.written, cp)
	if p.reply != nil {
		p.pending = append(p.pending, p.reply(len(p.written)-1, cp)...)
	}
	return nil
}

func (p *fakePort) read(b []byte) (int, error) {
	if len(p.pending) == 0 {
		return 0, nil
	}
	n := len(p.pending)
	if p.chunk > 0 && p.chunk < n {
		n = p.chunk
	}
	if n > len(b) {
		n = len(b)
	}
	copy(b, p.pending[:n])
	p.pending = p.pending[n:]
	return n, nil
}

// classifyRequest identifies which registry command a request is for and whether
// it is a write.
//
// Group alone cannot tell the two apart — the main device answers reads and
// writes on 0x1f — so the payload is what distinguishes them: a read carries
// none.
func classifyRequest(req []byte) (BaseCommand, bool, bool) {
	for _, cmd := range baseCommands {
		if req[3] != cmd.device() {
			continue
		}
		for _, dir := range []struct {
			group uint8
			id    []uint8
			write bool
		}{{cmd.readGroup(), cmd.readID(), false}, {cmd.writeGroup(), cmd.writeID(), true}} {
			if req[2] != dir.group || len(req) < 5+len(dir.id) {
				continue
			}
			if string(req[4:4+len(dir.id)]) != string(dir.id) {
				continue
			}
			// 0x7e, len, group, device, id..., payload..., checksum
			payload := len(req) - 5 - len(dir.id)
			if dir.write != (payload > 0) {
				continue
			}
			return cmd, dir.write, true
		}
	}
	return BaseCommand{}, false, false
}

// basePort scripts a cooperative wheelbase: a write is acknowledged with the
// value it carried, a read is answered with the current value.
//
// current is in display units, keyed by command, and defaults to the command's
// maximum. It exists because a grouped apply now READS the safety settings
// before ordering them — whether a limit is being tightened or loosened decides
// when it is written.
func basePort(t *testing.T, current map[string]int) *fakePort {
	t.Helper()
	return &fakePort{reply: func(_ int, req []byte) []byte {
		cmd, write, ok := classifyRequest(req)
		if !ok {
			t.Fatalf("unrecognised request % x", req)
		}
		if write {
			return replyTo(t, req, req[len(req)-3:len(req)-1])
		}
		value, has := current[cmd.Key]
		if !has {
			value = cmd.Max
		}
		payload, err := cmd.encode(value)
		if err != nil {
			t.Fatalf("encode %s = %d: %v", cmd.Key, value, err)
		}
		return replyTo(t, req, payload)
	}}
}

// newTestClient wires a client to a fake port with a short timeout, so the
// no-reply path does not add half a second to the suite.
func newTestClient(p *fakePort) *BaseClient {
	c := NewBaseClient(p)
	c.timeout = 30 * time.Millisecond
	return c
}

// replyTo builds the base's answer to a request: same command id, given payload,
// response group and swapped device.
func replyTo(t *testing.T, request []byte, payload []byte) []byte {
	t.Helper()
	group, device := request[2], request[3]
	id := request[4:5]
	frame, err := buildFrame(responseGroup(group), responseDevice(device), id, payload)
	if err != nil {
		t.Fatalf("buildFrame: %v", err)
	}
	return frame
}

func TestReadSetting(t *testing.T) {
	cmd, _ := LookupBaseCommand("ffb_strength")
	p := &fakePort{reply: func(_ int, req []byte) []byte {
		return replyTo(t, req, []byte{0x01, 0xc2}) // raw 450 = 45%
	}}
	got, err := newTestClient(p).ReadSetting(cmd)
	if err != nil {
		t.Fatalf("ReadSetting: %v", err)
	}
	if got != 45 {
		t.Errorf("value = %d%%, want 45 — the base stores tenths", got)
	}
	// The request must be a read (0x28) to the base (0x13).
	req := p.written[0]
	if req[2] != baseReadGroup || req[3] != baseDevice {
		t.Errorf("request group/device = %#02x/%#02x", req[2], req[3])
	}
}

// A reply arriving in pieces across several reads must be reassembled, not
// dropped — the port hands back whatever happens to be buffered.
func TestReadSettingReassemblesASplitReply(t *testing.T) {
	cmd, _ := LookupBaseCommand("limit_angle")
	p := &fakePort{
		chunk: 2,
		reply: func(_ int, req []byte) []byte { return replyTo(t, req, []byte{0x01, 0xc2}) }, // raw 450 per side
	}
	got, err := newTestClient(p).ReadSetting(cmd)
	if err != nil {
		t.Fatalf("ReadSetting: %v", err)
	}
	if got != 900 {
		t.Errorf("value = %d deg, want 900 — the base stores the angle per side", got)
	}
}

// Traffic for other commands must not be mistaken for the answer.
func TestReadSettingIgnoresUnrelatedFrames(t *testing.T) {
	cmd, _ := LookupBaseCommand("damper")
	p := &fakePort{reply: func(_ int, req []byte) []byte {
		noise, _ := buildFrame(responseGroup(baseStatusGroup), responseDevice(baseDevice), []uint8{0x01}, []byte{0xff, 0xff})
		other, _ := buildFrame(responseGroup(baseReadGroup), responseDevice(baseDevice), []uint8{0x99}, []byte{0x12, 0x34})
		return append(append(noise, other...), replyTo(t, req, []byte{0x03, 0x3e})...) // raw 830 = 83
	}}
	got, err := newTestClient(p).ReadSetting(cmd)
	if err != nil {
		t.Fatalf("ReadSetting: %v", err)
	}
	if got != 83 {
		t.Errorf("value = %d, want 83", got)
	}
}

// An older base simply will not answer some commands. That must be reported as
// unsupported so the page can mark the control unavailable, not fail outright.
func TestReadSettingReportsSilenceAsUnsupported(t *testing.T) {
	cmd, _ := LookupBaseCommand("equalizer6")
	p := &fakePort{} // never replies
	_, err := newTestClient(p).ReadSetting(cmd)
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("expected ErrUnsupported, got %v", err)
	}
}

func TestWriteSettingSendsAndVerifiesTheAck(t *testing.T) {
	cmd, _ := LookupBaseCommand("ffb_strength")
	p := &fakePort{reply: func(_ int, req []byte) []byte {
		return replyTo(t, req, req[5:7]) // echo the written value
	}}
	if err := newTestClient(p).WriteSetting(cmd, 75); err != nil {
		t.Fatalf("WriteSetting: %v", err)
	}
	req := p.written[0]
	if req[2] != baseWriteGroup {
		t.Errorf("group = %#02x, want %#02x", req[2], baseWriteGroup)
	}
	// 75% is written as raw 750: the display value must be scaled on the way out
	// as well as in, or a write undoes what a read showed.
	if req[5] != 0x02 || req[6] != 0xee {
		t.Errorf("payload = % x, want 02 ee (raw 750 for 75%%)", req[5:7])
	}
}

// If the base acknowledges a different value it did not take the write, and the
// user must not be told it did.
func TestWriteSettingRejectsAMismatchedAck(t *testing.T) {
	cmd, _ := LookupBaseCommand("ffb_strength")
	p := &fakePort{reply: func(_ int, req []byte) []byte {
		return replyTo(t, req, []byte{0x00, 0x64}) // acknowledges raw 100 = 10%, not 75%
	}}
	err := newTestClient(p).WriteSetting(cmd, 75)
	if err == nil {
		t.Fatal("a mismatched acknowledgement must be an error")
	}
	if !strings.Contains(err.Error(), "acknowledged") {
		t.Errorf("the error should say what the base acknowledged, got %v", err)
	}
}

// Validation happens before anything reaches the wire, so a value the UI should
// never have offered cannot reach the hardware.
func TestWriteSettingValidatesBeforeSending(t *testing.T) {
	cmd, _ := LookupBaseCommand("torque")
	p := &fakePort{}
	if err := newTestClient(p).WriteSetting(cmd, 250); err == nil {
		t.Fatal("out-of-range value should be rejected")
	}
	if len(p.written) != 0 {
		t.Errorf("nothing should have been written, got % x", p.written)
	}
}

// A safety limit being TIGHTENED goes first. An apply cut short then leaves the
// base more restricted than either preset intended, rather than pairing an old
// high torque cap with limits that never arrived.
func TestApplySettingsWritesTighteningSafetyFirst(t *testing.T) {
	p := basePort(t, map[string]int{"torque": 100, "limit_angle": 900})
	patch := SettingsPatch{
		"damper":       40,
		"ffb_strength": 80,
		"torque":       70,  // down from 100
		"limit_angle":  540, // down from 900
		"friction":     20,
	}
	res, err := newTestClient(p).ApplySettings(patch)
	if err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}
	if !res.Ok() {
		t.Fatalf("expected a clean apply, failed: %v", res.Failed)
	}
	if len(res.Applied) != len(patch) {
		t.Fatalf("applied %v", res.Applied)
	}
	for i, key := range res.Applied[:2] {
		if !IsSafetyCommand(key) {
			t.Errorf("position %d is %q; a tightening safety envelope must be written first (order: %v)", i, key, res.Applied)
		}
	}
	if IsSafetyCommand(res.Applied[len(res.Applied)-1]) {
		t.Errorf("a safety setting was written last: %v", res.Applied)
	}
}

// The mirror image, and the reason "safety first" alone is not the rule. A limit
// being RAISED must be written LAST: an apply interrupted after it would
// otherwise leave the more permissive cap paired with the previous preset's
// settings — looser than anything the user asked for.
func TestApplySettingsWritesRaisingSafetyLast(t *testing.T) {
	p := basePort(t, map[string]int{"torque": 50, "limit_angle": 360})
	patch := SettingsPatch{
		"damper":       40,
		"ffb_strength": 80,
		"torque":       70,  // up from 50
		"limit_angle":  540, // up from 360
		"friction":     20,
	}
	res, err := newTestClient(p).ApplySettings(patch)
	if err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}
	if !res.Ok() {
		t.Fatalf("expected a clean apply, failed: %v", res.Failed)
	}
	tail := res.Applied[len(res.Applied)-2:]
	for i, key := range tail {
		if !IsSafetyCommand(key) {
			t.Errorf("tail position %d is %q; a raised limit must be written last (order: %v)", i, key, res.Applied)
		}
	}
	if IsSafetyCommand(res.Applied[0]) {
		t.Errorf("a raised safety limit was written first: %v", res.Applied)
	}
}

// A patch can do both at once, and each limit is placed by its own direction:
// the one coming down leads, the one going up trails.
func TestApplySettingsSplitsSafetyByDirection(t *testing.T) {
	p := basePort(t, map[string]int{"torque": 50, "limit_angle": 900})
	res, err := newTestClient(p).ApplySettings(SettingsPatch{
		"ffb_strength": 80,
		"torque":       70,  // up
		"limit_angle":  540, // down
	})
	if err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}
	if got := res.Applied; len(got) != 3 || got[0] != "limit_angle" || got[2] != "torque" {
		t.Errorf("order = %v, want limit_angle (tightening) first and torque (loosening) last", got)
	}
}

// A safety limit whose current value cannot be read is treated as if it were
// being raised. The cautious placement is last: if the apply is interrupted
// before it, the wheel keeps the older, unknown limit rather than a possibly
// looser new one.
func TestApplySettingsTreatsAnUnreadableLimitAsRaising(t *testing.T) {
	torque, _ := LookupBaseCommand("torque")
	p := basePort(t, nil)
	cooperative := p.reply
	p.reply = func(n int, req []byte) []byte {
		if cmd, write, ok := classifyRequest(req); ok && !write && cmd.Key == torque.Key {
			return nil // the base does not answer the read
		}
		return cooperative(n, req)
	}
	res, err := newTestClient(p).ApplySettings(SettingsPatch{"ffb_strength": 80, "torque": 70})
	if err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}
	if got := res.Applied; len(got) != 2 || got[1] != "torque" {
		t.Errorf("order = %v; an unreadable limit must be written last", got)
	}
}

// One unsupported command on an older base should not stop the rest of a preset
// from landing, and the caller must be able to tell which is which.
func TestApplySettingsContinuesPastAFailure(t *testing.T) {
	cmd, _ := LookupBaseCommand("equalizer6")
	p := &fakePort{reply: func(_ int, req []byte) []byte {
		if req[4] == cmd.ID[0] {
			return nil // this base does not implement it
		}
		return basePort(t, nil).reply(0, req)
	}}
	res, err := newTestClient(p).ApplySettings(SettingsPatch{
		"ffb_strength": 60,
		"equalizer6":   50,
		"damper":       30,
	})
	if err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}
	if len(res.Applied) != 2 {
		t.Errorf("applied = %v, want the two supported settings", res.Applied)
	}
	if !errors.Is(res.Failed["equalizer6"], ErrUnsupported) {
		t.Errorf("failure for equalizer6 = %v", res.Failed["equalizer6"])
	}
	if res.Ok() {
		t.Error("a partial apply must not report success")
	}
}

func TestApplySettingsRejectsAnUnknownKey(t *testing.T) {
	p := &fakePort{}
	if _, err := newTestClient(p).ApplySettings(SettingsPatch{"flux_capacitor": 1}); err == nil {
		t.Fatal("an unknown setting key should be rejected")
	}
	if len(p.written) != 0 {
		t.Error("nothing should reach the wire when the patch is invalid")
	}
}

func TestBaseCommandValidation(t *testing.T) {
	torque, _ := LookupBaseCommand("torque")
	if err := torque.Validate(49); err == nil {
		t.Error("below the torque floor should be rejected")
	}
	if err := torque.Validate(100); err != nil {
		t.Errorf("100%% torque should be allowed: %v", err)
	}

	rev, _ := LookupBaseCommand("ffb_reverse")
	if err := rev.Validate(2); err == nil {
		t.Error("a boolean should only accept 0 or 1")
	}
	if err := rev.Validate(1); err != nil {
		t.Errorf("1 should be valid for a boolean: %v", err)
	}
}

// Scaling, from the values read off a real R12 V2 against Boxflat. Each of these
// was wrong on the first build, and each was wrong in a different way, so they
// are pinned individually rather than as one rule.
func TestScalingMatchesTheHardware(t *testing.T) {
	cases := []struct {
		key     string
		raw     int
		display int
		why     string
	}{
		{"limit_angle", 450, 900, "stored per side, shown lock to lock"},
		{"ffb_strength", 450, 45, "stored in tenths of a percent"},
		{"speed", 100, 10, "stored in tenths"},
		{"damper", 830, 83, "stored in tenths"},
		{"friction", 830, 83, "stored in tenths"},
		// Its range is 100..500, so the tenths conversion is exercised with a value
		// the setting can actually hold — 325 is what a real base reported.
		{"inertia", 3250, 325, "stored in tenths"},
		{"natural_inertia", 900, 900, "unscaled, unlike the other inertia"},
		{"road_sensitivity", 50, 10, "five raw steps per displayed step"},
		{"soft_limit_stiffness", 100, 1, "stored in hundredths"},
		{"equalizer1", 200, 200, "unscaled"},
		// The main device stores the game gains as one 0..255 byte, shown as a
		// percentage: Boxflat reports 128 as 50%.
		{"game_damper", 128, 50, "0..255 byte shown as a percentage"},
		{"game_spring", 255, 100, "full scale"},
		{"game_friction", 0, 0, "zero"},
		// Affine, not scaled: the three positions sit at 56/78/100 on the wire.
		{"soft_limit_strength", 56, 0, "Soft"},
		{"soft_limit_strength", 78, 1, "Middle — the value a real base reported"},
		{"soft_limit_strength", 100, 2, "Hard"},
		{"spring", 60, 6, "tenths, confirmed by sweeping 0 to 6"},
		{"music_volume", 128, 50, "0..255 byte shown as a percentage, like the game gains"},
		{"music_index", 1, 1, "1-based track index, passed through"},
	}
	for _, tc := range cases {
		cmd, ok := LookupBaseCommand(tc.key)
		if !ok {
			t.Errorf("%s: not in the registry", tc.key)
			continue
		}
		raw := []byte{uint8(tc.raw >> 8), uint8(tc.raw)}
		if cmd.Bytes == 1 {
			raw = []byte{uint8(tc.raw)}
		}
		got, err := cmd.decode(raw)
		if err != nil {
			t.Errorf("%s: decode: %v", tc.key, err)
			continue
		}
		if got != tc.display {
			t.Errorf("%s: raw %d decoded to %d, want %d (%s)", tc.key, tc.raw, got, tc.display, tc.why)
		}

		// A write must undo exactly what a read did, or setting a value to what
		// the page already shows would change it.
		back, err := cmd.encode(tc.display)
		if err != nil {
			t.Errorf("%s: encode(%d): %v", tc.key, tc.display, err)
			continue
		}
		if string(back) != string(raw) {
			t.Errorf("%s: encode(%d) = % x, want % x — a round trip must not move the value",
				tc.key, tc.display, back, raw)
		}
	}
}

// A scale or offset is a claim about how the hardware encodes a value, and every
// one in this registry was wrong on the first attempt: FFB strength read 450%,
// the rotation limit read half of what the wheel does, and soft limit strength
// turned out to be affine rather than scaled. So a conversion may only exist
// once it has been confirmed against a real base — an unverified command must
// pass its value through untouched rather than carry a guess that looks
// authoritative on screen.
func TestConversionsAreOnlyClaimedWhenConfirmed(t *testing.T) {
	for _, cmd := range BaseCommands() {
		converts := (cmd.Scale != 0 && cmd.Scale != 1) || cmd.Offset != 0
		if converts && !cmd.Verified {
			t.Errorf("%s: converts (scale %v, offset %v) but is not marked Verified — "+
				"an unconfirmed conversion shows a confidently wrong number",
				cmd.Key, cmd.Scale, cmd.Offset)
		}
	}
}

// The main device's gains and interpolation use one group for both directions
// with a different id each way, unlike every base command.
func TestMainDeviceCommandsAddressCorrectly(t *testing.T) {
	for _, key := range []string{"game_damper", "game_friction", "game_inertia", "game_spring", "interpolation"} {
		cmd, ok := LookupBaseCommand(key)
		if !ok {
			t.Errorf("%s missing from the registry", key)
			continue
		}
		if cmd.device() != mainDevice {
			t.Errorf("%s: device %#02x, want the main device %#02x", key, cmd.device(), mainDevice)
		}
		if cmd.readGroup() != mainGroup || cmd.writeGroup() != mainGroup {
			t.Errorf("%s: groups %#02x/%#02x, want %#02x both ways", key, cmd.readGroup(), cmd.writeGroup(), mainGroup)
		}
		if string(cmd.readID()) == string(cmd.writeID()) {
			t.Errorf("%s: read and write ids are the same; the main device uses different ids per direction", key)
		}
	}
}

// Every registry entry must be self-consistent, or a UI built from it will offer
// something the encoder cannot send.
func TestRegistryIsSelfConsistent(t *testing.T) {
	seen := map[string]bool{}
	ids := map[string]string{}
	for _, cmd := range BaseCommands() {
		if seen[cmd.Key] {
			t.Errorf("duplicate key %q", cmd.Key)
		}
		seen[cmd.Key] = true

		// Keyed by device too: the base and main devices have independent id
		// spaces, so the same id on each is not a collision.
		idKey := string(append([]byte{cmd.device()}, cmd.readID()...))
		if prev, dup := ids[idKey]; dup {
			t.Errorf("commands %q and %q share device %#02x id % x", prev, cmd.Key, cmd.device(), cmd.readID())
		}
		ids[idKey] = cmd.Key

		if cmd.Bytes != 1 && cmd.Bytes != 2 {
			t.Errorf("%s: width %d is not encodable", cmd.Key, cmd.Bytes)
		}
		if len(cmd.readID()) == 0 || len(cmd.writeID()) == 0 {
			t.Errorf("%s: no command id for one of the directions", cmd.Key)
		}
		// An enum without labels has no way to render as a choice, so it silently
		// degrades into a slider over raw numbers — which is exactly how
		// "hands-off protection" and "protection mode" first shipped looking like
		// percentage sliders. Requiring one label per value makes that
		// unrepresentable rather than merely discouraged.
		if cmd.Kind == KindEnum {
			want := cmd.Max - cmd.Min + 1
			if len(cmd.Labels) != want {
				t.Errorf("%s: enum spans %d values but has %d labels", cmd.Key, want, len(cmd.Labels))
			}
		}
		// Conversely, labels on something that is not an enum will never be shown.
		if cmd.Kind != KindEnum && len(cmd.Labels) > 0 {
			t.Errorf("%s: has labels but is not an enum, so they are dead weight", cmd.Key)
		}
		// A boolean's range is implied; setting one invites it to disagree with
		// Validate, which only accepts 0 or 1.
		if cmd.Kind == KindBool && (cmd.Min != 0 || cmd.Max != 0) {
			t.Errorf("%s: boolean should not declare a range (%d..%d)", cmd.Key, cmd.Min, cmd.Max)
		}
		if cmd.Kind == KindInt || cmd.Kind == KindEnum {
			if cmd.Max <= cmd.Min {
				t.Errorf("%s: empty range %d..%d", cmd.Key, cmd.Min, cmd.Max)
			}
			max := 1<<(8*cmd.Bytes) - 1
			if cmd.Max > max {
				t.Errorf("%s: max %d does not fit in %d byte(s)", cmd.Key, cmd.Max, cmd.Bytes)
			}
			if _, err := cmd.encode(cmd.Max); err != nil {
				t.Errorf("%s: cannot encode its own maximum: %v", cmd.Key, err)
			}
		}
	}
	// The safety keys must all exist, or the apply ordering silently does nothing.
	for key := range safetyKeys {
		if !seen[key] {
			t.Errorf("safety key %q is not in the registry", key)
		}
	}
}

// Nothing has been confirmed on an R12 V2 yet. This guards against a future
// reader assuming the provisional ranges are authoritative — when hardware
// validation lands, flip Verified and this test tells you what is still open.
func TestUnverifiedCommandsAreMarked(t *testing.T) {
	for _, cmd := range BaseCommands() {
		if cmd.Verified {
			continue
		}
		if cmd.Min == 0 && cmd.Max == 0 && cmd.Kind == KindInt {
			t.Errorf("%s: unverified and has no usable range", cmd.Key)
		}
	}
}

// An older base answering only part of the command set must still yield a usable
// page: the rest is marked unavailable rather than shown as zero, and the read as
// a whole succeeds.
func TestReadAllSettingsMarksUnsupportedRatherThanFailing(t *testing.T) {
	silent := map[uint8]bool{0x2c: true, 0x1b: true} // equalizer6, soft_limit_strength
	p := &fakePort{reply: func(_ int, req []byte) []byte {
		if silent[req[4]] {
			return nil
		}
		return replyTo(t, req, []byte{0x00, 0x0c})
	}}

	snap, err := newTestClient(p).ReadAllSettings()
	if err != nil {
		t.Fatalf("a base that answers most commands should not fail the read: %v", err)
	}
	if !snap.Unsupported["equalizer6"] || !snap.Unsupported["soft_limit_strength"] {
		t.Errorf("silent commands should be marked unsupported, got %v", snap.Unsupported)
	}
	if _, present := snap.Values["equalizer6"]; present {
		t.Error("an unsupported command must not appear as a value")
	}
	if snap.Values["ffb_strength"] != 1 { // raw 12 in tenths
		t.Errorf("supported commands should still be read, got %v", snap.Values["ffb_strength"])
	}
}

// A transport failure is not the same as an unsupported command: continuing would
// just produce a page full of spurious "unsupported".
func TestReadAllSettingsAbortsOnTransportFailure(t *testing.T) {
	p := &fakePort{writeErr: errors.New("device disconnected")}
	if _, err := newTestClient(p).ReadAllSettings(); err == nil {
		t.Fatal("a transport failure should abort the read")
	}
}

// Status uses group 0x2b, not the settings read group, and the ids are the ones
// confirmed on hardware — an earlier guess read state-err and an unassigned id,
// which is how the page came to show "0, 0, 3600".
func TestReadStatusReadsTheConfirmedIDs(t *testing.T) {
	seen := map[uint8]bool{}
	p := &fakePort{reply: func(_ int, req []byte) []byte {
		seen[req[4]] = true
		if req[2] != baseStatusGroup {
			t.Errorf("status request used group %#02x, want %#02x", req[2], baseStatusGroup)
		}
		return replyTo(t, req, []byte{0x0e, 0x10}) // 3600
	}}
	if _, err := newTestClient(p).ReadStatus(); err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	for _, id := range []uint8{statusStateID, statusErrorID, statusMCUTempID, statusMOSFETID, statusMotorTempID} {
		if !seen[id] {
			t.Errorf("status id %#02x was never requested", id)
		}
	}
	if seen[0x03] {
		t.Error("0x03 is unassigned and must not be read")
	}
}

// The base reports hundredths of a degree: 3600 is 36 C, not 3600 C. Reporting
// the raw value is how the first build showed a motor at 3600 degrees.
func TestReadStatusScalesTemperatures(t *testing.T) {
	p := &fakePort{reply: func(_ int, req []byte) []byte {
		return replyTo(t, req, []byte{0x0e, 0x10}) // 3600 = 36.00 C
	}}
	status, err := newTestClient(p).ReadStatus()
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if got := status.Temps["mcu_temp"]; got != 36 {
		t.Errorf("mcu_temp = %v C, want 36", got)
	}
	// The state words are raw counters, not temperatures, and must not be scaled.
	if !status.HasState || status.State != 3600 {
		t.Errorf("state = %d (has=%v), want the raw 3600", status.State, status.HasState)
	}
}

// The status ids are unconfirmed, so a base answering none of them is a base to
// show without temperatures — not an error.
func TestReadStatusToleratesSilence(t *testing.T) {
	p := &fakePort{}
	status, err := newTestClient(p).ReadStatus()
	if err != nil {
		t.Fatalf("silence should not be an error: %v", err)
	}
	if len(status.Temps) != 0 {
		t.Errorf("temps = %v, want none", status.Temps)
	}
	// state, state_err and the three temperatures.
	if len(status.Unsupported) != 2+len(statusTemps) {
		t.Errorf("every field should be marked unsupported, got %v", status.Unsupported)
	}
}

// WithBase must refuse rather than open a second handle when there is no driver
// and no port — the failure mode it exists to prevent is two handles racing.
func TestWithBaseRefusesWithoutAPort(t *testing.T) {
	if err := WithBase(nil, "", func(*BaseClient) error {
		t.Fatal("fn must not run without a port")
		return nil
	}); err == nil {
		t.Fatal("expected an error with no driver and no port")
	}
}

// Road sensitivity is a macro: changing it also rewrites the equalizer bands.
// A preset carrying both must apply the macro FIRST, so the explicit band values
// land on top of what it did. The other order silently discards them, and the
// driver gets an equalizer they did not ask for with no way to see it.
func TestApplySettingsWritesMacrosBeforeWhatTheyRewrite(t *testing.T) {
	p := basePort(t, nil)
	res, err := newTestClient(p).ApplySettings(SettingsPatch{
		"equalizer1":       200,
		"equalizer3":       150,
		"road_sensitivity": 6,
	})
	if err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}
	order := res.Applied
	if len(order) != 3 {
		t.Fatalf("applied %v", order)
	}
	macroAt, eqAt := -1, -1
	for i, key := range order {
		if key == "road_sensitivity" {
			macroAt = i
		}
		if eqAt == -1 && strings.HasPrefix(key, "equalizer") {
			eqAt = i
		}
	}
	if macroAt > eqAt {
		t.Errorf("road sensitivity applied at %d, after an equalizer band at %d: the macro would overwrite it (%v)",
			macroAt, eqAt, order)
	}
}

// The interaction is reported, so a caller can tell the user their two requests
// overlap rather than silently resolving it for them.
func TestRewritesReportsTheInteraction(t *testing.T) {
	got := Rewrites(SettingsPatch{"road_sensitivity": 6, "equalizer2": 100, "damper": 40})
	if got["equalizer2"] != "road_sensitivity" {
		t.Errorf("equalizer2 should be reported as rewritten by road_sensitivity, got %v", got)
	}
	if _, unrelated := got["damper"]; unrelated {
		t.Error("damper is unaffected and should not be reported")
	}
	if len(Rewrites(SettingsPatch{"equalizer2": 100})) != 0 {
		t.Error("no macro in the patch means no interaction")
	}
}

// Every key in an Affects list must exist, or the ordering rule silently does
// nothing the day a key is renamed.
func TestAffectsReferenceRealCommands(t *testing.T) {
	for _, cmd := range BaseCommands() {
		for _, affected := range cmd.Affects {
			if _, ok := LookupBaseCommand(affected); !ok {
				t.Errorf("%s affects %q, which is not in the registry", cmd.Key, affected)
			}
		}
	}
}

// A single dropped reply is not evidence that a base lacks a setting. Observed
// on a real R12 V2: damping level read as unsupported once and answered on the
// next attempt, which would have hidden a control the wheel plainly has.
func TestReadAllSettingsRetriesASilentCommand(t *testing.T) {
	const damping = 0x19 // speed_damping
	misses := 0
	p := &fakePort{reply: func(_ int, req []byte) []byte {
		if req[4] == damping && misses == 0 {
			misses++
			return nil // one dropped reply
		}
		return replyTo(t, req, []byte{0x00, 0x28})
	}}

	snap, err := newTestClient(p).ReadAllSettings()
	if err != nil {
		t.Fatalf("ReadAllSettings: %v", err)
	}
	if snap.Unsupported["speed_damping"] {
		t.Error("a single miss should be retried, not taken as unsupported")
	}
	if _, ok := snap.Values["speed_damping"]; !ok {
		t.Error("the retried value should be present")
	}
}

// A base that never answers is still reported as unsupported, so the retry does
// not turn a genuine gap into a hang or a false reading.
func TestReadAllSettingsStillDetectsAGenuineGap(t *testing.T) {
	const eq6 = 0x2c
	p := &fakePort{reply: func(_ int, req []byte) []byte {
		if req[4] == eq6 {
			return nil
		}
		return replyTo(t, req, []byte{0x00, 0x28})
	}}
	snap, err := newTestClient(p).ReadAllSettings()
	if err != nil {
		t.Fatalf("ReadAllSettings: %v", err)
	}
	if !snap.Unsupported["equalizer6"] {
		t.Error("a command that never answers should be unsupported")
	}
}

// A bulk read must pace itself. Firing every request back to back overruns the
// base and it stops replying to some of them — measured on an R12 V2, where the
// damping level went from failing about half of all sweeps to zero once a small
// gap was introduced. The sleep looks removable and is not, so its absence is a
// test failure rather than a mystery months later.
func TestReadAllSettingsPacesItself(t *testing.T) {
	p := &fakePort{reply: func(_ int, req []byte) []byte { return replyTo(t, req, []byte{0x00, 0x0a}) }}
	start := time.Now()
	if _, err := newTestClient(p).ReadAllSettings(); err != nil {
		t.Fatalf("ReadAllSettings: %v", err)
	}
	elapsed := time.Since(start)

	commands := len(BaseCommands())
	// Allow generous slack for scheduler jitter; the point is that a gap exists
	// at all, not its precise size.
	want := time.Duration(commands) * readGap / 2
	if elapsed < want {
		t.Errorf("read %d commands in %v, which is faster than pacing allows (%v): "+
			"the inter-request gap is missing and the base will be overrun",
			commands, elapsed, want)
	}
}
