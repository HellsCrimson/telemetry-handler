package moza

func setupLightTestFrames(colors [10]RGB) ([][]byte, error) {
	return setupTelemetryFrames(15, colors, colors, 0x03ff)
}

func setupTelemetryFrames(brightness uint8, rpmColors [10]RGB, buttonColors [10]RGB, buttonMask uint16) ([][]byte, error) {
	frames := make([][]byte, 0, 5)

	mode, err := setTelemetryMode(true)
	if err != nil {
		return nil, err
	}
	frames = append(frames, mode)

	brightnessFrame, err := setRPMBrightness(brightness)
	if err != nil {
		return nil, err
	}
	frames = append(frames, brightnessFrame)

	colorFrames, err := setRPMTelemetryColors(rpmColors)
	if err != nil {
		return nil, err
	}
	frames = append(frames, colorFrames...)

	buttonColorFrames, err := setButtonTelemetryColors(buttonColors)
	if err != nil {
		return nil, err
	}
	frames = append(frames, buttonColorFrames...)

	off, err := setRPMTelemetryMask(0)
	if err != nil {
		return nil, err
	}
	frames = append(frames, off)

	buttons, err := setButtonTelemetryMask(buttonMask)
	if err != nil {
		return nil, err
	}
	frames = append(frames, buttons)

	return frames, nil
}

// rpmMask maps the current/max RPM ratio onto a bitmask of lit rev-light
// segments. leds is the wheel's RPM LED count (see Profile.RPMLEDs); it is
// clamped into the addressable range so an unset or out-of-range value falls
// back to the default segment count. curve reshapes the ratio before it is
// mapped onto the bar (e.g. to hold the green LEDs across a wider RPM band).
func rpmMask(currentRPM, maxRPM float32, leds int, curve RPMCurve) uint16 {
	leds = clampRPMLEDs(leds)

	if currentRPM <= 0 || maxRPM <= 0 {
		return 0
	}

	ratio := float64(currentRPM) / float64(maxRPM)
	if ratio <= 0 {
		return 0
	}
	if ratio > 1 {
		ratio = 1
	}
	ratio = curve.apply(ratio)

	lit := int(ratio * float64(leds))
	if lit < 1 {
		lit = 1
	}
	if lit > leds {
		lit = leds
	}

	return uint16((1 << lit) - 1)
}
