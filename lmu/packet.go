// Package lmu decodes the JSON telemetry packets emitted by the lmu-bridge
// sidecar (see sidecar/). The sidecar reads Le Mans Ultimate / rFactor 2 shared
// memory under Wine and forwards a small JSON datagram over UDP; this package
// parses it. It is intentionally decoupled from the rest of the app (no forza
// import) — the mapping into the app's telemetry model lives in package app.
package lmu

import (
	"encoding/json"
	"fmt"
)

// Packet is the lmu-bridge UDP wire format. Field names/tags must match the
// sidecar's packet struct in sidecar/main.go.
type Packet struct {
	Source       string  `json:"source"` // always "lmu"
	Seq          uint64  `json:"seq"`
	Version      uint32  `json:"version"`
	NumVehicles  int32   `json:"num_vehicles"`
	VehicleName  string  `json:"vehicle_name"`
	TrackName    string  `json:"track_name"`
	ElapsedTime  float64 `json:"elapsed_time"`
	LapNumber    int32   `json:"lap_number"`
	Gear         int32   `json:"gear"` // -1=reverse, 0=neutral, 1+=forward
	EngineRPM    float64 `json:"engine_rpm"`
	EngineMaxRPM float64 `json:"engine_max_rpm"`
	SpeedMS      float64 `json:"speed_ms"`
	Throttle     float64 `json:"throttle"` // 0..1
	Brake        float64 `json:"brake"`    // 0..1
	Steering     float64 `json:"steering"` // -1..1
	Clutch       float64 `json:"clutch"`   // 0..1
	Fuel         float64 `json:"fuel"`     // liters
}

// LooksLikePacket cheaply detects an lmu-bridge datagram so a single UDP
// receiver can demultiplex it from Forza's fixed-size binary packets: JSON
// always starts with '{'.
func LooksLikePacket(data []byte) bool {
	return len(data) > 0 && data[0] == '{'
}

// Parse decodes an lmu-bridge datagram and verifies it is one of ours.
func Parse(data []byte) (Packet, error) {
	var p Packet
	if err := json.Unmarshal(data, &p); err != nil {
		return Packet{}, err
	}
	if p.Source != "lmu" {
		return Packet{}, fmt.Errorf("not an lmu packet (source=%q)", p.Source)
	}
	return p, nil
}
