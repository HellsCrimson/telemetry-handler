// Command lmu-bridge is a telemetry sidecar for Le Mans Ultimate (and any
// rFactor 2 based title). LMU does not broadcast telemetry over UDP like Forza
// does; instead the rF2 Shared Memory Map Plugin (TheIronWolfModding) mirrors
// the game's internal state into a named Windows shared-memory buffer. This
// program reads that buffer and re-emits a small JSON packet over UDP so the
// native (Linux) telemetry-handler app can consume it through its existing UDP
// receiver loop.
//
// On Linux the game runs under Proton/Wine, and the shared memory lives inside
// the Wine prefix's namespace — invisible to native processes. So this binary
// must be built for Windows and run under the SAME WINEPREFIX as the game; the
// UDP it sends to 127.0.0.1 crosses the Wine/host boundary normally.
//
//	GOOS=windows GOARCH=amd64 go build -o lmu-bridge.exe ./sidecar
//	WINEPREFIX=/path/to/prefix wine lmu-bridge.exe -addr 127.0.0.1:20440 -v
//
// First smoke test (no main app needed): point it at netcat and watch packets:
//
//	nc -u -l 20440        # on the Linux host
//	WINEPREFIX=... wine lmu-bridge.exe -addr 127.0.0.1:20440 -v
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log"
	"math"
	"os"
	"os/signal"
	"time"
)

// Names of the rF2 Shared Memory Map Plugin's buffers.
const (
	telemetryMapName = `$rFactor2SMMP_Telemetry$`
	scoringMapName   = `$rFactor2SMMP_Scoring$`
)

// maxVehicles is the rF2 shared-memory array capacity (mVehicles[128]).
const maxVehicles = 128

// Offsets into the telemetry buffer (little-endian). The rF2 headers are
// #pragma pack(push, 4), so doubles are 4-byte aligned and there is NO padding
// between fields — offsets are just the cumulative sum of field sizes.
// NOTE: rF2/MSVC `long`/`unsigned long` are 32-bit; rF2Vec3 is 3 doubles (24B).
const (
	offVersionBegin = 0  // unsigned long
	offVersionEnd   = 4  // unsigned long
	offNumVehicles  = 12 // long (after mBytesUpdatedHint@8)
	offVehicle0     = 16 // rF2VehicleTelemetry[0]
)

// tVehicleStride is sizeof(rF2VehicleTelemetry) under pack(4): the header,
// inputs, status, damage and the trailing rF2Wheel mWheels[4] (4×260B) plus the
// expansion padding. Used to step between vehicles when locating the player's
// car. If this is slightly off the player simply won't be matched and we fall
// back to vehicle[0], so it never reads garbage telemetry.
const tVehicleStride = 1888

// telemetryWindow spans the whole vehicle array so we can scan for the player.
// snapshot() clamps to the real mapped size, so over-estimating is harmless.
const telemetryWindow = offVehicle0 + maxVehicles*tVehicleStride

// Offsets into the scoring buffer (little-endian, packed 4):
//
//	rF2Scoring { u32 begin; u32 end; long bytesHint; rF2ScoringInfo info; rF2VehicleScoring veh[128]; }
//
// rF2ScoringInfo is 548 bytes, so veh[0] starts at 12+548. We only need the
// vehicle id and the player flag from each scoring entry.
const (
	sOffNumVehicles = 12 + 104 // info.mNumVehicles
	sOffVehicle0    = 12 + 548 // veh[0]
	sVehicleStride  = 584      // sizeof(rF2VehicleScoring)

	svID       = 0   // long mID — matches rF2VehicleTelemetry.mID
	svIsPlayer = 196 // bool mIsPlayer
	svControl  = 197 // signed char mControl (0 = local player)
)

const scoringWindow = sOffVehicle0 + maxVehicles*sVehicleStride

// Offsets within a rF2VehicleTelemetry, relative to the vehicle's start
// (packed layout, verified against a live buffer hex dump).
const (
	vElapsedTime  = 12  // double  (mID@0, mDeltaTime@4, mElapsedTime@12)
	vLapNumber    = 20  // long
	vVehicleName  = 32  // char[64] (mLapStartET@24)
	vTrackName    = 96  // char[64]
	vLocalVel     = 184 // rF2Vec3, m/s local (mPos@160, mLocalVel@184)
	vGear         = 352 // long (-1=reverse, 0=neutral, 1+=forward)
	vEngineRPM    = 356 // double
	vThrottle     = 388 // double mUnfilteredThrottle 0..1 (water@364, oil@372, clutchRPM@380)
	vBrake        = 396 // double mUnfilteredBrake 0..1
	vSteering     = 404 // double mUnfilteredSteering -1..1
	vClutch       = 412 // double mUnfilteredClutch 0..1
	vFuel         = 524 // double mFuel, liters (filtered inputs 420..451, misc/aero 452..523)
	vEngineMaxRPM = 532 // double mEngineMaxRPM, rev limit
)

// packet is our own UDP wire format — deliberately not the Forza FH6 layout.
// JSON keeps the first integration trivial (readable in netcat, json.Unmarshal
// in the main app) and easy to evolve; swap to a binary encoding later if the
// rate ever justifies it.
type packet struct {
	Source       string  `json:"source"` // always "lmu"
	Seq          uint64  `json:"seq"`
	Version      uint32  `json:"version"` // SMMP update counter (sanity/debug)
	NumVehicles  int32   `json:"num_vehicles"`
	VehicleName  string  `json:"vehicle_name"`
	TrackName    string  `json:"track_name"`
	ElapsedTime  float64 `json:"elapsed_time"`
	LapNumber    int32   `json:"lap_number"`
	Gear         int32   `json:"gear"` // -1=reverse, 0=neutral, 1+=forward
	EngineRPM    float64 `json:"engine_rpm"`
	EngineMaxRPM float64 `json:"engine_max_rpm"` // rev limit
	SpeedMS      float64 `json:"speed_ms"`       // magnitude of local velocity
	Throttle     float64 `json:"throttle"`
	Brake        float64 `json:"brake"`
	Steering     float64 `json:"steering"`
	Clutch       float64 `json:"clutch"`
	Fuel         float64 `json:"fuel"` // liters
}

func u32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
func i32(b []byte, off int) int32  { return int32(binary.LittleEndian.Uint32(b[off:])) }
func f64(b []byte, off int) float64 {
	return math.Float64frombits(binary.LittleEndian.Uint64(b[off:]))
}

// cstr reads a fixed-width, NUL-terminated C string.
func cstr(b []byte, off, max int) string {
	s := b[off : off+max]
	if i := bytes.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return string(bytes.TrimSpace(s))
}

func main() {
	addr := flag.String("addr", "127.0.0.1:20440", "UDP destination host:port")
	hz := flag.Int("hz", 50, "poll/send rate in Hz (rF2 telemetry updates at ~50Hz)")
	mapName := flag.String("map", telemetryMapName, "telemetry shared-memory object name")
	scoringMap := flag.String("scoring-map", scoringMapName, "scoring shared-memory object name (used to find the player's car)")
	vehicle := flag.Int("vehicle", -1, "force a telemetry vehicle index (-1 = auto-detect the player via scoring)")
	verbose := flag.Bool("v", false, "log every packet to stdout")
	dump := flag.Int("dump", 0, "hex-dump the first N bytes of the telemetry buffer once (offset layout debug), then continue")
	dumpScoring := flag.Int("dump-scoring", 0, "hex-dump the first N bytes of the scoring buffer once, then continue")
	logfile := flag.String("logfile", "", "also write logs to this file (use a Windows path under Wine, e.g. Z:\\tmp\\lmu-bridge-inner.log)")
	flag.Parse()

	// Under `proton run`, stdout/stderr go to a Wine console we can't capture,
	// so optionally tee logs to a file the bridge opens itself.
	if *logfile != "" {
		f, err := os.Create(*logfile)
		if err != nil {
			log.Fatalf("open logfile %s: %v", *logfile, err)
		}
		defer f.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}

	sender, err := newUDPSender(*addr)
	if err != nil {
		log.Fatalf("open udp %s: %v", *addr, err)
	}
	defer sender.close()
	log.Printf("lmu-bridge: sending telemetry to %s at %dHz (map %q)", *addr, *hz, *mapName)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	ticker := time.NewTicker(time.Second / time.Duration(*hz))
	defer ticker.Stop()

	var (
		m            *mapping // telemetry buffer
		ms           *mapping // scoring buffer (player detection)
		buf          = make([]byte, telemetryWindow)
		sbuf         = make([]byte, scoringWindow)
		seq          uint64
		dumped       bool
		dumpedScore  bool
		lastLog      time.Time
		lastWarn     time.Time
		lastScoreErr time.Time
	)
	defer func() {
		if m != nil {
			m.close()
		}
		if ms != nil {
			ms.close()
		}
	}()

	for {
		select {
		case <-sig:
			log.Print("lmu-bridge: shutting down")
			return
		case <-ticker.C:
		}

		// (Re)open the mapping lazily: the plugin only creates it once the
		// game is running with the plugin enabled.
		if m == nil {
			mm, err := openMapping(*mapName)
			if err != nil {
				if time.Since(lastWarn) > 2*time.Second {
					log.Printf("waiting for shared memory: %v", err)
					lastWarn = time.Now()
				}
				continue
			}
			m = mm
			log.Printf("opened shared memory %q", *mapName)
		}

		// Scoring is opened alongside telemetry; it carries the per-vehicle
		// player flag we need to identify the local driver's car. It is optional:
		// if it can't be opened we fall back to vehicle[0].
		if ms == nil && *vehicle < 0 {
			if mm, err := openMapping(*scoringMap); err == nil {
				ms = mm
				log.Printf("opened shared memory %q", *scoringMap)
			} else if time.Since(lastScoreErr) > 5*time.Second {
				log.Printf("scoring buffer unavailable, using vehicle[0]: %v", err)
				lastScoreErr = time.Now()
			}
		}

		if !readConsistent(m, buf) {
			continue // writer mid-update across all retries; try next tick
		}

		if *dump > 0 && !dumped {
			n := min(*dump, len(buf))
			log.Printf("telemetry hex dump (first %d bytes):\n%s", n, hex.Dump(buf[:n]))
			dumped = true
		}
		if *dumpScoring > 0 && !dumpedScore && ms != nil && readConsistent(ms, sbuf) {
			n := min(*dumpScoring, len(sbuf))
			log.Printf("scoring hex dump (first %d bytes):\n%s", n, hex.Dump(sbuf[:n]))
			dumpedScore = true
		}

		// Pick the player's car. Manual override wins; otherwise read scoring to
		// find the player's vehicle id and locate it in the telemetry array.
		// Anything unexpected falls back to vehicle[0] (the legacy behaviour).
		chosen := 0
		playerID := int32(0)
		switch {
		case *vehicle >= 0:
			if *vehicle < maxVehicles {
				chosen = *vehicle
			}
		case ms != nil && readConsistent(ms, sbuf):
			if pid, ok := findPlayerID(sbuf); ok {
				playerID = pid
				if idx, ok := findVehicleByID(buf, pid); ok {
					chosen = idx
				}
			}
		}
		base := offVehicle0 + chosen*tVehicleStride
		// Guard against a stride/index that would read past the snapshot.
		if base+vEngineMaxRPM+8 > len(buf) {
			base = offVehicle0
			chosen = 0
		}
		vx, vy, vz := f64(buf, base+vLocalVel), f64(buf, base+vLocalVel+8), f64(buf, base+vLocalVel+16)

		pkt := packet{
			Source:       "lmu",
			Seq:          seq,
			Version:      u32(buf, offVersionBegin),
			NumVehicles:  i32(buf, offNumVehicles),
			VehicleName:  cstr(buf, base+vVehicleName, 64),
			TrackName:    cstr(buf, base+vTrackName, 64),
			ElapsedTime:  f64(buf, base+vElapsedTime),
			LapNumber:    i32(buf, base+vLapNumber),
			Gear:         i32(buf, base+vGear),
			EngineRPM:    f64(buf, base+vEngineRPM),
			EngineMaxRPM: f64(buf, base+vEngineMaxRPM),
			SpeedMS:      math.Sqrt(vx*vx + vy*vy + vz*vz),
			Throttle:     f64(buf, base+vThrottle),
			Brake:        f64(buf, base+vBrake),
			Steering:     f64(buf, base+vSteering),
			Clutch:       f64(buf, base+vClutch),
			Fuel:         f64(buf, base+vFuel),
		}
		seq++

		data, err := json.Marshal(&pkt)
		if err != nil {
			continue
		}
		if err := sender.send(data); err != nil {
			log.Printf("udp send: %v", err)
		}

		if *verbose || time.Since(lastLog) > time.Second {
			log.Printf("seq=%d veh=%d idx=%d pid=%d car=%q gear=%d rpm=%.0f speed=%.1fm/s thr=%.2f brk=%.2f",
				pkt.Seq, pkt.NumVehicles, chosen, playerID, pkt.VehicleName, pkt.Gear, pkt.EngineRPM, pkt.SpeedMS, pkt.Throttle, pkt.Brake)
			lastLog = time.Now()
		}
	}
}

// findPlayerID scans the scoring buffer for the local player's car and returns
// its mID. The player is the scoring vehicle with mControl==0 (local control) or
// mIsPlayer set. Returns false when scoring looks invalid or no player is found,
// so the caller falls back to vehicle[0].
func findPlayerID(buf []byte) (int32, bool) {
	n := i32(buf, sOffNumVehicles)
	if n < 1 || n > maxVehicles {
		return 0, false
	}
	for i := 0; i < int(n); i++ {
		base := sOffVehicle0 + i*sVehicleStride
		if base+svControl >= len(buf) {
			break
		}
		if int8(buf[base+svControl]) == 0 || buf[base+svIsPlayer] != 0 {
			return i32(buf, base+svID), true
		}
	}
	return 0, false
}

// findVehicleByID returns the index of the telemetry vehicle whose mID matches
// id (the telemetry and scoring arrays are not necessarily in the same order, so
// we match on the stable slot id rather than the index). Returns false if absent.
func findVehicleByID(buf []byte, id int32) (int, bool) {
	n := int(i32(buf, offNumVehicles))
	if n < 1 || n > maxVehicles {
		n = maxVehicles
	}
	for i := 0; i < n; i++ {
		base := offVehicle0 + i*tVehicleStride
		if base+4 > len(buf) {
			break
		}
		if i32(buf, base+0) == id { // mID is the first field of rF2VehicleTelemetry
			return i, true
		}
	}
	return 0, false
}

// readConsistent snapshots the buffer and accepts it only when the SMMP version
// counters match (mVersionUpdateBegin == mVersionUpdateEnd), i.e. the writer was
// not mid-update. Retries briefly before giving up for this tick.
func readConsistent(m *mapping, buf []byte) bool {
	for range 8 {
		m.snapshot(buf)
		if u32(buf, offVersionBegin) == u32(buf, offVersionEnd) {
			return true
		}
		time.Sleep(200 * time.Microsecond)
	}
	return false
}
