package moza

import "testing"

func TestBuildFrameMatchesBoxflatProtocolShape(t *testing.T) {
	got, err := setRPMTelemetryMask(0x0201)
	if err != nil {
		t.Fatalf("setRPMTelemetryMask returned error: %v", err)
	}

	want := []uint8{0x7e, 0x04, 0x3f, 0x17, 0x1a, 0x00, 0x01, 0x02, 0x02}
	if string(got) != string(want) {
		t.Fatalf("frame = %#v, want %#v", got, want)
	}
}

func TestSetRPMTelemetryColorsSplitsIndexedColors(t *testing.T) {
	colors := [10]RGB{}
	for i := range colors {
		colors[i] = RGB{R: uint8(i), G: uint8(i + 1), B: uint8(i + 2)}
	}

	frames, err := setRPMTelemetryColors(colors)
	if err != nil {
		t.Fatalf("setRPMTelemetryColors returned error: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("len(frames) = %d, want 2", len(frames))
	}
	if frames[0][1] != 22 || frames[1][1] != 22 {
		t.Fatalf("payload lengths = %d/%d, want 22/22", frames[0][1], frames[1][1])
	}
	if frames[0][4] != 25 || frames[0][5] != 0 || frames[0][6] != 0 {
		t.Fatalf("first frame does not start with telemetry color command: %#v", frames[0])
	}
	if frames[1][6] != 5 {
		t.Fatalf("second frame first color index = %d, want 5", frames[1][6])
	}
}

func TestRPMMask(t *testing.T) {
	tests := []struct {
		name       string
		currentRPM float32
		maxRPM     float32
		want       uint16
	}{
		{name: "zero", currentRPM: 0, maxRPM: 8000, want: 0},
		{name: "one led", currentRPM: 400, maxRPM: 8000, want: 0x0001},
		{name: "half", currentRPM: 4000, maxRPM: 8000, want: 0x001f},
		{name: "full", currentRPM: 8000, maxRPM: 8000, want: 0x03ff},
		{name: "clamped", currentRPM: 9000, maxRPM: 8000, want: 0x03ff},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rpmMask(tt.currentRPM, tt.maxRPM, 10, RPMCurve{}); got != tt.want {
				t.Fatalf("rpmMask() = %#04x, want %#04x", got, tt.want)
			}
		})
	}
}

func TestRPMMaskLEDCount(t *testing.T) {
	// A different LED count rescales the bar: full RPM lights every segment, and
	// a zero/oversized count clamps back into the addressable range.
	tests := []struct {
		name string
		leds int
		want uint16
	}{
		{name: "five leds full", leds: 5, want: 0x001f},
		{name: "sixteen leds full", leds: 16, want: 0xffff},
		{name: "zero falls back to default ten", leds: 0, want: 0x03ff},
		{name: "over max clamps to sixteen", leds: 99, want: 0xffff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rpmMask(8000, 8000, tt.leds, RPMCurve{}); got != tt.want {
				t.Fatalf("rpmMask(full, leds=%d) = %#06x, want %#06x", tt.leds, got, tt.want)
			}
		})
	}
}

func TestRPMMaskCurve(t *testing.T) {
	// A curve that bows below the diagonal (output < input in the mid range)
	// holds the lower LEDs across a wider RPM band: at 70% RPM it lights fewer
	// segments than the linear bar, while both agree at the 0% and 100%
	// endpoints.
	exp := RPMCurve{Points: []CurvePoint{{0, 0}, {0.5, 0.2}, {1, 1}}}

	linearMid := rpmMask(5600, 8000, 10, RPMCurve{}) // 70% linear
	expMid := rpmMask(5600, 8000, 10, exp)           // 70% curved
	if popcount(expMid) >= popcount(linearMid) {
		t.Fatalf("curve lit %d LEDs at 70%%, want fewer than linear's %d",
			popcount(expMid), popcount(linearMid))
	}

	if got := rpmMask(8000, 8000, 10, exp); got != 0x03ff {
		t.Fatalf("curve full RPM = %#06x, want full bar 0x03ff", got)
	}
	if got := rpmMask(0, 8000, 10, exp); got != 0 {
		t.Fatalf("curve idle = %#06x, want 0", got)
	}
}

func TestRPMCurveApply(t *testing.T) {
	// Endpoints, monotonicity and clamping of the spline.
	c := RPMCurve{Points: []CurvePoint{{0, 0}, {0.5, 0.2}, {1, 1}}}
	if got := c.apply(0); got != 0 {
		t.Fatalf("apply(0) = %v, want 0", got)
	}
	if got := c.apply(1); got != 1 {
		t.Fatalf("apply(1) = %v, want 1", got)
	}
	if got := c.apply(0.5); got < 0.19 || got > 0.21 {
		t.Fatalf("apply(0.5) = %v, want ~0.2 (passes through control point)", got)
	}
	prev := -1.0
	for i := 0; i <= 20; i++ {
		x := float64(i) / 20
		y := c.apply(x)
		if y < 0 || y > 1 {
			t.Fatalf("apply(%v) = %v out of [0,1]", x, y)
		}
		if y < prev-1e-9 {
			t.Fatalf("curve not monotone at x=%v: %v < %v", x, y, prev)
		}
		prev = y
	}
	// A flat-topped curve: the bar saturates before max RPM (last point x<1).
	early := RPMCurve{Points: []CurvePoint{{0, 0}, {0.8, 1}}}
	if got := early.apply(0.9); got != 1 {
		t.Fatalf("apply(0.9) past last point = %v, want held at 1", got)
	}
	// Empty curve is the identity.
	if got := (RPMCurve{}).apply(0.42); got != 0.42 {
		t.Fatalf("empty curve apply(0.42) = %v, want identity 0.42", got)
	}
}

func popcount(v uint16) int {
	n := 0
	for v != 0 {
		n += int(v & 1)
		v >>= 1
	}
	return n
}

func TestProfileFor(t *testing.T) {
	if p := ProfileFor(0x0006, "MOZA R12 Base"); p.RPMLEDs != 10 {
		t.Fatalf("R12 profile RPMLEDs = %d, want 10", p.RPMLEDs)
	}
	// An unknown product ID falls back to the default profile but keeps the
	// reported product string so the UI can still name the device.
	p := ProfileFor(0xabcd, "MOZA RX Base")
	if p.RPMLEDs != defaultRPMLEDs {
		t.Fatalf("unknown profile RPMLEDs = %d, want %d", p.RPMLEDs, defaultRPMLEDs)
	}
	if p.Model != "MOZA RX Base" {
		t.Fatalf("unknown profile Model = %q, want %q", p.Model, "MOZA RX Base")
	}
}
