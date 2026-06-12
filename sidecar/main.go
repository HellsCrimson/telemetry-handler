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

// Name of the rF2 Shared Memory Map Plugin's telemetry buffer.
const telemetryMapName = `$rFactor2SMMP_Telemetry$`

// readWindow is how many bytes we copy out of the mapped view each poll. The
// real buffer is hundreds of KB (header + 128 vehicles); we only decode the
// header and vehicle[0], whose last field we touch ends at offset 448, so a
// few KB is plenty and safely within the mapping.
const readWindow = 4096

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

// Offsets within a rF2VehicleTelemetry, relative to the vehicle's start
// (packed layout, verified against a live buffer hex dump).
const (
	vElapsedTime = 12  // double  (mID@0, mDeltaTime@4, mElapsedTime@12)
	vLapNumber   = 20  // long
	vVehicleName = 32  // char[64] (mLapStartET@24)
	vTrackName   = 96  // char[64]
	vLocalVel    = 184 // rF2Vec3, m/s local (mPos@160, mLocalVel@184)
	vGear        = 352 // long (-1=reverse, 0=neutral, 1+=forward)
	vEngineRPM   = 356 // double
	vThrottle    = 388 // double mUnfilteredThrottle 0..1 (water@364, oil@372, clutchRPM@380)
	vBrake       = 396 // double mUnfilteredBrake 0..1
	vSteering    = 404 // double mUnfilteredSteering -1..1
	vClutch      = 412 // double mUnfilteredClutch 0..1
)

// packet is our own UDP wire format — deliberately not the Forza FH6 layout.
// JSON keeps the first integration trivial (readable in netcat, json.Unmarshal
// in the main app) and easy to evolve; swap to a binary encoding later if the
// rate ever justifies it.
type packet struct {
	Source      string  `json:"source"` // always "lmu"
	Seq         uint64  `json:"seq"`
	Version     uint32  `json:"version"` // SMMP update counter (sanity/debug)
	NumVehicles int32   `json:"num_vehicles"`
	VehicleName string  `json:"vehicle_name"`
	TrackName   string  `json:"track_name"`
	ElapsedTime float64 `json:"elapsed_time"`
	LapNumber   int32   `json:"lap_number"`
	Gear        int32   `json:"gear"`
	EngineRPM   float64 `json:"engine_rpm"`
	SpeedMS     float64 `json:"speed_ms"` // magnitude of local velocity
	Throttle    float64 `json:"throttle"`
	Brake       float64 `json:"brake"`
	Steering    float64 `json:"steering"`
	Clutch      float64 `json:"clutch"`
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
	mapName := flag.String("map", telemetryMapName, "shared-memory object name")
	verbose := flag.Bool("v", false, "log every packet to stdout")
	dump := flag.Int("dump", 0, "hex-dump the first N bytes of the buffer once (offset layout debug), then continue")
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
		m        *mapping
		buf      = make([]byte, readWindow)
		seq      uint64
		dumped   bool
		lastLog  time.Time
		lastWarn time.Time
	)
	defer func() {
		if m != nil {
			m.close()
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

		if !readConsistent(m, buf) {
			continue // writer mid-update across all retries; try next tick
		}

		if *dump > 0 && !dumped {
			n := min(*dump, len(buf))
			log.Printf("buffer hex dump (first %d bytes):\n%s", n, hex.Dump(buf[:n]))
			dumped = true
		}

		base := offVehicle0
		vx, vy, vz := f64(buf, base+vLocalVel), f64(buf, base+vLocalVel+8), f64(buf, base+vLocalVel+16)

		pkt := packet{
			Source:      "lmu",
			Seq:         seq,
			Version:     u32(buf, offVersionBegin),
			NumVehicles: i32(buf, offNumVehicles),
			VehicleName: cstr(buf, base+vVehicleName, 64),
			TrackName:   cstr(buf, base+vTrackName, 64),
			ElapsedTime: f64(buf, base+vElapsedTime),
			LapNumber:   i32(buf, base+vLapNumber),
			Gear:        i32(buf, base+vGear),
			EngineRPM:   f64(buf, base+vEngineRPM),
			SpeedMS:     math.Sqrt(vx*vx + vy*vy + vz*vz),
			Throttle:    f64(buf, base+vThrottle),
			Brake:       f64(buf, base+vBrake),
			Steering:    f64(buf, base+vSteering),
			Clutch:      f64(buf, base+vClutch),
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
			log.Printf("seq=%d veh=%d car=%q gear=%d rpm=%.0f speed=%.1fm/s thr=%.2f brk=%.2f",
				pkt.Seq, pkt.NumVehicles, pkt.VehicleName, pkt.Gear, pkt.EngineRPM, pkt.SpeedMS, pkt.Throttle, pkt.Brake)
			lastLog = time.Now()
		}
	}
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
