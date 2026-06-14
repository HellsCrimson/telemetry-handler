package wire

import (
	"encoding/binary"
	"reflect"
	"testing"
)

// offsetOf returns the byte offset of the named field within v as
// encoding/binary lays it out (the sum of the sizes of all preceding fields,
// blank padding fields included).
func offsetOf(v any, field string) int {
	t := reflect.TypeOf(v)
	off := 0
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Name == field {
			return off
		}
		off += binary.Size(reflect.Zero(f.Type).Interface())
	}
	return -1
}

// TestStructSizes locks the rF2-faithful mirror structs to the exact sizes from
// TheIronWolf's rF2State.h (#pragma pack(4)). These are the strides the sidecar
// steps by when reading shared memory, so a layout drift here is a correctness
// bug — fail the build, don't read garbage. The values are independently
// verified: the legacy sidecar's hand-derived offsets (mGear@352, mFuel@524,
// mEngineMaxRPM@532, mPhysicalSteeringWheelRange@692) all fall out of these.
func TestStructSizes(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"Vec3", binary.Size(Vec3{}), 24},
		{"Wheel", binary.Size(Wheel{}), 260},
		{"VehicleTelemetry", binary.Size(VehicleTelemetry{}), 1888},
		{"VehicleScoring", binary.Size(VehicleScoring{}), 584},
		{"ScoringInfo", binary.Size(ScoringInfo{}), 548},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("sizeof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// fieldOffset returns the byte offset of a named field within a mirror struct as
// encoding/binary lays it out (which equals the rF2 pack(4) offset, since the
// structs are padding-free apart from the explicit expansion arrays).
func TestKnownFieldOffsets(t *testing.T) {
	// These offsets were verified against a live rF2 buffer hex dump in the
	// original sidecar; they pin the telemetry struct layout end-to-end.
	telem := []struct {
		name string
		off  int
	}{
		{"Gear", 352},
		{"EngineRPM", 356},
		{"EngineWaterTemp", 364},
		{"EngineOilTemp", 372},
		{"ClutchRPM", 380},
		{"UnfilteredThrottle", 388},
		{"UnfilteredBrake", 396},
		{"UnfilteredSteering", 404},
		{"UnfilteredClutch", 412},
		{"Fuel", 524},
		{"EngineMaxRPM", 532},
		{"PhysicalSteeringWheelRange", 692},
	}
	for _, c := range telem {
		if off := offsetOf(VehicleTelemetry{}, c.name); off != c.off {
			t.Errorf("VehicleTelemetry.%s offset = %d, want %d", c.name, off, c.off)
		}
	}

	scoring := []struct {
		name string
		off  int
	}{
		{"IsPlayer", 196},
		{"Control", 197},
	}
	for _, c := range scoring {
		if off := offsetOf(VehicleScoring{}, c.name); off != c.off {
			t.Errorf("VehicleScoring.%s offset = %d, want %d", c.name, off, c.off)
		}
	}

	if off := offsetOf(ScoringInfo{}, "NumVehicles"); off != 104 {
		t.Errorf("ScoringInfo.NumVehicles offset = %d, want 104", off)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	in := Frame{
		Seq:         42,
		SMMPVersion: 7,
		PlayerID:    3,
		PlayerIdx:   1,
		Rules:       Rules{Stage: 2, SafetyCarActive: 1, SafetyCarSpeed: 30},
		Weather:     Weather{Cloudiness: 0.4, AmbientTempK: 295},
		Extended:    Extended{TractionControl: 2, CurrentPitSpeedLimit: 22.2, UnsubscribedBuffersMask: 160},
	}
	copy(in.ScoringInfo.TrackName[:], "Le Mans")
	in.Vehicles = []Vehicle{
		{Telemetry: mkTelem("Car A", 6, 8500), HasScoring: 1, Scoring: mkScoring("Driver A", 1)},
		{Telemetry: mkTelem("Car B", 4, 7200), HasScoring: 1, Scoring: mkScoring("Driver B", 2)},
	}

	payload, err := MarshalFrame(&in)
	if err != nil {
		t.Fatalf("MarshalFrame: %v", err)
	}
	out, err := UnmarshalFrame(payload)
	if err != nil {
		t.Fatalf("UnmarshalFrame: %v", err)
	}

	if out.Seq != in.Seq || out.PlayerIdx != in.PlayerIdx || out.PlayerID != in.PlayerID {
		t.Errorf("header mismatch: %+v", out)
	}
	if len(out.Vehicles) != 2 {
		t.Fatalf("got %d vehicles, want 2", len(out.Vehicles))
	}
	if got := GoString(out.Vehicles[0].Telemetry.VehicleName[:]); got != "Car A" {
		t.Errorf("vehicle 0 name = %q, want Car A", got)
	}
	if out.Vehicles[1].Telemetry.Gear != 4 || out.Vehicles[1].Telemetry.EngineRPM != 7200 {
		t.Errorf("vehicle 1 telemetry mismatch: %+v", out.Vehicles[1].Telemetry)
	}
	if got := GoString(out.Vehicles[1].Scoring.DriverName[:]); got != "Driver B" {
		t.Errorf("vehicle 1 driver = %q, want Driver B", got)
	}
	if out.Rules.SafetyCarSpeed != 30 || out.Extended.CurrentPitSpeedLimit != 22.2 {
		t.Errorf("globals mismatch: rules=%+v ext=%+v", out.Rules, out.Extended)
	}
	if p, ok := out.Player(); !ok || GoString(p.Telemetry.VehicleName[:]) != "Car B" {
		t.Errorf("Player() = %v ok=%v", GoString(p.Telemetry.VehicleName[:]), ok)
	}
}

func TestChunkReassembleMultiChunk(t *testing.T) {
	in := Frame{Seq: 5}
	// Enough vehicles to force several small chunks.
	for i := 0; i < 40; i++ {
		in.Vehicles = append(in.Vehicles, Vehicle{Telemetry: mkTelem("c", int32(i%6), float64(1000*i))})
	}
	payload, err := MarshalFrame(&in)
	if err != nil {
		t.Fatalf("MarshalFrame: %v", err)
	}

	chunks := Chunk(payload, in.Seq, 4096)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d (payload %d)", len(chunks), len(payload))
	}

	var re Reassembler
	var done []byte
	// Feed out of order to exercise offset placement.
	order := []int{len(chunks) - 1}
	for i := 0; i < len(chunks)-1; i++ {
		order = append(order, i)
	}
	for _, idx := range order {
		got, ok, err := re.Add(chunks[idx])
		if err != nil {
			t.Fatalf("Add chunk %d: %v", idx, err)
		}
		if ok {
			done = got
		}
	}
	if done == nil {
		t.Fatal("frame never completed")
	}
	out, err := UnmarshalFrame(done)
	if err != nil {
		t.Fatalf("UnmarshalFrame: %v", err)
	}
	if len(out.Vehicles) != 40 {
		t.Fatalf("got %d vehicles, want 40", len(out.Vehicles))
	}
}

func TestReassemblerDropsStaleFrame(t *testing.T) {
	in := Frame{Seq: 1, Vehicles: []Vehicle{{Telemetry: mkTelem("a", 1, 100)}, {Telemetry: mkTelem("b", 2, 200)}}}
	payload, _ := MarshalFrame(&in)
	chunks := Chunk(payload, in.Seq, 512)
	if len(chunks) < 2 {
		t.Skip("need multi-chunk payload")
	}

	next := Frame{Seq: 2, Vehicles: in.Vehicles}
	payload2, _ := MarshalFrame(&next)
	chunks2 := Chunk(payload2, next.Seq, 512)

	var re Reassembler
	// First chunk of frame 1, then frame 2 fully — frame 1 must be abandoned.
	if _, ok, err := re.Add(chunks[0]); err != nil || ok {
		t.Fatalf("unexpected: ok=%v err=%v", ok, err)
	}
	var completed bool
	for _, c := range chunks2 {
		if _, ok, err := re.Add(c); err != nil {
			t.Fatalf("Add: %v", err)
		} else if ok {
			completed = true
		}
	}
	if !completed {
		t.Fatal("frame 2 should have completed after superseding frame 1")
	}
}

func TestIsEnvelope(t *testing.T) {
	chunks := Chunk([]byte("hello"), 1, 0)
	if !IsEnvelope(chunks[0]) {
		t.Error("Chunk output should be recognized as an envelope")
	}
	if IsEnvelope([]byte("{json}")) {
		t.Error("JSON must not look like an envelope")
	}
	if IsEnvelope(make([]byte, 324)) {
		t.Error("a zeroed Forza-sized packet must not look like an envelope")
	}
}

func mkTelem(name string, gear int32, rpm float64) VehicleTelemetry {
	var t VehicleTelemetry
	copy(t.VehicleName[:], name)
	t.Gear = gear
	t.EngineRPM = rpm
	return t
}

func mkScoring(driver string, place uint8) VehicleScoring {
	var s VehicleScoring
	copy(s.DriverName[:], driver)
	s.Place = place
	return s
}
