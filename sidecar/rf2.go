package main

import (
	"bytes"
	"encoding/binary"
	"math"

	"telemetry-handler/game/lmu/wire"
)

// This file maps the rF2 Shared Memory Map Plugin's buffers (TheIronWolf's
// rF2State.h, #pragma pack(4)) into the app's wire.Frame.
//
// Each mapped buffer is prefixed by an 8-byte rF2MappedBufferVersionBlock
// (mVersionUpdateBegin/End) written by the plugin's mapping wrapper, even
// though it is not part of the ISI struct. Buffers derived from
// rF2MappedBufferHeaderWithSize (Telemetry, Scoring, Rules) additionally carry
// a 4-byte mBytesUpdatedHint before the struct body. The non-versioned buffers
// (Weather/Graphics/ForceFeedback/PitInfo/Extended) carry only the version
// block. The constants below encode those prefixes; the per-vehicle/scoring
// strides match wire's mirror structs (asserted in lmu/wire/wire_test.go).
const (
	prefVersionBlock = 8 // rF2MappedBufferVersionBlock
	prefBytesHint    = 4 // mBytesUpdatedHint (WithSize buffers only)

	telNumVehOff   = prefVersionBlock + prefBytesHint     // rF2Telemetry.mNumVehicles
	telVehiclesOff = prefVersionBlock + prefBytesHint + 4 // rF2Telemetry.mVehicles[0]

	scoInfoOff     = prefVersionBlock + prefBytesHint // rF2Scoring.mScoringInfo
	scoVehiclesOff = scoInfoOff + scoringInfoStride   // rF2Scoring.mVehicles[0]

	rulesOff   = prefVersionBlock + prefBytesHint // rF2Rules.mTrackRules
	weatherOff = prefVersionBlock                 // rF2Weather
	gfxOff     = prefVersionBlock                 // rF2Graphics.mGraphicsInfo
	ffbOff     = prefVersionBlock                 // rF2ForceFeedback
	extOff     = prefVersionBlock                 // rF2Extended
	pitOff     = prefVersionBlock                 // rF2PitInfo.mPitMenu

	telemetryStride   = 1888
	scoringStride     = 584
	scoringInfoStride = 548
)

// Little-endian field readers (rF2 runs on x86).
func u32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
func i32(b []byte, off int) int32  { return int32(binary.LittleEndian.Uint32(b[off:])) }
func i16(b []byte, off int) int16  { return int16(binary.LittleEndian.Uint16(b[off:])) }
func f64(b []byte, off int) float64 {
	return math.Float64frombits(binary.LittleEndian.Uint64(b[off:]))
}
func f32(b []byte, off int) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(b[off:]))
}

// frameBuffers holds the snapshotted bytes of every rF2 buffer for one tick.
// Any field may be nil/short when that buffer is unavailable; the readers below
// return zero values in that case so a missing optional buffer is non-fatal.
type frameBuffers struct {
	tel, sco, rules, weather, ext, gfx, ffb, pit []byte
}

// buildFrame assembles a wire.Frame from the raw buffers. forcedVehicle >= 0
// overrides player detection with that telemetry index.
func buildFrame(seq uint32, b frameBuffers, forcedVehicle int) wire.Frame {
	f := wire.Frame{
		Seq:       seq,
		PlayerID:  -1,
		PlayerIdx: -1,
	}
	if len(b.tel) >= prefVersionBlock {
		f.SMMPVersion = u32(b.tel, 0)
	}

	// Scoring: parse the info block, then every scoring vehicle, indexing them
	// by slot ID and noting the local player's ID.
	scoringByID := map[int32]wire.VehicleScoring{}
	playerID := int32(-1)
	if si, ok := readScoringInfo(b.sco); ok {
		f.ScoringInfo = si
		n := clampCount(int(si.NumVehicles))
		for i := range n {
			base := scoVehiclesOff + i*scoringStride
			vs, ok := readScoring(b.sco, base)
			if !ok {
				break
			}
			scoringByID[vs.ID] = vs
			if playerID < 0 && (vs.Control == 0 || vs.IsPlayer != 0) {
				playerID = vs.ID
			}
		}
	}

	// Telemetry: every vehicle, matched to its scoring row by slot ID.
	if len(b.tel) >= telVehiclesOff {
		n := clampCount(int(i32(b.tel, telNumVehOff)))
		for i := range n {
			base := telVehiclesOff + i*telemetryStride
			vt, ok := readTelemetry(b.tel, base)
			if !ok {
				break
			}
			veh := wire.Vehicle{Telemetry: vt}
			if vs, ok := scoringByID[vt.ID]; ok {
				veh.HasScoring = 1
				veh.Scoring = vs
			}
			if vt.ID == playerID {
				f.PlayerIdx = int32(len(f.Vehicles))
				f.PlayerID = playerID
			}
			f.Vehicles = append(f.Vehicles, veh)
		}
	}

	if forcedVehicle >= 0 && forcedVehicle < len(f.Vehicles) {
		f.PlayerIdx = int32(forcedVehicle)
		f.PlayerID = f.Vehicles[forcedVehicle].Telemetry.ID
	}

	f.Rules = readRules(b.rules)
	f.Weather = readWeather(b.weather)
	f.Graphics = readGraphics(b.gfx)
	f.ForceFeedback = readForceFeedback(b.ffb)
	f.PitMenu = readPitMenu(b.pit)
	f.Extended = readExtended(b.ext)
	return f
}

// clampCount bounds a vehicle count read from shared memory to [0, 128].
func clampCount(n int) int {
	if n < 0 {
		return 0
	}
	if n > maxVehicles {
		return maxVehicles
	}
	return n
}

func readTelemetry(buf []byte, base int) (wire.VehicleTelemetry, bool) {
	var vt wire.VehicleTelemetry
	if base < 0 || base+telemetryStride > len(buf) {
		return vt, false
	}
	if err := binary.Read(bytes.NewReader(buf[base:base+telemetryStride]), binary.LittleEndian, &vt); err != nil {
		return wire.VehicleTelemetry{}, false
	}
	return vt, true
}

func readScoring(buf []byte, base int) (wire.VehicleScoring, bool) {
	var vs wire.VehicleScoring
	if base < 0 || base+scoringStride > len(buf) {
		return vs, false
	}
	if err := binary.Read(bytes.NewReader(buf[base:base+scoringStride]), binary.LittleEndian, &vs); err != nil {
		return wire.VehicleScoring{}, false
	}
	return vs, true
}

func readScoringInfo(buf []byte) (wire.ScoringInfo, bool) {
	var si wire.ScoringInfo
	if len(buf) < scoInfoOff+scoringInfoStride {
		return si, false
	}
	if err := binary.Read(bytes.NewReader(buf[scoInfoOff:scoInfoOff+scoringInfoStride]), binary.LittleEndian, &si); err != nil {
		return wire.ScoringInfo{}, false
	}
	return si, true
}

// readRules pulls the global track-rules fields from rF2TrackRules. Offsets are
// relative to mTrackRules (see rF2State.h); the large mInput*Expansion arrays
// are skipped, which is why mYellowFlagState et al. sit far into the struct.
func readRules(buf []byte) wire.Rules {
	base := rulesOff
	if len(buf) < base+332 {
		return wire.Rules{}
	}
	return wire.Rules{
		CurrentET:            f64(buf, base+0),
		Stage:                i32(buf, base+8),
		PoleColumn:           i32(buf, base+12),
		YellowFlagDetected:   buf[base+32],
		SafetyCarExists:      buf[base+34],
		SafetyCarActive:      buf[base+35],
		SafetyCarLaps:        i32(buf, base+36),
		SafetyCarThreshold:   f32(buf, base+40),
		YellowFlagState:      int8(buf[base+320]),
		YellowFlagLaps:       i16(buf, base+322),
		SafetyCarInstruction: i32(buf, base+324),
		SafetyCarSpeed:       f32(buf, base+328),
	}
}

// readWeather reads rF2Weather (mTrackNodeSize + rF2WeatherControlInfo).
func readWeather(buf []byte) wire.Weather {
	base := weatherOff
	if len(buf) < base+112 {
		return wire.Weather{}
	}
	w := wire.Weather{
		TrackNodeSize: f64(buf, base+0),
		ET:            f64(buf, base+8),    // mWeatherInfo.mET
		Cloudiness:    f64(buf, base+8+80), // after mET + mRaining[3][3]
		AmbientTempK:  f64(buf, base+8+88),
		WindMaxSpeed:  f64(buf, base+8+96),
	}
	for k := range 9 {
		w.Raining[k] = f64(buf, base+8+8+k*8) // mRaining[3][3]
	}
	return w
}

// readGraphics reads the camera fields of rF2GraphicsInfo (mHWND is skipped).
func readGraphics(buf []byte) wire.Graphics {
	base := gfxOff
	if len(buf) < base+136 {
		return wire.Graphics{}
	}
	return wire.Graphics{
		CamPos:     wire.Vec3{X: f64(buf, base+0), Y: f64(buf, base+8), Z: f64(buf, base+16)},
		ViewedID:   i32(buf, base+128),
		CameraType: i32(buf, base+132),
	}
}

func readForceFeedback(buf []byte) wire.ForceFeedback {
	base := ffbOff
	if len(buf) < base+8 {
		return wire.ForceFeedback{}
	}
	return wire.ForceFeedback{ForceValue: f64(buf, base+0)}
}

// readPitMenu reads rF2PitMenu (the current pit selection).
func readPitMenu(buf []byte) wire.PitMenu {
	base := pitOff
	if len(buf) < base+76 {
		return wire.PitMenu{}
	}
	pm := wire.PitMenu{
		CategoryIndex: i32(buf, base+0),
		ChoiceIndex:   i32(buf, base+36),
		NumChoices:    i32(buf, base+72),
	}
	copy(pm.CategoryName[:], buf[base+4:base+36])
	copy(pm.ChoiceString[:], buf[base+40:base+72])
	return pm
}
