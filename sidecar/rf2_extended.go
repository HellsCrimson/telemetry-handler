package main

import (
	"bytes"
	"encoding/binary"

	"telemetry-handler/game/lmu/wire"
)

// The Extended buffer (rF2Extended) holds the session driving aids, the pit
// speed limit, the subscription mask and the input-buffer-enabled flags. Its
// useful fields sit AFTER two large arrays (mTrackedDamages[512] ~8KB and
// mSessionTransitionCapture ~1KB), so rather than hand-compute ~10K-byte
// offsets we mirror the whole struct (with sub-structs + explicit pads for the
// few implicit-padding spots) and let binary.Read place the fields. The
// rf2Extended size (10144) is asserted in rf2_extended_test.go so a layout
// mistake is caught at test time; the early fields (Version/Is64bit/Physics)
// are robust even if the deep arithmetic ever drifts.
//
// Note: pack(4) caps every member's alignment at 4, so 8-byte ULONGLONG fields
// align to 4 — that is why the explicit `_ [3]byte` pads appear only after lone
// bool/uint8 fields that precede a 4-aligned field.

type rf2PhysicsOptions struct {
	TractionControl         uint8
	AntiLockBrakes          uint8
	StabilityControl        uint8
	AutoShift               uint8
	AutoClutch              uint8
	Invulnerable            uint8
	OppositeLock            uint8
	SteeringHelp            uint8
	BrakingHelp             uint8
	SpinRecovery            uint8
	AutoPit                 uint8
	AutoLift                uint8
	AutoBlip                uint8
	FuelMult                uint8
	TireMult                uint8
	MechFail                uint8
	AllowPitcrewPush        uint8
	RepeatShifts            uint8
	HoldClutch              uint8
	AutoReverse             uint8
	AlternateNeutral        uint8
	AIControl               uint8
	Unused1                 uint8
	Unused2                 uint8 // 24 bytes so far, already 4-aligned (no pad)
	ManualShiftOverrideTime float32
	AutoShiftOverrideTime   float32
	SpeedSensitiveSteering  float32
	SteerRatioSpeed         float32
} // 40 bytes

type rf2TrackedDamage struct {
	MaxImpact   float64
	AccumImpact float64
} // 16 bytes

type rf2VehScoringCapture struct {
	ID           int32
	Place        uint8
	IsPlayer     uint8
	FinishStatus int8
	_            [1]byte // element padded to 4-alignment
} // 8 bytes

type rf2SessionTransitionCapture struct {
	GamePhase          uint8
	_                  [3]byte
	Session            int32
	NumScoringVehicles int32
	ScoringVehicles    [128]rf2VehScoringCapture
} // 1036 bytes

type rf2Extended struct {
	Version [12]byte
	Is64bit uint8
	_       [3]byte
	Physics rf2PhysicsOptions

	TrackedDamages [512]rf2TrackedDamage

	InRealtimeFC            uint8
	MultimediaThreadStarted uint8
	SimulationThreadStarted uint8
	SessionStarted          uint8
	TicksSessionStarted     uint64
	TicksSessionEnded       uint64

	SessionTransitionCapture      rf2SessionTransitionCapture
	DisplayedMessageUpdateCapture [128]byte

	DirectMemoryAccessEnabled uint8
	_                         [3]byte
	TicksStatusMessageUpdated uint64
	StatusMessage             [128]byte

	TicksLastHistoryMessageUpdated uint64
	LastHistoryMessage             [128]byte

	CurrentPitSpeedLimit float32

	SCRPluginEnabled        uint8
	_                       [3]byte
	SCRPluginDoubleFileType int32

	TicksLSIPhaseMessageUpdated uint64
	LSIPhaseMessage             [96]byte

	TicksLSIPitStateMessageUpdated uint64
	LSIPitStateMessage             [96]byte

	TicksLSIOrderInstructionMessageUpdated uint64
	LSIOrderInstructionMessage             [96]byte

	TicksLSIRulesInstructionMessageUpdated uint64
	LSIRulesInstructionMessage             [96]byte

	UnsubscribedBuffersMask int32

	HWControlInputEnabled      uint8
	WeatherControlInputEnabled uint8
	RulesControlInputEnabled   uint8
	PluginControlInputEnabled  uint8
} // 10144 bytes

const rf2ExtendedSize = 10144

// readExtended decodes the Extended buffer into the trimmed wire.Extended.
func readExtended(buf []byte) wire.Extended {
	if len(buf) < extOff+rf2ExtendedSize {
		return wire.Extended{}
	}
	var e rf2Extended
	if err := binary.Read(bytes.NewReader(buf[extOff:extOff+rf2ExtendedSize]), binary.LittleEndian, &e); err != nil {
		return wire.Extended{}
	}
	out := wire.Extended{
		Is64bit:                 e.Is64bit,
		TractionControl:         e.Physics.TractionControl,
		AntiLockBrakes:          e.Physics.AntiLockBrakes,
		StabilityControl:        e.Physics.StabilityControl,
		AutoShift:               e.Physics.AutoShift,
		AutoClutch:              e.Physics.AutoClutch,
		Invulnerable:            e.Physics.Invulnerable,
		OppositeLock:            e.Physics.OppositeLock,
		SteeringHelp:            e.Physics.SteeringHelp,
		BrakingHelp:             e.Physics.BrakingHelp,
		SpinRecovery:            e.Physics.SpinRecovery,
		AutoPit:                 e.Physics.AutoPit,
		AutoLift:                e.Physics.AutoLift,
		AutoBlip:                e.Physics.AutoBlip,
		FuelMult:                e.Physics.FuelMult,
		TireMult:                e.Physics.TireMult,
		MechFail:                e.Physics.MechFail,
		AllowPitcrewPush:        e.Physics.AllowPitcrewPush,
		RepeatShifts:            e.Physics.RepeatShifts,
		HoldClutch:              e.Physics.HoldClutch,
		AutoReverse:             e.Physics.AutoReverse,
		AlternateNeutral:        e.Physics.AlternateNeutral,
		AIControl:               e.Physics.AIControl,
		CurrentPitSpeedLimit:    e.CurrentPitSpeedLimit,
		UnsubscribedBuffersMask: e.UnsubscribedBuffersMask,
		SessionStarted:          e.SessionStarted,
	}
	copy(out.Version[:], e.Version[:])
	return out
}
