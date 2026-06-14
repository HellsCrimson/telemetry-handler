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

func rpmMask(currentRPM, maxRPM float32) uint16 {
	if currentRPM <= 0 || maxRPM <= 0 {
		return 0
	}

	ratio := currentRPM / maxRPM
	if ratio <= 0 {
		return 0
	}
	if ratio > 1 {
		ratio = 1
	}

	lit := int(ratio * 10)
	if lit < 1 {
		lit = 1
	}
	if lit > 10 {
		lit = 10
	}

	return uint16((1 << lit) - 1)
}
