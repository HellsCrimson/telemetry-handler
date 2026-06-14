package wire

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// byteOrder is the wire byte order. rF2 runs on x86 (little-endian) and the
// sidecar reads its shared memory little-endian, so the wire stays LE too.
var byteOrder = binary.LittleEndian

// The clean global structs below are NOT rF2-faithful mirrors — they are
// hand-picked subsets the sidecar fills by reading specific fields. Unlike the
// per-vehicle structs we don't ship the rF2 expansion arrays for globals.

// Rules is the global track-rules state (subset of rF2TrackRules).
type Rules struct {
	CurrentET            float64
	Stage                int32 // rF2TrackRulesStage
	PoleColumn           int32 // rF2TrackRulesColumn
	YellowFlagDetected   uint8
	SafetyCarExists      uint8
	SafetyCarActive      uint8
	SafetyCarLaps        int32
	SafetyCarThreshold   float32
	YellowFlagState      int8
	YellowFlagLaps       int16
	SafetyCarInstruction int32 // 0=no change,1=go active,2=head for pits
	SafetyCarSpeed       float32
}

// Weather is the detailed weather from the dedicated Weather buffer
// (rF2Weather + rF2WeatherControlInfo). Base weather (rain/temps/wind) is also
// in ScoringInfo; this adds cloudiness, the 3x3 rain grid and the control ET.
type Weather struct {
	TrackNodeSize float64
	ET            float64    // when the weather takes effect
	Raining       [9]float64 // 3x3 rain grid (0.0-1.0)
	Cloudiness    float64    // 0.0-1.0
	AmbientTempK  float64    // Kelvin
	WindMaxSpeed  float64
}

// Extended is a subset of rF2Extended: API version, the session driving aids
// (rF2PhysicsOptions), pit speed limit, and the subscription mask the sidecar
// uses to warn when Graphics/Weather are not subscribed.
type Extended struct {
	Version [12]byte
	Is64bit uint8

	// rF2PhysicsOptions (session driving aids / assists)
	TractionControl  uint8 // 0 (off) - 3 (high)
	AntiLockBrakes   uint8 // 0 (off) - 2 (high)
	StabilityControl uint8 // 0 (off) - 2 (high)
	AutoShift        uint8 // 0=off,1=up,2=down,3=all
	AutoClutch       uint8
	Invulnerable     uint8
	OppositeLock     uint8
	SteeringHelp     uint8 // 0 (off) - 3 (high)
	BrakingHelp      uint8 // 0 (off) - 2 (high)
	SpinRecovery     uint8
	AutoPit          uint8
	AutoLift         uint8
	AutoBlip         uint8
	FuelMult         uint8
	TireMult         uint8
	MechFail         uint8
	AllowPitcrewPush uint8
	RepeatShifts     uint8
	HoldClutch       uint8
	AutoReverse      uint8
	AlternateNeutral uint8
	AIControl        uint8 // player vehicle under AI control

	CurrentPitSpeedLimit    float32 // m/s
	UnsubscribedBuffersMask int32   // active UnsubscribedBuffersMask (bit 32=Graphics, 128=Weather)
	SessionStarted          uint8   // bool
}

// Graphics is a subset of rF2GraphicsInfo (camera state).
type Graphics struct {
	CamPos     Vec3
	ViewedID   int32 // slot ID being viewed (-1 if invalid)
	CameraType int32
}

// ForceFeedback mirrors rF2ForceFeedback.
type ForceFeedback struct {
	ForceValue float64
}

// PitMenu is a subset of rF2PitMenu (the current pit selection).
type PitMenu struct {
	CategoryIndex int32
	CategoryName  [32]byte
	ChoiceIndex   int32
	ChoiceString  [32]byte
	NumChoices    int32
}

// Vehicle is one car: its full telemetry plus, when matched, its scoring entry.
// HasScoring is 0 when no scoring row matched this car's ID (the Scoring field
// is then zero-valued).
type Vehicle struct {
	Telemetry  VehicleTelemetry
	HasScoring uint8
	_          [3]byte
	Scoring    VehicleScoring
}

// frameHeader is the fixed-size leading section of a marshalled frame (every
// global plus the vehicle count). The variable-length Vehicles follow it.
type frameHeader struct {
	Seq         uint32 // sidecar frame sequence
	SMMPVersion uint32 // telemetry buffer version counter (debug)
	PlayerID    int32  // local player's slot ID (-1 if unknown)
	PlayerIdx   int32  // index into Vehicles of the player car (-1 if unknown)
	NumVehicles uint32

	ScoringInfo   ScoringInfo
	Weather       Weather
	Rules         Rules
	Extended      Extended
	Graphics      Graphics
	ForceFeedback ForceFeedback
	PitMenu       PitMenu
}

// Frame is one consistent capture of all rF2 buffers: every car's full
// telemetry (+ matched scoring) and the session globals. This is the decoded
// payload reassembled from the chunked UDP datagrams.
type Frame struct {
	Seq           uint32
	SMMPVersion   uint32
	PlayerID      int32
	PlayerIdx     int32
	ScoringInfo   ScoringInfo
	Weather       Weather
	Rules         Rules
	Extended      Extended
	Graphics      Graphics
	ForceFeedback ForceFeedback
	PitMenu       PitMenu
	Vehicles      []Vehicle
}

// MarshalFrame encodes a Frame into the binary payload (before chunking).
func MarshalFrame(f *Frame) ([]byte, error) {
	hdr := frameHeader{
		Seq:           f.Seq,
		SMMPVersion:   f.SMMPVersion,
		PlayerID:      f.PlayerID,
		PlayerIdx:     f.PlayerIdx,
		NumVehicles:   uint32(len(f.Vehicles)),
		ScoringInfo:   f.ScoringInfo,
		Weather:       f.Weather,
		Rules:         f.Rules,
		Extended:      f.Extended,
		Graphics:      f.Graphics,
		ForceFeedback: f.ForceFeedback,
		PitMenu:       f.PitMenu,
	}
	buf := bytes.NewBuffer(make([]byte, 0, binary.Size(hdr)+len(f.Vehicles)*binary.Size(Vehicle{})))
	if err := binary.Write(buf, byteOrder, &hdr); err != nil {
		return nil, err
	}
	for i := range f.Vehicles {
		if err := binary.Write(buf, byteOrder, &f.Vehicles[i]); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// maxFrameVehicles bounds the vehicle count decoded from a payload so a corrupt
// length can't trigger a huge allocation. rF2 maps at most 128 vehicles.
const maxFrameVehicles = 128

// UnmarshalFrame decodes a binary payload (post-reassembly) into a Frame.
func UnmarshalFrame(data []byte) (Frame, error) {
	r := bytes.NewReader(data)
	var hdr frameHeader
	if err := binary.Read(r, byteOrder, &hdr); err != nil {
		return Frame{}, fmt.Errorf("frame header: %w", err)
	}
	if hdr.NumVehicles > maxFrameVehicles {
		return Frame{}, fmt.Errorf("implausible vehicle count %d", hdr.NumVehicles)
	}
	f := Frame{
		Seq:           hdr.Seq,
		SMMPVersion:   hdr.SMMPVersion,
		PlayerID:      hdr.PlayerID,
		PlayerIdx:     hdr.PlayerIdx,
		ScoringInfo:   hdr.ScoringInfo,
		Weather:       hdr.Weather,
		Rules:         hdr.Rules,
		Extended:      hdr.Extended,
		Graphics:      hdr.Graphics,
		ForceFeedback: hdr.ForceFeedback,
		PitMenu:       hdr.PitMenu,
		Vehicles:      make([]Vehicle, hdr.NumVehicles),
	}
	for i := range f.Vehicles {
		if err := binary.Read(r, byteOrder, &f.Vehicles[i]); err != nil {
			return Frame{}, fmt.Errorf("vehicle %d: %w", i, err)
		}
	}
	return f, nil
}

// GoString trims a fixed-width NUL-terminated C string to a Go string.
func GoString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(bytes.TrimSpace(b))
}

// Player returns the player's vehicle and true, or false when the frame has no
// identified player car.
func (f *Frame) Player() (Vehicle, bool) {
	if f.PlayerIdx < 0 || int(f.PlayerIdx) >= len(f.Vehicles) {
		return Vehicle{}, false
	}
	return f.Vehicles[f.PlayerIdx], true
}
