package moza

import (
	"encoding/hex"
	"testing"
)

func TestParseProtocol(t *testing.T) {
	cases := map[string]Protocol{
		"new":  ProtocolNew,
		"NEW":  ProtocolNew,
		" new": ProtocolNew,
		"auto": ProtocolAuto,
		"AUTO": ProtocolAuto,
		"old":  ProtocolOld,
		"":     ProtocolOld,
		"junk": ProtocolOld,
	}
	for in, want := range cases {
		if got := ParseProtocol(in); got != want {
			t.Errorf("ParseProtocol(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestParseModelResponse uses the exact identity replies captured from the two
// rims: the new (ESX) rim answers on device 0x81 with "ES"; the legacy rim
// answers on device 0x71 (0x17) which must NOT be treated as new-protocol.
func TestParseModelResponse(t *testing.T) {
	es := mustHex(t, "7e11878101455300000000000000000000000000003d") // new rim "ES" on dev 0x18
	ks := mustHex(t, "7e118771014b53000000000000000000000000000033") // legacy rim "KS" on dev 0x17

	// 0x81 = device 0x18 (new); 0x71 = device 0x17 (legacy).
	if model, ok := parseModelResponse(es, 0x81); !ok || model != "ES" {
		t.Errorf("ES reply on 0x18: got (%q, %v), want (\"ES\", true)", model, ok)
	}
	if _, ok := parseModelResponse(ks, 0x81); ok {
		t.Error("legacy rim reply (device 0x17) must not match the new (0x81) probe")
	}
	if model, ok := parseModelResponse(ks, 0x71); !ok || model != "KS" {
		t.Errorf("KS reply on 0x17: got (%q, %v), want (\"KS\", true)", model, ok)
	}
	if _, ok := parseModelResponse([]byte{0x00, 0x11, 0x22}, 0x81); ok {
		t.Error("garbage must not match")
	}
	// Tolerate the response sitting mid-buffer behind other frames.
	noise := append([]byte{0x7e, 0x06, 0x41, 0x18, 0xfd, 0xde, 0, 0, 0, 0, 0xc5}, es...)
	if model, ok := parseModelResponse(noise, 0x81); !ok || model != "ES" {
		t.Errorf("framed reply: got (%q, %v), want (\"ES\", true)", model, ok)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

// TestSetLEDColorsNewMatchesCapture asserts the bulk colour command reproduces
// the exact bytes captured from Pit House (group 0x3f → device 0x18, cmd 0x19):
// LEDs 0-2 green, 3-9 red.
func TestSetLEDColorsNewMatchesCapture(t *testing.T) {
	green, red := RGB{0, 255, 0}, RGB{255, 0, 0}
	colors := [10]RGB{green, green, green, red, red, red, red, red, red, red}

	frames, err := setLEDColorsNew(colors)
	if err != nil {
		t.Fatalf("setLEDColorsNew: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(frames))
	}

	want := []string{
		"7e163f1819000000ff000100ff000200ff0003ff000004ff000016",
		"7e163f18190005ff000006ff000007ff000008ff000009ff00002f",
	}
	for i, f := range frames {
		if got := hex.EncodeToString(f); got != want[i] {
			t.Errorf("frame %d = %s, want %s", i, got, want[i])
		}
	}
}

// TestNewConfigFramesMatchCapture checks individual config-burst frames against
// the captured bytes (checksums included).
func TestNewConfigFramesMatchCapture(t *testing.T) {
	cases := []struct {
		name    string
		id      []uint8
		payload []uint8
		want    string
	}{
		{"channel-enable-6", []uint8{0x1e, 0x00}, []uint8{0x06, 0x00, 0x00}, "7e0540181e000600000c"},
		{"brightness", []uint8{0x1b, 0x00}, []uint8{0xff, 0x00, 0x00}, "7e0540181b00ff000002"},
		{"led-color-page-idx6-off", []uint8{0x1f, 0x00}, []uint8{0xff, 0x06, 0x00, 0x00, 0x00}, "7e0740181f00ff060000000e"},
		{"reg-2000", []uint8{0x20, 0x00}, []uint8{}, "7e024018200005"},
		{"reg-24ff", []uint8{0x24, 0xff}, []uint8{0x01, 0xff, 0x00, 0x00, 0x00}, "7e07401824ff01ff0000000d"},
	}
	for _, c := range cases {
		frame, err := newConfigWrite(c.id, c.payload)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := hex.EncodeToString(frame); got != c.want {
			t.Errorf("%s = %s, want %s", c.name, got, c.want)
		}
	}
}

// TestSetRPMMaskNewMatchesCapture pins the live rev-light mask frames to the
// bytes captured from the Pit House RPM sweep (group 0x41 → device 0x18, cmd
// 0xfdde, 32-bit big-endian mask).
func TestSetRPMMaskNewMatchesCapture(t *testing.T) {
	cases := []struct {
		mask uint32
		want string
	}{
		{0x00000000, "7e064118fdde00000000c5"},
		{0x00000001, "7e064118fdde00000001c6"},
		{0x0000007f, "7e064118fdde0000007f44"},
		{0x000003ff, "7e064118fdde000003ffc7"}, // 10 LEDs lit
	}
	for _, c := range cases {
		frame, err := setRPMMaskNew(c.mask)
		if err != nil {
			t.Fatalf("setRPMMaskNew(%#x): %v", c.mask, err)
		}
		if got := hex.EncodeToString(frame); got != c.want {
			t.Errorf("setRPMMaskNew(%#x) = %s, want %s", c.mask, got, c.want)
		}
	}
}

func TestRPMMaskValueRedline(t *testing.T) {
	// Below redline: cumulative lit mask.
	if got := rpmMaskValue(4000, 8000, 10, RPMCurve{}); got != 0x1f {
		t.Errorf("half RPM = %#x, want 0x1f", got)
	}
	// Full bar but not yet at the redline ratio.
	if got := rpmMaskValue(7800, 8000, 10, RPMCurve{}); got != 0x3ff {
		t.Errorf("near-full RPM = %#x, want 0x3ff (steady full bar)", got)
	}
	// At/above redline: the flash pattern.
	if got := rpmMaskValue(8000, 8000, 10, RPMCurve{}); got != redlineMask {
		t.Errorf("redline = %#x, want %#x", got, redlineMask)
	}
	// Engine off: no LEDs.
	if got := rpmMaskValue(0, 8000, 10, RPMCurve{}); got != 0 {
		t.Errorf("idle = %#x, want 0", got)
	}
}

func TestDefaultRPMGradientEndpoints(t *testing.T) {
	g := defaultRPMGradient()
	if g[0] != (RGB{0, 255, 0}) {
		t.Errorf("first LED = %v, want green", g[0])
	}
	if g[9] != (RGB{255, 0, 0}) {
		t.Errorf("last LED = %v, want red", g[9])
	}
	// Mid LED should be a bright yellow (both R and G high), not muddy olive.
	if mid := g[4]; mid.R < 200 || mid.G < 200 {
		t.Errorf("mid LED = %v, want bright yellow-ish", mid)
	}
}

func TestMaskForLit(t *testing.T) {
	cases := map[int]uint32{0: 0x0, 1: 0x1, 5: 0x1f, 10: 0x3ff}
	for lit, want := range cases {
		if got := maskForLit(lit); got != want {
			t.Errorf("maskForLit(%d) = %#x, want %#x", lit, got, want)
		}
	}
}

func TestEnsureRPMColorsFallsBackToGradient(t *testing.T) {
	if got := ensureRPMColors([10]RGB{}); got == ([10]RGB{}) {
		t.Error("expected gradient fallback for empty colours, got all-zero")
	}
	custom := [10]RGB{{1, 2, 3}}
	if got := ensureRPMColors(custom); got != custom {
		t.Error("configured colours should be preserved")
	}
}
