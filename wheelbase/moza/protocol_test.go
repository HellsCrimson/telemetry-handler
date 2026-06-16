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
			if got := rpmMask(tt.currentRPM, tt.maxRPM, 10); got != tt.want {
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
			if got := rpmMask(8000, 8000, tt.leds); got != tt.want {
				t.Fatalf("rpmMask(full, leds=%d) = %#06x, want %#06x", tt.leds, got, tt.want)
			}
		})
	}
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
