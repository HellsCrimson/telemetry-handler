package overlay

import (
	"fmt"
	"math"
	"time"

	"telemetry-handler/forza"
)

// pedalHistorySeconds is how many seconds of throttle/brake history the overlay
// graph shows.
const pedalHistorySeconds = 4.0

type HUD struct {
	Connected       bool
	Stale           bool
	SpeedKPH        string
	Gear            string
	RPM             string
	MaxRPM          string
	RPMRatio        float64
	Throttle        float64
	Brake           float64
	Clutch          float64
	HandBrake       float64
	ThrottleHistory []float64
	BrakeHistory    []float64
	HistoryCap      int
	SteeringAngle   int8
}

// FormatHUD builds the instantaneous HUD view model from a telemetry sample. It
// is stateless; the rolling pedal history and gear-neutral inference are layered
// on by hudHistory.build (which calls this).
func FormatHUD(t forza.Telemetry, available bool, receivedAt time.Time, now time.Time) HUD {
	connected := available && !receivedAt.IsZero()
	stale := !connected || now.Sub(receivedAt) > staleAfter

	maxRPM := float64(t.EngineMaxRpm)
	currentRPM := float64(t.CurrentEngineRpm)

	return HUD{
		Connected:     connected,
		Stale:         stale,
		SpeedKPH:      fmt.Sprintf("%.0f", float64(t.Speed)*3.6),
		Gear:          gearLabel(t.Gear, 0),
		RPM:           fmt.Sprintf("%.0f", currentRPM),
		MaxRPM:        fmt.Sprintf("%.0f", maxRPM),
		RPMRatio:      clampRatio(currentRPM, maxRPM),
		Throttle:      pedalRatio(t.Accel),
		Brake:         pedalRatio(t.Brake),
		Clutch:        pedalRatio(t.Clutch),
		HandBrake:     pedalRatio(t.HandBrake),
		SteeringAngle: t.Steer,
	}
}

// gearLabel renders the gear value for display. Forza reports 0 for reverse and
// 1..N for forward gears; neutral is reported as N+1 (one past the car's top
// gear). neutralGear is the inferred N+1 value (0 when unknown), so neutral is
// shown as "N" instead of a non-existent top gear.
func gearLabel(gear, neutralGear uint8) string {
	switch {
	case gear == 0:
		return "R"
	case neutralGear != 0 && gear == neutralGear:
		return "N"
	default:
		return fmt.Sprintf("G%d", gear)
	}
}

// hudHistory carries the per-session state the stateless FormatHUD cannot: a
// rolling buffer of recent throttle/brake values for the graph, and the
// highest gear value seen (per car) used to identify the neutral position.
type hudHistory struct {
	cap      int
	throttle []float64
	brake    []float64

	hasCar  bool
	lastCar int32
	maxGear uint8
}

func newHUDHistory(updateHz float64) *hudHistory {
	if updateHz <= 0 {
		updateHz = 10
	}
	n := int(updateHz * pedalHistorySeconds)
	if n < 8 {
		n = 8
	}
	if n > 600 {
		n = 600
	}
	return &hudHistory{cap: n}
}

func (h *hudHistory) build(t forza.Telemetry, available bool, receivedAt, now time.Time) HUD {
	hud := FormatHUD(t, available, receivedAt, now)

	throttle, brake := hud.Throttle, hud.Brake
	if !hud.Connected {
		throttle, brake = 0, 0
	}
	h.throttle = appendCapped(h.throttle, throttle, h.cap)
	h.brake = appendCapped(h.brake, brake, h.cap)
	hud.ThrottleHistory = h.throttle
	hud.BrakeHistory = h.brake
	hud.HistoryCap = h.cap

	if available {
		if !h.hasCar || t.CarOrdinal != h.lastCar {
			h.hasCar = true
			h.lastCar = t.CarOrdinal
			h.maxGear = 0
		}
		// Ignore obviously bogus values; the neutral blip (N+1) becomes the max
		// after the first upshift, which is exactly the value we want.
		if t.Gear > h.maxGear && t.Gear < 250 {
			h.maxGear = t.Gear
		}
	}
	neutral := uint8(0)
	if h.maxGear >= 2 { // guard single-speed cars from labelling gear 1 as neutral
		neutral = h.maxGear
	}
	hud.Gear = gearLabel(t.Gear, neutral)

	return hud
}

func appendCapped(buf []float64, v float64, capacity int) []float64 {
	buf = append(buf, v)
	if len(buf) > capacity {
		buf = buf[len(buf)-capacity:]
	}
	return buf
}

func clampRatio(value, max float64) float64 {
	if max <= 0 || math.IsNaN(value) || math.IsNaN(max) {
		return 0
	}
	ratio := value / max
	if ratio < 0 {
		return 0
	}
	if ratio > 1 {
		return 1
	}
	return ratio
}

func pedalRatio(value uint8) float64 {
	return float64(value) / 255
}
