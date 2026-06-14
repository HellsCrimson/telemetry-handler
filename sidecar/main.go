// Command lmu-bridge is a telemetry sidecar for Le Mans Ultimate (and any
// rFactor 2 based title). LMU does not broadcast telemetry over UDP like Forza
// does; instead the rF2 Shared Memory Map Plugin (TheIronWolfModding) mirrors
// the game's internal state into named Windows shared-memory buffers. This
// program reads ALL of those buffers — full telemetry for EVERY car (engine,
// wheels/tires, suspension, forces, aero, damage, electric boost, ...), the
// full per-car scoring (positions, lap/sector times, gaps, pit state, flags),
// plus the session globals (weather, rules, driving aids, pit speed limit) —
// and re-emits one consistent binary frame per tick over UDP. A frame can
// exceed the 64KB datagram limit at a full grid, so it is split into several
// chunked datagrams the main app reassembles (see telemetry-handler/game/lmu/wire).
//
// On Linux the game runs under Proton/Wine, and the shared memory lives inside
// the Wine prefix's namespace — invisible to native processes. So this binary
// must be built for Windows and run under the SAME WINEPREFIX as the game; the
// UDP it sends to 127.0.0.1 crosses the Wine/host boundary normally.
//
//	GOOS=windows GOARCH=amd64 go build -o lmu-bridge.exe ./sidecar
//	WINEPREFIX=/path/to/prefix wine lmu-bridge.exe -addr 127.0.0.1:20440 -v
package main

import (
	"encoding/hex"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"time"

	"telemetry-handler/game/lmu/wire"
)

// Names of the rF2 Shared Memory Map Plugin's buffers.
const (
	telemetryMapName     = `$rFactor2SMMP_Telemetry$`
	scoringMapName       = `$rFactor2SMMP_Scoring$`
	rulesMapName         = `$rFactor2SMMP_Rules$`
	weatherMapName       = `$rFactor2SMMP_Weather$`
	graphicsMapName      = `$rFactor2SMMP_Graphics$`
	forceFeedbackMapName = `$rFactor2SMMP_ForceFeedback$`
	pitInfoMapName       = `$rFactor2SMMP_PitInfo$`
	extendedMapName      = `$rFactor2SMMP_Extended$`
)

// maxVehicles is the rF2 shared-memory array capacity (mVehicles[128]).
const maxVehicles = 128

// Per-buffer snapshot sizes. Telemetry/Scoring span the whole vehicle array;
// the others only need their (small) leading region we read — snapshot() clamps
// to the real mapped size, so over-allocating here is harmless.
const (
	telemetryWindow = telVehiclesOff + maxVehicles*telemetryStride
	scoringWindow   = scoVehiclesOff + maxVehicles*scoringStride
	rulesWindow     = rulesOff + 512
	weatherWindow   = weatherOff + 256
	graphicsWindow  = gfxOff + 256
	ffbWindow       = ffbOff + 64
	pitWindow       = pitOff + 512
	extendedWindow  = extOff + rf2ExtendedSize
)

// buffer is one lazily-opened shared-memory mapping we snapshot each tick.
type buffer struct {
	name      string
	required  bool // telemetry is required; the rest are best-effort
	versioned bool // tear-free read via the begin/end version counters
	m         *mapping
	buf       []byte
	warnedAt  time.Time
}

// snapshot (re)opens the mapping if needed and copies a consistent view into
// buf. Returns the bytes, or nil if the buffer is unavailable / mid-update.
func (b *buffer) snapshot(mapName string) []byte {
	if b.m == nil {
		m, err := openMapping(mapName)
		if err != nil {
			if time.Since(b.warnedAt) > 5*time.Second {
				if b.required {
					log.Printf("waiting for %s shared memory: %v", b.name, err)
				} else {
					log.Printf("%s buffer unavailable (optional): %v", b.name, err)
				}
				b.warnedAt = time.Now()
			}
			return nil
		}
		b.m = m
		log.Printf("opened shared memory %q", mapName)
	}
	if b.versioned {
		if !readConsistent(b.m, b.buf) {
			return nil // writer mid-update across all retries; skip this tick
		}
		return b.buf
	}
	b.m.snapshot(b.buf)
	return b.buf
}

func (b *buffer) close() {
	if b.m != nil {
		b.m.close()
		b.m = nil
	}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:20440", "UDP destination host:port")
	hz := flag.Int("hz", 50, "poll/send rate in Hz (rF2 telemetry updates at ~50Hz)")
	vehicle := flag.Int("vehicle", -1, "force a telemetry vehicle index (-1 = auto-detect the player via scoring)")
	chunkSize := flag.Int("chunk", 0, "max UDP payload bytes per chunk (0 = default ~60000)")
	noSubscribe := flag.Bool("no-subscribe", false, "don't write the PluginControl buffer to enable Graphics/Weather")
	verbose := flag.Bool("v", false, "log every frame to stdout")
	dump := flag.Int("dump", 0, "hex-dump the first N bytes of the telemetry buffer once, then continue")
	dumpScoring := flag.Int("dump-scoring", 0, "hex-dump the first N bytes of the scoring buffer once, then continue")
	dumpExtended := flag.Int("dump-extended", 0, "hex-dump the first N bytes of the extended buffer once, then continue")
	logfile := flag.String("logfile", "", "also write logs to this file (Windows path under Wine, e.g. Z:\\tmp\\lmu-bridge.log)")
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
	log.Printf("lmu-bridge: streaming full telemetry to %s at %dHz", *addr, *hz)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	ticker := time.NewTicker(time.Second / time.Duration(*hz))
	defer ticker.Stop()

	buffers := map[string]*buffer{
		"telemetry": {name: "telemetry", required: true, versioned: true, buf: make([]byte, telemetryWindow)},
		"scoring":   {name: "scoring", versioned: true, buf: make([]byte, scoringWindow)},
		"rules":     {name: "rules", versioned: true, buf: make([]byte, rulesWindow)},
		"weather":   {name: "weather", buf: make([]byte, weatherWindow)},
		"graphics":  {name: "graphics", buf: make([]byte, graphicsWindow)},
		"ffb":       {name: "forcefeedback", buf: make([]byte, ffbWindow)},
		"pit":       {name: "pitinfo", buf: make([]byte, pitWindow)},
		"extended":  {name: "extended", buf: make([]byte, extendedWindow)},
	}
	mapNames := map[string]string{
		"telemetry": telemetryMapName,
		"scoring":   scoringMapName,
		"rules":     rulesMapName,
		"weather":   weatherMapName,
		"graphics":  graphicsMapName,
		"ffb":       forceFeedbackMapName,
		"pit":       pitInfoMapName,
		"extended":  extendedMapName,
	}
	defer func() {
		for _, b := range buffers {
			b.close()
		}
	}()

	var (
		pluginCtl    *mapping // PluginControl input buffer (RW), for subscription
		seq          uint32
		dumped       [3]bool
		lastLog      time.Time
		lastSubWarn  time.Time
		lastSubWrite time.Time
	)
	defer func() {
		if pluginCtl != nil {
			pluginCtl.close()
		}
	}()

	for {
		select {
		case <-sig:
			log.Print("lmu-bridge: shutting down")
			return
		case <-ticker.C:
		}

		telBuf := buffers["telemetry"].snapshot(mapNames["telemetry"])
		if telBuf == nil {
			continue // no game / plugin yet, or a torn read
		}

		b := frameBuffers{
			tel:     telBuf,
			sco:     buffers["scoring"].snapshot(mapNames["scoring"]),
			rules:   buffers["rules"].snapshot(mapNames["rules"]),
			weather: buffers["weather"].snapshot(mapNames["weather"]),
			ext:     buffers["extended"].snapshot(mapNames["extended"]),
			gfx:     buffers["graphics"].snapshot(mapNames["graphics"]),
			ffb:     buffers["ffb"].snapshot(mapNames["ffb"]),
			pit:     buffers["pit"].snapshot(mapNames["pit"]),
		}

		if *dump > 0 && !dumped[0] {
			n := min(*dump, len(b.tel))
			log.Printf("telemetry hex dump (first %d bytes):\n%s", n, hex.Dump(b.tel[:n]))
			dumped[0] = true
		}
		if *dumpScoring > 0 && !dumped[1] && b.sco != nil {
			n := min(*dumpScoring, len(b.sco))
			log.Printf("scoring hex dump (first %d bytes):\n%s", n, hex.Dump(b.sco[:n]))
			dumped[1] = true
		}
		if *dumpExtended > 0 && !dumped[2] && b.ext != nil {
			n := min(*dumpExtended, len(b.ext))
			log.Printf("extended hex dump (first %d bytes):\n%s", n, hex.Dump(b.ext[:n]))
			dumped[2] = true
		}

		// Best-effort buffer subscription so Graphics/Weather (unsubscribed by
		// default) start updating. Retry every 3s while still unsubscribed.
		if !*noSubscribe {
			maybeSubscribe(&pluginCtl, b.ext, &lastSubWrite, &lastSubWarn)
		}

		frame := buildFrame(seq, b, *vehicle)
		seq++

		payload, err := wire.MarshalFrame(&frame)
		if err != nil {
			log.Printf("marshal frame: %v", err)
			continue
		}
		chunks := wire.Chunk(payload, frame.Seq, *chunkSize)
		for _, c := range chunks {
			if err := sender.send(c); err != nil {
				log.Printf("udp send: %v", err)
				break
			}
		}

		if *verbose || time.Since(lastLog) > time.Second {
			car := ""
			if p, ok := frame.Player(); ok {
				car = wire.GoString(p.Telemetry.VehicleName[:])
			}
			log.Printf("seq=%d cars=%d playerIdx=%d car=%q payload=%dB chunks=%d",
				frame.Seq, len(frame.Vehicles), frame.PlayerIdx, car, len(payload), len(chunks))
			lastLog = time.Now()
		}
	}
}

// maybeSubscribe opens the PluginControl buffer (once) and requests that the
// plugin enable all buffers. While Graphics/Weather remain unsubscribed (per
// the Extended mask) it re-requests every 3s and warns (every 30s) that the
// static CustomPluginVariables.JSON fix may be needed.
func maybeSubscribe(pluginCtl **mapping, extBuf []byte, lastWrite, lastWarn *time.Time) {
	mask := int32(0)
	if extBuf != nil {
		mask = readExtended(extBuf).UnsubscribedBuffersMask
	}
	if extBuf != nil && !graphicsOrWeatherUnsubscribed(mask) {
		return // already subscribed to everything we need
	}

	if *pluginCtl == nil {
		m, err := openMappingRW(pluginControlMapName)
		if err != nil {
			if time.Since(*lastWarn) > 30*time.Second {
				log.Printf("PluginControl buffer unavailable; to enable Graphics/Weather set "+
					"\"UnsubscribedBuffersMask\": 0 in CustomPluginVariables.JSON: %v", err)
				*lastWarn = time.Now()
			}
			return
		}
		*pluginCtl = m
	}

	if time.Since(*lastWrite) > 3*time.Second {
		subscribeAll(*pluginCtl)
		*lastWrite = time.Now()
	}
	if extBuf != nil && time.Since(*lastWarn) > 30*time.Second {
		log.Printf("Graphics/Weather still unsubscribed (mask=%d); if they never populate, set "+
			"\"UnsubscribedBuffersMask\": 0 in CustomPluginVariables.JSON", mask)
		*lastWarn = time.Now()
	}
}

// readConsistent snapshots the buffer and accepts it only when the SMMP version
// counters match (mVersionUpdateBegin == mVersionUpdateEnd), i.e. the writer
// was not mid-update. Retries briefly before giving up for this tick.
func readConsistent(m *mapping, buf []byte) bool {
	for range 8 {
		m.snapshot(buf)
		if u32(buf, 0) == u32(buf, 4) {
			return true
		}
		time.Sleep(200 * time.Microsecond)
	}
	return false
}
