//go:build linux || windows

package moza

import "time"

// detectProbeWindow bounds how long DetectWheel waits for the rim to answer the
// model-code queries before giving up.
const detectProbeWindow = 350 * time.Millisecond

// DetectWheel identifies the attached rim over serial. It asks both the new LED
// controller (device 0x18) and the legacy wheel device (0x17) for their model
// code; the rim answers on whichever it has. A reply from 0x18 means a
// new-protocol rim (ESX class); a reply from only 0x17 means a legacy rim. It
// returns the detected protocol and the model string ("ES", "KS", …). Any
// failure or silence falls back to (ProtocolOld, ""), so detection never blocks
// a legacy rim from working.
func DetectWheel(port string) (Protocol, string, error) {
	conn, err := openSerial(port)
	if err != nil {
		return ProtocolOld, "", err
	}
	defer conn.Close()

	// Send both model-code queries up front, then read one window for either
	// reply, so a legacy rim costs one short read rather than two.
	for _, dev := range []uint8{ledDevice, wheelDevice} {
		q, err := buildFrame(0x07, dev, []uint8{0x01}, nil)
		if err != nil {
			return ProtocolOld, "", err
		}
		if err := conn.WriteFrame(q); err != nil {
			return ProtocolOld, "", err
		}
	}

	deadline := time.Now().Add(detectProbeWindow)
	acc := make([]byte, 0, 256)
	buf := make([]byte, 128)
	var legacyModel string
	var haveLegacy bool
	for time.Now().Before(deadline) {
		n, err := conn.read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
			// A reply from device 0x18 (response byte 0x81) is decisive: new rim.
			if model, ok := parseModelResponse(acc, 0x81); ok {
				return ProtocolNew, model, nil
			}
			if model, ok := parseModelResponse(acc, 0x71); ok {
				legacyModel, haveLegacy = model, true
			}
		}
		if err != nil {
			break
		}
	}
	if haveLegacy {
		return ProtocolOld, legacyModel, nil
	}
	return ProtocolOld, "", nil
}

// ResolveWheel turns a configured protocol into the concrete protocol plus the
// rim model for display. ProtocolAuto triggers a DetectWheel probe and uses its
// result; an explicit ProtocolOld/New is honoured but the rim is still probed so
// the dashboard can name it. An empty port or probe failure yields the
// configured protocol (or ProtocolOld for auto) and an empty model.
func ResolveWheel(configured Protocol, port string) (Protocol, string) {
	if port == "" {
		if configured == ProtocolAuto {
			return ProtocolOld, ""
		}
		return configured, ""
	}
	detected, model, _ := DetectWheel(port)
	if configured == ProtocolAuto {
		return detected, model
	}
	return configured, model
}
