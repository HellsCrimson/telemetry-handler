package output

import (
	"fmt"

	"telemetry-handler/forza"
)

type Formatter struct{}

func NewFormatter() Formatter {
	return Formatter{}
}

func (Formatter) Format(t forza.Telemetry) string {
	raceState := "off"
	if t.IsRaceOn == 1 {
		raceState = "on"
	}

	return fmt.Sprintf(
		"race=%s t=%dms speed=%.1fkm/h rpm=%.0f/%.0f gear=%d throttle=%d brake=%d clutch=%d handbrake=%d steer=%d power=%.0fkW torque=%.0fNm boost=%.1fpsi tireC=%.0f/%.0f/%.0f/%.0f pos=%.1f,%.1f,%.1f",
		raceState,
		t.TimestampMS,
		t.Speed*3.6,
		t.CurrentEngineRpm,
		t.EngineMaxRpm,
		t.Gear,
		t.Accel,
		t.Brake,
		t.Clutch,
		t.HandBrake,
		t.Steer,
		t.Power/1000,
		t.Torque,
		t.Boost,
		t.TireTempFrontLeft,
		t.TireTempFrontRight,
		t.TireTempRearLeft,
		t.TireTempRearRight,
		t.PositionX,
		t.PositionY,
		t.PositionZ,
	)
}
