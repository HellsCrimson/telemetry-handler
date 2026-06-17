package moza

import (
	"math"
	"strings"
)

// Protocol selects which MOZA rim LED protocol the driver speaks.
//
// MOZA ships two incompatible rev-light protocols. Older rims (e.g. the ES
// series) accept the "telemetry" command set on group 0x3f / device 0x17
// (wheelDevice): enable telemetry mode, push 10 colours, then stream a lit
// segment bitmask (see protocol.go). Newer rims (ESX, KS Pro, CS Pro, GS,
// FSR V2) ignore that path entirely — their LEDs live on a separate device
// (0x18) and stay in the idle "breathing" animation until they receive a
// group-0x40 channel-config burst, after which colours are pushed per-LED. The
// rim is not identifiable over USB (only the base is), so the protocol is a
// config choice (config.Moza.Protocol) rather than something we can detect.
type Protocol int

const (
	// ProtocolOld is the legacy telemetry-mask protocol (default, unchanged).
	ProtocolOld Protocol = iota
	// ProtocolNew is the channel-config + per-LED-colour protocol used by newer
	// rims such as the ESX.
	ProtocolNew
	// ProtocolAuto asks the caller to detect the rim's protocol at connect time
	// (DetectWheel). It is resolved to ProtocolOld/ProtocolNew before a Driver
	// is built, so a Driver never sees it (and treats it as ProtocolOld if it
	// somehow does).
	ProtocolAuto
)

// ParseProtocol maps a config string onto a Protocol: "new", "auto", or
// (anything else) the legacy protocol, so existing configs and the known-good
// rim are unaffected.
func ParseProtocol(s string) Protocol {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "new":
		return ProtocolNew
	case "auto":
		return ProtocolAuto
	default:
		return ProtocolOld
	}
}

// parseModelResponse scans a serial read buffer for a model-code reply (group
// 0x87 / the given response device / cmd 0x01) and returns the ASCII model
// string. respDev is the queried device's nibble-swapped form: 0x81 for the new
// LED controller (device 0x18), 0x71 for the legacy wheel (0x17). The bool is
// false when no matching reply is present.
func parseModelResponse(buf []byte, respDev byte) (string, bool) {
	for i := 0; i+5 <= len(buf); i++ {
		if buf[i] != 0x7e || buf[i+2] != 0x87 || buf[i+3] != respDev || buf[i+4] != 0x01 {
			continue
		}
		var model []byte
		for j := i + 5; j < len(buf) && buf[j] != 0x00; j++ {
			if buf[j] < 0x20 || buf[j] > 0x7e {
				break
			}
			model = append(model, buf[j])
		}
		// device 0x18 answered → new-protocol rim, whether or not the model parsed.
		return string(model), true
	}
	return "", false
}

const (
	// configGroup is the group byte for new-protocol configuration writes (the
	// channel-config burst). The legacy protocol uses commandWrite (0x3f); the
	// new rim's config device answers on 0x40.
	configGroup = 0x40
	// rpmMaskGroup carries the live rev-light bitmask (command 0xfdde). Captured
	// from Pit House as group 0x41 → device 0x18.
	rpmMaskGroup = 0x41
	// ledDevice is the new-protocol rim LED controller, distinct from the legacy
	// wheelDevice (0x17). All new-protocol LED traffic is addressed here.
	ledDevice = 0x18
)

// setRPMMaskNew builds the live rev-light frame: a 32-bit big-endian bitmask
// where bit i lights LED i, sent on group 0x41 / device 0x18 / command 0xfdde.
// This is the new-protocol analogue of setRPMTelemetryMask; the colours come
// from the palette loaded at setup (setLEDColorsNew). Verified byte-for-byte
// against the captured Pit House sweep (mask 0x3ff = 10 LEDs lit).
func setRPMMaskNew(mask uint32) ([]byte, error) {
	return buildFrame(rpmMaskGroup, ledDevice, []uint8{0xfd, 0xde}, []uint8{
		uint8(mask >> 24), uint8(mask >> 16), uint8(mask >> 8), uint8(mask),
	})
}

// maskForLit returns the cumulative bitmask lighting the first lit LEDs.
func maskForLit(lit int) uint32 {
	if lit <= 0 {
		return 0
	}
	return uint32(1)<<uint(lit) - 1
}

const (
	// redlineRatio is the RPM fraction at/above which the rev bar switches to the
	// redline flash instead of a steady full bar.
	redlineRatio = 0.985
	// redlineMask is the exact pattern Pit House sends at the limiter (captured
	// from the ESX sweep): all 10 LEDs lit plus the high "flash" bits the rim
	// firmware blinks on its own.
	redlineMask uint32 = 0xffff83ff
)

// rpmMaskValue maps the current/max RPM onto the live rev-light bitmask. Past
// redlineRatio it returns the redline flash pattern; below it, the bar fills
// proportionally — scaled so all LEDs are lit by the time RPM reaches the
// redline (the bar shows a full steady bar just before it starts flashing).
func rpmMaskValue(currentRPM, maxRPM float32, leds int, curve RPMCurve) uint32 {
	if currentRPM <= 0 || maxRPM <= 0 {
		return 0
	}
	ratio := float64(currentRPM) / float64(maxRPM)
	if ratio >= redlineRatio {
		return redlineMask
	}
	leds = clampRPMLEDs(leds)
	// Position within the working band (0..redline), reshaped by the curve so the
	// lower LEDs can hold across a wider RPM range.
	pos := curve.apply(ratio / redlineRatio)
	lit := int(pos*float64(leds) + 0.5)
	return maskForLit(min(max(lit, 1), leds))
}

// newConfigWrite builds a configuration frame for the new-protocol LED device
// (group 0x40 → device 0x18).
func newConfigWrite(id []uint8, payload []uint8) ([]byte, error) {
	return buildFrame(configGroup, ledDevice, id, payload)
}

// setupTelemetryFramesNew reproduces the channel-config burst MOZA Pit House
// sends to a new-protocol rim on connection. Without it the rim never leaves
// its idle breathing animation and discards LED writes. The exact registers and
// payloads were captured from Pit House driving an ESX rim (group 0x40 →
// device 0x18); the mystery mode registers (0x1c/0x1d/0x20/0x21/0x22/0x24) are
// replayed verbatim because their semantics are undocumented but they precede
// every working LED update in the capture. It loads the rev-light colour palette
// (the persistent colours the live mask lights) and starts with the mask off.
func setupTelemetryFramesNew(brightness uint8, colors [10]RGB) ([][]byte, error) {
	frames := make([][]byte, 0, 16)

	add := func(id, payload []uint8) error {
		f, err := newConfigWrite(id, payload)
		if err != nil {
			return err
		}
		frames = append(frames, f)
		return nil
	}

	// Channel enables 0x02..0x06 — the burst that takes the rim out of breathing
	// mode and makes it accept live LED data.
	for _, ch := range []uint8{0x02, 0x03, 0x04, 0x05, 0x06} {
		if err := add([]uint8{0x1e, 0x00}, []uint8{ch, 0x00, 0x00}); err != nil {
			return nil, err
		}
	}
	// Brightness (0..255 on this device; the config value is 0..15).
	if err := add([]uint8{0x1b, 0x00}, []uint8{newBrightness(brightness), 0x00, 0x00}); err != nil {
		return nil, err
	}
	// Mode/config registers replayed verbatim from the capture.
	for _, frame := range [][2][]uint8{
		{{0x1c, 0x00}, {0x00}},
		{{0x1d, 0x00}, {0x00}},
		{{0x20, 0x00}, {}},
		{{0x21, 0x00}, {0x00}},
		{{0x22, 0x01}, {0x00, 0x00}},
		{{0x24, 0xff}, {0x01, 0xff, 0x00, 0x00, 0x00}},
	} {
		if err := add(frame[0], frame[1]); err != nil {
			return nil, err
		}
	}

	// Load the rev-light colour palette (group 0x3f / cmd 0x19). The live mask
	// (setRPMMaskNew) lights the first N LEDs using these colours.
	palette, err := setLEDColorsNew(colors)
	if err != nil {
		return nil, err
	}
	frames = append(frames, palette...)

	// Start with no LEDs lit.
	off, err := setRPMMaskNew(0)
	if err != nil {
		return nil, err
	}
	frames = append(frames, off)
	return frames, nil
}

// setupFramesFor builds the connection-time setup burst for the selected
// protocol: the legacy telemetry-mode setup for ProtocolOld, or the
// channel-config burst for ProtocolNew.
func setupFramesFor(options Options) ([][]byte, error) {
	if options.Protocol == ProtocolNew {
		return setupTelemetryFramesNew(options.RPMBrightness, ensureRPMColors(options.RPMColors))
	}
	return setupTelemetryFrames(options.RPMBrightness, options.RPMColors, options.ButtonColors, options.ButtonMask)
}

// setLEDColorsNew pushes all 10 rim LED colours via the bulk colour command
// (group 0x3f → device 0x18, command 0x19 0x00). The payload is a sequence of
// [index, R, G, B] quads; the rim takes five per frame, so the bar is sent as
// two frames (LEDs 0..4 and 5..9), matching the captured Pit House traffic.
func setLEDColorsNew(colors [10]RGB) ([][]byte, error) {
	id := []uint8{0x19, 0x00}

	quad := func(start int) []uint8 {
		payload := make([]uint8, 0, 20)
		for i := start; i < start+5; i++ {
			payload = append(payload, uint8(i), colors[i].R, colors[i].G, colors[i].B)
		}
		return payload
	}

	first, err := buildFrame(commandWrite, ledDevice, id, quad(0))
	if err != nil {
		return nil, err
	}
	second, err := buildFrame(commandWrite, ledDevice, id, quad(5))
	if err != nil {
		return nil, err
	}
	return [][]byte{first, second}, nil
}

// newBrightness scales the 0..15 config brightness onto the new device's
// 0..255 range, defaulting to full brightness when unset so a runtime-toggled
// rim is never invisible.
func newBrightness(b uint8) uint8 {
	if b == 0 {
		return 0xff
	}
	b = min(b, 15)
	return uint8(uint16(b) * 0xff / 15)
}

// ensureRPMColors falls back to a green→red rev-light gradient when no colours
// are configured, so the new-protocol rim shows a sensible bar out of the box.
func ensureRPMColors(c [10]RGB) [10]RGB {
	for _, col := range c {
		if col.R != 0 || col.G != 0 || col.B != 0 {
			return c
		}
	}
	return defaultRPMGradient()
}

// defaultRPMGradient is a bright green→yellow→red rev-light ramp across the 10
// LEDs. It interpolates hue (green 120° → red 0°) at full saturation/value so the
// mid-range is a clean yellow rather than the muddy olive a linear RGB lerp
// produces.
func defaultRPMGradient() [10]RGB {
	var g [10]RGB
	for i := range g {
		t := float64(i) / float64(len(g)-1)
		g[i] = hsv(120*(1-t), 1, 1)
	}
	return g
}

// hsv converts an HSV colour (hue in degrees [0,360), saturation/value in [0,1])
// to RGB. Used to build smooth, vivid rev-light gradients.
func hsv(h, s, v float64) RGB {
	c := v * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := v - c
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return RGB{R: uint8((r + m) * 255), G: uint8((g + m) * 255), B: uint8((b + m) * 255)}
}
