package app

import (
	"fmt"
	"log"
	"sync"
	"time"

	"encoding/json"

	"telemetry-handler/analysis"
	"telemetry-handler/config"
	"telemetry-handler/engineer"
	"telemetry-handler/game/forza"
	"telemetry-handler/game/lmu/wire"
	"telemetry-handler/wheelbase/moza"
	"telemetry-handler/recording"
	"telemetry-handler/store"
)

// TelemetryMeta carries descriptive session info that does not fit the binary
// forza.Telemetry model (which has no string fields). It is populated per source
// — currently LMU, which sends the car/track names the dashboard's Info tab
// shows. Forza leaves it zero-valued.
type TelemetryMeta struct {
	Car         string  `json:"car"`          // vehicle/car name
	Track       string  `json:"track"`        // track name
	SessionTime float64 `json:"session_time"` // elapsed session time (seconds)
	NumVehicles int     `json:"num_vehicles"` // cars in the session
	// SteeringRangeDeg is the car's lock-to-lock steering rotation in degrees
	// (LMU reports it per car). 0 when the game doesn't provide it (Forza), in
	// which case the overlay falls back to the configured default.
	SteeringRangeDeg float64 `json:"steering_range_deg"`
}

// TelemetrySnapshot is the latest parsed telemetry frame plus metadata about
// when it was received. It is returned to the frontend over the Wails bindings.
type TelemetrySnapshot struct {
	Telemetry  forza.Telemetry `json:"telemetry"`
	ReceivedAt time.Time       `json:"received_at"`
	Available  bool            `json:"available"`
	// Source identifies the game that produced the latest frame ("forza" or
	// "lmu"), so the dashboard can tailor which tabs/readouts it shows. Empty
	// until the first packet arrives.
	Source string `json:"source"`
	// Meta is descriptive session info (car/track names, etc.) that has no place
	// in the binary telemetry struct. Surfaced on the dashboard's Info tab.
	Meta TelemetryMeta `json:"meta"`
}

// ReplaySample is a single telemetry frame from a recording, offset from the
// start of the recording in milliseconds. Source/Meta mirror the live
// TelemetrySnapshot so the dashboard can tailor a replay/review per game.
type ReplaySample struct {
	OffsetMS  uint64          `json:"offset_ms"`
	Telemetry forza.Telemetry `json:"telemetry"`
	Source    string          `json:"source"`
	Meta      TelemetryMeta   `json:"meta"`
}

// Runtime holds the shared, mutex-protected application state: the latest
// telemetry snapshot, the active configuration, the optional MOZA driver and
// the recording manager. It is the single source of truth consumed by the UDP
// receiver loop, the Wails-bound service and the overlay.
type Runtime struct {
	mu        sync.RWMutex
	cfg       config.Config
	cfgPath   string
	telemetry forza.Telemetry
	seen      bool
	seenAt    time.Time
	source    string
	meta      TelemetryMeta
	frame     *wire.Frame        // full LMU frame (all cars + globals); nil for Forza
	engineer  *engineer.Engineer // live strategy engine (multi-car SessionState)
	store     *store.Store       // local persistence (reference laps, corners, sessions, recordings); nil if unavailable
	moza      *moza.Driver
	recorder  *recording.Manager

	// mozaWarned dedupes the "waiting for wheel" log so the reconnect
	// supervisor does not spam it every tick while the wheel is absent.
	mozaWarned bool

	// loadErrPath/loadErr record a config-file load failure so the dashboard can
	// surface that the app fell back to default settings.
	loadErrPath string
	loadErr     string
}

func NewRuntime(cfg config.Config, cfgPath string, recorder *recording.Manager, st *store.Store) *Runtime {
	return &Runtime{cfg: cfg, cfgPath: cfgPath, recorder: recorder, store: st, engineer: engineer.New()}
}

// SetLoadError records that the config file at path failed to load (msg is the
// error), so the runtime started with defaults. Surfaced via Service.GetConfigStatus.
func (r *Runtime) SetLoadError(path, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadErrPath = path
	r.loadErr = msg
}

// LoadError returns the recorded config-load failure path and message (both
// empty when the config loaded cleanly or defaults were used with no file).
func (r *Runtime) LoadError() (path, msg string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loadErrPath, r.loadErr
}

func (r *Runtime) Config() config.Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

func (r *Runtime) LatestTelemetry() TelemetrySnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return TelemetrySnapshot{
		Telemetry:  r.telemetry,
		ReceivedAt: r.seenAt,
		Available:  r.seen,
		Source:     r.source,
		Meta:       r.meta,
	}
}

// SetTelemetry records the latest frame, the game it came from (source is
// "forza" or "lmu") and descriptive session meta, used by the dashboard to
// tailor its layout per game and populate the Info tab.
func (r *Runtime) SetTelemetry(telemetry forza.Telemetry, source string, meta TelemetryMeta) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.telemetry = telemetry
	r.seen = true
	r.seenAt = time.Now()
	r.source = source
	r.meta = meta
}

// SetFrame stores (or clears, with nil) the full LMU telemetry frame — every
// car's complete telemetry plus session globals. The mapped player car still
// flows through SetTelemetry for the overlay/MOZA/dashboard; this retains the
// rest so it is available to the frontend via GetLatestFrame.
func (r *Runtime) SetFrame(frame *wire.Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frame = frame
}

// LatestFrame returns the most recent full LMU frame, or nil when the current
// source is Forza or no LMU frame has arrived.
func (r *Runtime) LatestFrame() *wire.Frame {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.frame
}

// ObserveFrame feeds one decoded LMU frame to the strategy engine. The receiver
// calls it once per frame (alongside SetFrame), so the engine sees telemetry at
// the sidecar's full rate — necessary for the per-corner accumulation it gains in
// a later phase. The engineer has its own lock, so this needs no runtime lock.
func (r *Runtime) ObserveFrame(frame *wire.Frame) {
	r.engineer.Observe(frame)
}

// EngineerState returns the strategy engine's latest game-agnostic SessionState
// for the Strategy Planner frontend.
func (r *Runtime) EngineerState() engineer.SessionState {
	return r.engineer.Snapshot()
}

// SetCompareCar selects the rival whose driven line the engine buffers for the
// Driver Vs. line overlay. Passes through to the engine (own lock).
func (r *Runtime) SetCompareCar(id int32) {
	r.engineer.SetCompareCar(id)
}

// HasStore reports whether local persistence is available.
func (r *Runtime) HasStore() bool { return r.store != nil }

// EngineerContext returns the latest frame's track/car/class, used by the
// reference supervisor to detect a context change.
func (r *Runtime) EngineerContext() (track, car, class string) {
	return r.engineer.CurrentContext()
}

// LoadReference loads the stored reference lap + corner names for a track+car
// into the engine (or clears the reference when none is stored). Called when the
// context changes. Best-effort: persistence errors are logged, never fatal.
func (r *Runtime) LoadReference(track, car, class string) {
	if r.store == nil {
		return
	}
	ref, ok, err := r.store.GetReferenceLap(track, car)
	if err != nil {
		log.Printf("store: load reference: %v", err)
	}
	if ok {
		var sectors []engineer.MiniSectorState
		var path []engineer.Vec2
		_ = json.Unmarshal([]byte(ref.Sectors), &sectors)
		_ = json.Unmarshal([]byte(ref.Path), &path)
		r.engineer.SetReference(track, car, ref.Class, ref.LapTime, sectors, path)
	} else {
		r.engineer.SetReference(track, car, class, 0, nil, nil)
	}
	if labelsJSON, ok, err := r.store.GetCorners(track); err != nil {
		log.Printf("store: load corners: %v", err)
	} else if ok {
		var labels []string
		_ = json.Unmarshal([]byte(labelsJSON), &labels)
		r.engineer.SetCorners(track, labels)
	}
}

// PersistDirty saves any reference lap / corner labels the engine has newly
// produced (a beaten PB). Cheap when nothing changed.
func (r *Runtime) PersistDirty() {
	if r.store == nil {
		return
	}
	if data, ok := r.engineer.TakeDirtyReference(); ok {
		sectors, _ := json.Marshal(data.Sectors)
		path, _ := json.Marshal(data.Path)
		if err := r.store.SaveReferenceLap(store.ReferenceLap{
			Track: data.Track, Car: data.Car, Class: data.Class,
			LapTime: data.Time, Sectors: string(sectors), Path: string(path),
		}); err != nil {
			log.Printf("store: save reference: %v", err)
		}
	}
	if track, labels, ok := r.engineer.TakeDirtyCorners(); ok {
		b, _ := json.Marshal(labels)
		if err := r.store.SaveCorners(track, string(b)); err != nil {
			log.Printf("store: save corners: %v", err)
		}
	}
}

// StartSession opens a session row for the history log, returning its id (0 when
// no store).
func (r *Runtime) StartSession(track, car string) int64 {
	if r.store == nil {
		return 0
	}
	id, err := r.store.StartSession(track, car)
	if err != nil {
		log.Printf("store: start session: %v", err)
		return 0
	}
	return id
}

// UpdateSession records running progress for a session.
func (r *Runtime) UpdateSession(id int64, laps int, bestLap float64) {
	if r.store == nil || id == 0 {
		return
	}
	if err := r.store.UpdateSession(id, laps, bestLap); err != nil {
		log.Printf("store: update session: %v", err)
	}
}

// ListSessions returns recent session history (empty when no store).
func (r *Runtime) ListSessions(limit int) []store.SessionRow {
	if r.store == nil {
		return nil
	}
	rows, err := r.store.ListSessions(limit)
	if err != nil {
		log.Printf("store: list sessions: %v", err)
	}
	return rows
}

// IndexLatestRecording records metadata for the most recently written recording
// file, tagging it with the current track/car/source. Called after a recording
// stops.
func (r *Runtime) IndexLatestRecording(track, car, source string) {
	if r.store == nil || r.recorder == nil {
		return
	}
	infos, err := r.recorder.List()
	if err != nil || len(infos) == 0 {
		return
	}
	// List() returns newest first (see recording.Manager.List); index entry 0.
	latest := infos[0]
	if err := r.store.UpsertRecording(store.RecordingRow{
		Name: latest.Name, Track: track, Car: car, Source: source,
		RecordedAt: latest.Modified.Unix(), SizeBytes: latest.Size,
	}); err != nil {
		log.Printf("store: index recording: %v", err)
	}
}

// ListIndexedRecordings returns the recordings index (empty when no store).
func (r *Runtime) ListIndexedRecordings() []store.RecordingRow {
	if r.store == nil {
		return nil
	}
	rows, err := r.store.ListRecordings()
	if err != nil {
		log.Printf("store: list recordings: %v", err)
	}
	return rows
}

func (r *Runtime) PrintEvery() time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return time.Duration(float64(time.Second) / r.cfg.PrintHz)
}

func (r *Runtime) TerminalPrintEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.Terminal.Enabled
}

func (r *Runtime) MozaEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.moza != nil
}

func (r *Runtime) UpdateMozaRPM(currentRPM, maxRPM float32) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	driver := r.moza
	if driver == nil {
		return nil
	}
	return driver.UpdateRPM(currentRPM, maxRPM)
}

func (r *Runtime) ApplyConfig(next config.Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if next.ListenAddr != r.cfg.ListenAddr || next.ListenPort != r.cfg.ListenPort {
		return fmt.Errorf("listen_addr and listen_port changes require restarting the process")
	}
	if next.Recording.Dir != r.cfg.Recording.Dir {
		return fmt.Errorf("recording.dir changes require restarting the process")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := r.applyMoza(next.Moza, &next); err != nil {
		return err
	}
	r.cfg = next
	return nil
}

func (r *Runtime) SaveConfig(next config.Config) error {
	if err := next.Validate(); err != nil {
		return err
	}
	return config.Save(r.cfgPath, next)
}

func (r *Runtime) PreviewMoza(next config.Moza) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	driver := r.moza
	if driver == nil {
		return fmt.Errorf("MOZA preview requires an active MOZA driver")
	}
	if next.UpdateHz <= 0 {
		next.UpdateHz = 20
	}
	return driver.Apply(mozaOptionsFromConfig(next))
}

func (r *Runtime) RecordPacket(packet []byte, at time.Time) error {
	if r.recorder == nil {
		return nil
	}
	return r.recorder.Record(packet, at)
}

func (r *Runtime) StartRecording() (recording.Status, error) {
	if r.recorder == nil {
		return recording.Status{}, fmt.Errorf("recording is not available")
	}
	// Label the file with the game currently sending telemetry so recordings are
	// distinguishable; falls back to a neutral label before the first packet.
	r.mu.RLock()
	label := r.source
	r.mu.RUnlock()
	return r.recorder.Start(label)
}

func (r *Runtime) StopRecording() (recording.Status, error) {
	if r.recorder == nil {
		return recording.Status{}, nil
	}
	return r.recorder.Stop()
}

func (r *Runtime) RecordingStatus() recording.Status {
	if r.recorder == nil {
		return recording.Status{}
	}
	return r.recorder.Status()
}

func (r *Runtime) ListRecordings() ([]recording.Info, error) {
	if r.recorder == nil {
		return nil, fmt.Errorf("recording is not available")
	}
	return r.recorder.List()
}

func (r *Runtime) ReplayRecording(name string, maxSamples int) ([]ReplaySample, error) {
	if r.recorder == nil {
		return nil, fmt.Errorf("recording is not available")
	}

	rawSamples, err := r.recorder.Read(name, maxSamples)
	if err != nil {
		return nil, err
	}

	samples := make([]ReplaySample, 0, len(rawSamples))
	var re wire.Reassembler
	for _, raw := range rawSamples {
		// New-format LMU recordings store chunked binary frames; feed the chunks
		// through the same reassembler the live receiver uses and decode a frame
		// once it completes.
		if wire.IsEnvelope(raw.Packet) {
			payload, complete, err := re.Add(raw.Packet)
			if err != nil || !complete {
				continue
			}
			frame, err := wire.UnmarshalFrame(payload)
			if err != nil {
				continue
			}
			samples = append(samples, ReplaySample{
				OffsetMS:  raw.OffsetMS,
				Telemetry: frameToTelemetry(&frame),
				Source:    "lmu",
				Meta:      frameToMeta(&frame),
			})
			continue
		}
		// Forza (binary) and legacy LMU (JSON) packets are whole; decode by content
		// so older recordings of either game still replay through the same path.
		telemetry, source, meta, err := decodePacket(raw.Packet)
		if err != nil {
			// Skip a frame we can't decode rather than failing the whole replay;
			// a single corrupt or unknown packet shouldn't abort a review.
			continue
		}
		samples = append(samples, ReplaySample{
			OffsetMS:  raw.OffsetMS,
			Telemetry: telemetry,
			Source:    source,
			Meta:      meta,
		})
	}
	return samples, nil
}

// AnalyzeRecording replays a recording and runs the coaching analysis over it,
// returning a per-lap scorecard and a list of detected events. maxSamples caps
// the number of frames read (0 = all); pass 0 for a full-session review.
func (r *Runtime) AnalyzeRecording(name string, maxSamples int) (analysis.Report, error) {
	samples, err := r.ReplayRecording(name, maxSamples)
	if err != nil {
		return analysis.Report{}, err
	}
	frames := make([]analysis.Frame, len(samples))
	for i, s := range samples {
		frames[i] = analysis.Frame{OffsetMS: s.OffsetMS, Telemetry: s.Telemetry}
	}
	return analysis.Analyze(name, frames), nil
}

func (r *Runtime) Close() {
	if r.recorder != nil {
		if _, err := r.recorder.Stop(); err != nil {
			log.Printf("recording stop: %v", err)
		}
	}

	r.mu.Lock()
	driver := r.moza
	r.moza = nil
	r.mu.Unlock()
	if driver != nil {
		if err := driver.Close(); err != nil {
			log.Printf("moza close: %v", err)
		}
	}

	if r.store != nil {
		if err := r.store.Close(); err != nil {
			log.Printf("store close: %v", err)
		}
	}
}

// ApplyMoza applies the given MOZA configuration, (re)opening or closing the
// driver as needed. It is used at startup to honour cfg.Moza.Enabled.
func (r *Runtime) ApplyMoza(next config.Moza) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applyMoza(next, nil)
}

func (r *Runtime) applyMoza(next config.Moza, staged *config.Config) error {
	if !next.Enabled {
		if r.moza != nil {
			if err := r.moza.Close(); err != nil {
				return err
			}
			r.moza = nil
		}
		return nil
	}

	if staged != nil {
		staged.Moza = next
		if err := staged.Validate(); err != nil {
			return err
		}
	}

	options := mozaOptionsFromConfig(next)
	// Reconfigure an existing driver on the same port in place.
	if r.moza != nil && r.cfg.Moza.Port == next.Port {
		if err := r.moza.Apply(options); err != nil {
			// The device write failed (likely unplugged). Drop the driver; the
			// reconnect supervisor will reopen it when the wheel returns.
			log.Printf("moza: apply failed, will reconnect when the wheel is available: %v", err)
			_ = r.moza.Close()
			r.moza = nil
		}
		return nil
	}
	// New driver or a port change: (re)open the device.
	if r.moza != nil {
		if err := r.moza.Close(); err != nil {
			return err
		}
		r.moza = nil
	}
	driver, err := moza.NewDriver(options)
	if err != nil {
		// The wheel is off or not connected yet. This is not fatal: keep the
		// app running and let the reconnect supervisor retry. Returning nil also
		// means a config Apply with MOZA enabled but the wheel off still applies
		// the rest of the configuration.
		log.Printf("moza: not connected, will retry when the wheel is available: %v", err)
		r.mozaWarned = true
		return nil
	}
	r.mozaWarned = false
	r.moza = driver
	return nil
}

// reconnectMoza is called periodically by the MOZA supervisor. When MOZA is
// enabled in config but no driver is currently connected, it tries to (re)open
// the device. This is what lets the app start with the wheel off — or recover
// from the wheel being unplugged before any telemetry arrived — and connect
// automatically once the wheel is powered on. A connected driver heals transient
// USB errors on its own (see moza/reconnect.go), so this is a no-op then.
func (r *Runtime) reconnectMoza() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.cfg.Moza.Enabled || r.moza != nil {
		return
	}
	driver, err := moza.NewDriver(mozaOptionsFromConfig(r.cfg.Moza))
	if err != nil {
		if !r.mozaWarned {
			log.Printf("moza: waiting for wheel on %s: %v", r.cfg.Moza.Port, err)
			r.mozaWarned = true
		}
		return
	}
	log.Printf("moza: connected on %s", r.cfg.Moza.Port)
	r.mozaWarned = false
	r.moza = driver
}

func mozaOptionsFromConfig(cfg config.Moza) moza.Options {
	return moza.Options{
		Port:          cfg.Port,
		UpdateHz:      cfg.UpdateHz,
		RPMBrightness: cfg.RPMBrightness,
		RPMColors:     mozaColorsFromConfig(cfg.RPMColors),
		ButtonColors:  mozaColorsFromConfig(cfg.ButtonColors),
		ButtonMask:    cfg.ButtonMask,
	}
}

func mozaColorsFromConfig(colors [10]config.Color) [10]moza.RGB {
	var out [10]moza.RGB
	for i, color := range colors {
		out[i] = moza.RGB{R: color[0], G: color[1], B: color[2]}
	}
	return out
}
