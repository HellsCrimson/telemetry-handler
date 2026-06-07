package overlay

import (
	"fmt"
	"math"
	"time"

	"telemetry-handler/forza"
)

type HUD struct {
	Connected    bool
	Stale        bool
	SpeedKPH     string
	Gear         string
	RPM          string
	MaxRPM       string
	RPMRatio     float64
	Throttle     float64
	Brake        float64
	Clutch       float64
	SteeringAngle int8
}

func FormatHUD(t forza.Telemetry, available bool, receivedAt time.Time, now time.Time) HUD {
	connected := available && !receivedAt.IsZero()
	stale := !connected || now.Sub(receivedAt) > staleAfter

	maxRPM := float64(t.EngineMaxRpm)
	currentRPM := float64(t.CurrentEngineRpm)

	return HUD{
		Connected:     connected,
		Stale:         stale,
		SpeedKPH:      fmt.Sprintf("%.0f", float64(t.Speed)*3.6),
		Gear:          fmt.Sprintf("%d", t.Gear),
		RPM:           fmt.Sprintf("%.0f", currentRPM),
		MaxRPM:        fmt.Sprintf("%.0f", maxRPM),
		RPMRatio:      clampRatio(currentRPM, maxRPM),
		Throttle:      pedalRatio(t.Accel),
		Brake:         pedalRatio(t.Brake),
		Clutch:        pedalRatio(t.Clutch),
		SteeringAngle: t.Steer,
	}
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
