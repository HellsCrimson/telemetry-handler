package moza

import "fmt"

const (
	messageStart = 0x7e
	magicValue   = 0x0d

	commandWrite = 63
	wheelDevice  = 23
)

type RGB struct {
	R uint8
	G uint8
	B uint8
}

func buildFrame(group uint8, device uint8, id []uint8, payload []uint8) ([]byte, error) {
	payloadLen := len(id) + len(payload)
	if payloadLen > 255 {
		return nil, fmt.Errorf("payload too large: %d bytes", payloadLen)
	}

	frame := make([]byte, 0, payloadLen+5)
	frame = append(frame, messageStart, uint8(payloadLen), group, device)
	frame = append(frame, id...)
	frame = append(frame, payload...)
	frame = append(frame, checksum(frame))
	return frame, nil
}

func checksum(frame []byte) uint8 {
	sum := magicValue
	for _, b := range frame {
		sum += int(b)
	}
	return uint8(sum % 256)
}

func wheelWrite(id []uint8, payload []uint8) ([]byte, error) {
	return buildFrame(commandWrite, wheelDevice, id, payload)
}

func setTelemetryMode(enabled bool) ([]byte, error) {
	value := uint8(0)
	if enabled {
		value = 1
	}
	return wheelWrite([]uint8{28, 0}, []uint8{value})
}

func setRPMBrightness(value uint8) ([]byte, error) {
	if value > 15 {
		value = 15
	}
	return wheelWrite([]uint8{27, 0, 255}, []uint8{value})
}

func setRPMTelemetryMask(mask uint16) ([]byte, error) {
	return wheelWrite([]uint8{26, 0}, []uint8{uint8(mask), uint8(mask >> 8)})
}

func setButtonTelemetryMask(mask uint16) ([]byte, error) {
	return wheelWrite([]uint8{26, 1}, []uint8{uint8(mask), uint8(mask >> 8)})
}

func setRPMTelemetryColors(colors [10]RGB) ([][]byte, error) {
	return setTelemetryColors([]uint8{25, 0}, colors)
}

func setButtonTelemetryColors(colors [10]RGB) ([][]byte, error) {
	return setTelemetryColors([]uint8{25, 1}, colors)
}

func setTelemetryColors(id []uint8, colors [10]RGB) ([][]byte, error) {
	payload := make([]uint8, 0, 40)
	for index, color := range colors {
		payload = append(payload, uint8(index), color.R, color.G, color.B)
	}

	first, err := wheelWrite(id, payload[:20])
	if err != nil {
		return nil, err
	}
	second, err := wheelWrite(id, payload[20:])
	if err != nil {
		return nil, err
	}
	return [][]byte{first, second}, nil
}
