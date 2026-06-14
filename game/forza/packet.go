package forza

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const PacketSize = 324

type PacketSizeError struct {
	Got  int
	Want int
}

func (e *PacketSizeError) Error() string {
	return fmt.Sprintf("unexpected FH6 packet size: got %d bytes, want %d", e.Got, e.Want)
}

type Telemetry struct {
	IsRaceOn                             int32
	TimestampMS                          uint32
	EngineMaxRpm                         float32
	EngineIdleRpm                        float32
	CurrentEngineRpm                     float32
	AccelerationX                        float32
	AccelerationY                        float32
	AccelerationZ                        float32
	VelocityX                            float32
	VelocityY                            float32
	VelocityZ                            float32
	AngularVelocityX                     float32
	AngularVelocityY                     float32
	AngularVelocityZ                     float32
	Yaw                                  float32
	Pitch                                float32
	Roll                                 float32
	NormalizedSuspensionTravelFrontLeft  float32
	NormalizedSuspensionTravelFrontRight float32
	NormalizedSuspensionTravelRearLeft   float32
	NormalizedSuspensionTravelRearRight  float32
	TireSlipRatioFrontLeft               float32
	TireSlipRatioFrontRight              float32
	TireSlipRatioRearLeft                float32
	TireSlipRatioRearRight               float32
	WheelRotationSpeedFrontLeft          float32
	WheelRotationSpeedFrontRight         float32
	WheelRotationSpeedRearLeft           float32
	WheelRotationSpeedRearRight          float32
	WheelOnRumbleStripFrontLeft          int32
	WheelOnRumbleStripFrontRight         int32
	WheelOnRumbleStripRearLeft           int32
	WheelOnRumbleStripRearRight          int32
	WheelInPuddleFrontLeft               int32
	WheelInPuddleFrontRight              int32
	WheelInPuddleRearLeft                int32
	WheelInPuddleRearRight               int32
	SurfaceRumbleFrontLeft               float32
	SurfaceRumbleFrontRight              float32
	SurfaceRumbleRearLeft                float32
	SurfaceRumbleRearRight               float32
	TireSlipAngleFrontLeft               float32
	TireSlipAngleFrontRight              float32
	TireSlipAngleRearLeft                float32
	TireSlipAngleRearRight               float32
	TireCombinedSlipFrontLeft            float32
	TireCombinedSlipFrontRight           float32
	TireCombinedSlipRearLeft             float32
	TireCombinedSlipRearRight            float32
	SuspensionTravelMetersFrontLeft      float32
	SuspensionTravelMetersFrontRight     float32
	SuspensionTravelMetersRearLeft       float32
	SuspensionTravelMetersRearRight      float32
	CarOrdinal                           int32
	CarClass                             int32
	CarPerformanceIndex                  int32
	DrivetrainType                       int32
	NumCylinders                         int32
	CarGroup                             uint32
	SmashableVelDiff                     float32
	SmashableMass                        float32
	PositionX                            float32
	PositionY                            float32
	PositionZ                            float32
	Speed                                float32
	Power                                float32
	Torque                               float32
	TireTempFrontLeft                    float32
	TireTempFrontRight                   float32
	TireTempRearLeft                     float32
	TireTempRearRight                    float32
	Boost                                float32
	Fuel                                 float32
	DistanceTraveled                     float32
	BestLap                              float32
	LastLap                              float32
	CurrentLap                           float32
	CurrentRaceTime                      float32
	LapNumber                            uint16
	RacePosition                         uint8
	Accel                                uint8
	Brake                                uint8
	Clutch                               uint8
	HandBrake                            uint8
	Gear                                 uint8
	Steer                                int8
	NormalizedDrivingLine                int8
	NormalizedAIBrakeDifference          int8
}

type wirePacket struct {
	Telemetry
	Reserved uint8
}

func ParseFH6Packet(data []byte) (Telemetry, error) {
	if len(data) != PacketSize {
		return Telemetry{}, &PacketSizeError{Got: len(data), Want: PacketSize}
	}

	var packet wirePacket
	if err := binary.Read(bytes.NewReader(data), binary.LittleEndian, &packet); err != nil {
		return Telemetry{}, err
	}
	return packet.Telemetry, nil
}
