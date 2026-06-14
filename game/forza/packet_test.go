package forza

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestParseFH6Packet(t *testing.T) {
	want := Telemetry{
		IsRaceOn:         1,
		TimestampMS:      123456,
		EngineMaxRpm:     9000,
		EngineIdleRpm:    900,
		CurrentEngineRpm: 4521.5,
		Speed:            42.25,
		Power:            310000,
		Torque:           480.5,
		Boost:            12.5,
		Gear:             4,
		Accel:            200,
		Brake:            10,
		Clutch:           2,
		HandBrake:        1,
		Steer:            -12,
		LapNumber:        3,
		RacePosition:     2,
		PositionX:        100.5,
		PositionY:        7.25,
		PositionZ:        -20.75,
	}

	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, wirePacket{Telemetry: want}); err != nil {
		t.Fatalf("binary.Write: %v", err)
	}
	if buf.Len() != PacketSize {
		t.Fatalf("synthetic packet size = %d, want %d", buf.Len(), PacketSize)
	}

	got, err := ParseFH6Packet(buf.Bytes())
	if err != nil {
		t.Fatalf("ParseFH6Packet returned error: %v", err)
	}

	if got.TimestampMS != want.TimestampMS ||
		got.CurrentEngineRpm != want.CurrentEngineRpm ||
		got.Speed != want.Speed ||
		got.Power != want.Power ||
		got.Torque != want.Torque ||
		got.Boost != want.Boost ||
		got.Gear != want.Gear ||
		got.Accel != want.Accel ||
		got.Brake != want.Brake ||
		got.Clutch != want.Clutch ||
		got.HandBrake != want.HandBrake ||
		got.Steer != want.Steer ||
		got.LapNumber != want.LapNumber ||
		got.RacePosition != want.RacePosition ||
		got.PositionX != want.PositionX ||
		got.PositionY != want.PositionY ||
		got.PositionZ != want.PositionZ {
		t.Fatalf("parsed telemetry mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestParseFH6PacketRejectsShortPacket(t *testing.T) {
	_, err := ParseFH6Packet(make([]byte, PacketSize-1))
	assertPacketSizeError(t, err, PacketSize-1)
}

func TestParseFH6PacketRejectsOversizedPacket(t *testing.T) {
	_, err := ParseFH6Packet(make([]byte, PacketSize+1))
	assertPacketSizeError(t, err, PacketSize+1)
}

func assertPacketSizeError(t *testing.T, err error, gotSize int) {
	t.Helper()

	var sizeErr *PacketSizeError
	if !errors.As(err, &sizeErr) {
		t.Fatalf("error = %v, want PacketSizeError", err)
	}
	if sizeErr.Got != gotSize || sizeErr.Want != PacketSize {
		t.Fatalf("PacketSizeError = got %d want %d, expected got %d want %d", sizeErr.Got, sizeErr.Want, gotSize, PacketSize)
	}
}
