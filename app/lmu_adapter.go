package app

import (
	"hash/fnv"

	"telemetry-handler/forza"
	"telemetry-handler/lmu"
)

// lmuNeutralGear is the Forza gear value emitted for LMU's neutral. The overlay
// infers neutral as "the highest gear value seen" (Forza reports neutral as
// N+1), so we pick a sentinel above any real gearbox: the car starts in neutral
// (garage/grid) so the overlay learns it immediately, and real forward gears
// (≤ ~9) never collide with it — which matters because LMU's sequential boxes
// don't pass through neutral on every shift.
const lmuNeutralGear = 15

// decodePacket converts a raw telemetry packet — as received over UDP or read
// back from a recording — into the canonical forza.Telemetry model plus its
// source ("forza"/"lmu") and descriptive meta. It demultiplexes Forza's binary
// packets from the lmu-bridge's JSON by content, so the live receiver and
// recording replay decode both games identically.
func decodePacket(packet []byte) (forza.Telemetry, string, TelemetryMeta, error) {
	if lmu.LooksLikePacket(packet) {
		p, err := lmu.Parse(packet)
		if err != nil {
			return forza.Telemetry{}, "lmu", TelemetryMeta{}, err
		}
		return lmuToTelemetry(p), "lmu", lmuToMeta(p), nil
	}
	t, err := forza.ParseFH6Packet(packet)
	if err != nil {
		return forza.Telemetry{}, "forza", TelemetryMeta{}, err
	}
	return t, "forza", TelemetryMeta{}, nil
}

// lmuToTelemetry maps an lmu-bridge packet into the app's forza.Telemetry model
// so the overlay, MOZA lighting, dashboard and terminal output consume LMU
// exactly like Forza, with no downstream changes. It is stateless.
func lmuToTelemetry(p lmu.Packet) forza.Telemetry {
	return forza.Telemetry{
		// LMU sends data whenever the sim is publishing; treat it as race-on so
		// MOZA RPM tracks. With the engine off (garage) RPM is 0, so lights stay
		// off anyway.
		IsRaceOn:         1,
		CurrentEngineRpm: float32(p.EngineRPM),
		EngineMaxRpm:     float32(p.EngineMaxRPM),
		Speed:            float32(p.SpeedMS),
		Gear:             lmuGear(p.Gear),
		Accel:            unitToByte(p.Throttle),
		Brake:            unitToByte(p.Brake),
		Clutch:           unitToByte(p.Clutch),
		Steer:            unitToSteer(p.Steering),
		Fuel:             float32(p.Fuel),
		LapNumber:        uint16(max(p.LapNumber, 0)),
		CarOrdinal:       carOrdinal(p.VehicleName),
	}
}

// lmuToMeta extracts the descriptive session info an lmu-bridge packet carries
// (car/track names, session time, field size) that does not fit the binary
// forza.Telemetry model. Surfaced on the dashboard's Info tab.
func lmuToMeta(p lmu.Packet) TelemetryMeta {
	return TelemetryMeta{
		Car:              p.VehicleName,
		Track:            p.TrackName,
		SessionTime:      p.ElapsedTime,
		NumVehicles:      int(p.NumVehicles),
		SteeringRangeDeg: p.SteeringRange,
	}
}

// lmuGear converts an LMU gear (-1=reverse, 0=neutral, 1+=forward) into Forza's
// encoding the overlay expects (0=reverse, neutral as the top-of-range value).
func lmuGear(g int32) uint8 {
	switch {
	case g < 0:
		return 0 // reverse -> "R"
	case g == 0:
		return lmuNeutralGear // neutral -> "N" (see lmuNeutralGear)
	default:
		return uint8(min(g, 240)) // forward gear
	}
}

// unitToByte maps a 0..1 ratio to Forza's 0..255 pedal byte.
func unitToByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v * 255)
}

// unitToSteer maps a -1..1 ratio to Forza's -127..127 int8 steering.
func unitToSteer(v float64) int8 {
	if v <= -1 {
		return -127
	}
	if v >= 1 {
		return 127
	}
	return int8(v * 127)
}

// carOrdinal derives a stable per-car id from the vehicle name so the overlay's
// per-car gear tracking resets when the car changes (Forza keys on CarOrdinal).
func carOrdinal(name string) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int32(h.Sum32() & 0x7fffffff)
}
