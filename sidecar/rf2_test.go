package main

import (
	"bytes"
	"encoding/binary"
	"testing"

	"telemetry-handler/game/lmu/wire"
)

// TestRF2ExtendedSize pins the hand-mirrored rF2Extended layout to its computed
// pack(4) size. If a sub-struct or pad is wrong the deep fields (pit speed
// limit, subscription mask) would read garbage — fail here instead.
func TestRF2ExtendedSize(t *testing.T) {
	if got := binary.Size(rf2Extended{}); got != rf2ExtendedSize {
		t.Fatalf("sizeof(rf2Extended) = %d, want %d", got, rf2ExtendedSize)
	}
	if got := binary.Size(rf2PhysicsOptions{}); got != 40 {
		t.Errorf("sizeof(rf2PhysicsOptions) = %d, want 40", got)
	}
	if got := binary.Size(rf2SessionTransitionCapture{}); got != 1036 {
		t.Errorf("sizeof(rf2SessionTransitionCapture) = %d, want 1036", got)
	}
	if got := binary.Size(rf2VehScoringCapture{}); got != 8 {
		t.Errorf("sizeof(rf2VehScoringCapture) = %d, want 8", got)
	}
}

// TestBuildFrameFromSyntheticBuffers writes two telemetry vehicles and matching
// scoring rows into byte buffers laid out exactly like the rF2 shared memory,
// then checks buildFrame pairs them, finds the player, and reads a global.
func TestBuildFrameFromSyntheticBuffers(t *testing.T) {
	telBuf := make([]byte, telemetryWindow)
	scoBuf := make([]byte, scoringWindow)

	// Telemetry: 2 vehicles, IDs 10 and 20.
	binary.LittleEndian.PutUint32(telBuf[telNumVehOff:], 2)
	writeTelem(telBuf, 0, 10, "Player Car", 5, 9000)
	writeTelem(telBuf, 1, 20, "AI Car", 3, 7000)

	// Scoring: same two cars but in REVERSED order, so matching must be by ID.
	// ScoringInfo.NumVehicles is at offset 104 within the info block.
	binary.LittleEndian.PutUint32(scoBuf[scoInfoOff+104:], 2)
	writeScoring(scoBuf, 0, 20, "AI Driver", 1 /*control AI*/, 0)
	writeScoring(scoBuf, 1, 10, "Player Driver", 0 /*local player*/, 1)

	f := buildFrame(7, frameBuffers{tel: telBuf, sco: scoBuf}, -1)

	if len(f.Vehicles) != 2 {
		t.Fatalf("got %d vehicles, want 2", len(f.Vehicles))
	}
	if f.PlayerID != 10 || f.PlayerIdx != 0 {
		t.Fatalf("player detection: id=%d idx=%d, want id=10 idx=0", f.PlayerID, f.PlayerIdx)
	}
	v0 := f.Vehicles[0]
	if wire.GoString(v0.Telemetry.VehicleName[:]) != "Player Car" || v0.Telemetry.Gear != 5 {
		t.Errorf("vehicle 0 telemetry wrong: %q gear=%d", wire.GoString(v0.Telemetry.VehicleName[:]), v0.Telemetry.Gear)
	}
	if v0.HasScoring == 0 || wire.GoString(v0.Scoring.DriverName[:]) != "Player Driver" {
		t.Errorf("vehicle 0 scoring not matched by ID: has=%d driver=%q", v0.HasScoring, wire.GoString(v0.Scoring.DriverName[:]))
	}
	if v0.Scoring.Place != 1 {
		t.Errorf("vehicle 0 place = %d, want 1", v0.Scoring.Place)
	}

	// forcedVehicle override
	f2 := buildFrame(8, frameBuffers{tel: telBuf, sco: scoBuf}, 1)
	if f2.PlayerIdx != 1 || f2.PlayerID != 20 {
		t.Errorf("forced vehicle: idx=%d id=%d, want idx=1 id=20", f2.PlayerIdx, f2.PlayerID)
	}
}

func writeTelem(buf []byte, idx int, id int32, name string, gear int32, rpm float64) {
	base := telVehiclesOff + idx*telemetryStride
	var vt wire.VehicleTelemetry
	vt.ID = id
	copy(vt.VehicleName[:], name)
	vt.Gear = gear
	vt.EngineRPM = rpm
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, &vt)
	copy(buf[base:], b.Bytes())
}

func writeScoring(buf []byte, idx int, id int32, driver string, control uint8, place uint8) {
	base := scoVehiclesOff + idx*scoringStride
	var vs wire.VehicleScoring
	vs.ID = id
	copy(vs.DriverName[:], driver)
	vs.Control = control
	vs.Place = place
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, &vs)
	copy(buf[base:], b.Bytes())
}
