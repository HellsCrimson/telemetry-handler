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
	"telemetry-handler/game/lmu/rest"
	"telemetry-handler/game/lmu/wire"
	"telemetry-handler/recording"
	"telemetry-handler/store"
	"telemetry-handler/wheelbase/moza"
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

// MozaStatus reports the MOZA wheel's live state to the dashboard (polled like
// the overlay/recording status). Enabled is the config intent; Connected is
// whether a driver is actually open. Model/Serial/RPMLEDs are populated from USB
// detection when the connected port maps to a known device.
type MozaStatus struct {
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	Port      string `json:"port"`
	Model     string `json:"model"`
	Serial    string `json:"serial"`
	RPMLEDs   int    `json:"rpm_leds"`
	// Wheel is the attached rim's model code (e.g. "ES", "KS"), read over serial;
	// empty when no rim answered. Protocol is the effective LED protocol in use
	// ("new" or "old") once connected.
	Wheel    string `json:"wheel"`
	Protocol string `json:"protocol"`
}

// Runtime holds the shared, mutex-protected application state: the latest
// telemetry snapshot, the active configuration, the optional MOZA driver and
// the recording manager. It is the single source of truth consumed by the UDP
// receiver loop, the Wails-bound service and the overlay.
type Runtime struct {
	mu           sync.RWMutex
	cfg          config.Config
	cfgPath      string
	telemetry    forza.Telemetry
	seen         bool
	seenAt       time.Time
	source       string
	meta         TelemetryMeta
	frame        *wire.Frame        // full LMU frame (all cars + globals); nil for Forza
	restData     *rest.Snapshot     // latest LMU REST poll (strategy/forecast/pit extras); nil until polled
	engineer     *engineer.Engineer // live strategy engine (multi-car SessionState)
	store        *store.Store       // local persistence (reference laps, corners, sessions, recordings); nil if unavailable
	moza         *moza.Driver
	mozaDevice   moza.Device   // USB identity of the connected wheel (zero when disconnected or undetectable)
	mozaProtocol moza.Protocol // effective LED protocol of the connected driver (for status display)
	recorder     *recording.Manager

	// mozaWarned dedupes the "waiting for wheel" log so the reconnect
	// supervisor does not spam it every tick while the wheel is absent.
	mozaWarned bool

	// loadErrPath/loadErr record a config-file load failure so the dashboard can
	// surface that the app fell back to default settings.
	loadErrPath string
	loadErr     string

	// voiceNotice is the transient message the voice assistant wants the overlay
	// to show (e.g. a staged pit change awaiting confirmation). It expires at
	// voiceNoticeUntil so a stale prompt does not linger on screen.
	voiceNoticeText  string
	voiceNoticeLevel int
	voiceNoticeUntil time.Time
}

// SetVoiceNotice records a transient overlay message from the voice assistant.
// level is one of voice's Level* constants; ttl is how long it stays up.
func (r *Runtime) SetVoiceNotice(text string, level int, ttl time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.voiceNoticeText = text
	r.voiceNoticeLevel = level
	r.voiceNoticeUntil = time.Now().Add(ttl)
}

// VoiceNotice returns the current voice message and its level, or "" once it has
// expired. Read by the overlay each frame.
func (r *Runtime) VoiceNotice() (string, int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.voiceNoticeText == "" || time.Now().After(r.voiceNoticeUntil) {
		return "", 0
	}
	return r.voiceNoticeText, r.voiceNoticeLevel
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

// SetRESTData stores the latest LMU REST snapshot (strategy/forecast/pit extras)
// and folds the live-session bits into the strategy engine so they surface on the
// SessionState the frontend already polls. A nil or unavailable snapshot clears
// the engine's REST overlay. Called by the REST poller off the hot frame path.
func (r *Runtime) SetRESTData(snap *rest.Snapshot) {
	r.mu.Lock()
	r.restData = snap
	r.mu.Unlock()
	// Map the LMU-specific REST shape to the game-agnostic strategy overlay here
	// (the app is the bridge); the engine stays free of the rest package. A nil or
	// unavailable snapshot maps to an empty (absent) overlay, clearing it.
	r.engineer.ApplyStrategy(restToStrategy(snap))
}

// RESTData returns the latest LMU REST snapshot, or nil if the API has not been
// polled (or LMU REST polling is disabled).
func (r *Runtime) RESTData() *rest.Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.restData
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

// MozaStatus reports the live state of the MOZA wheel for the dashboard: whether
// a driver is currently connected, the configured port, and — when the wheel was
// identified over USB — its model, serial and rev-light count. Connected without
// an identified model means the driver opened a port that USB detection could not
// match (e.g. a manual port override, or off Linux where detection is a stub).
func (r *Runtime) MozaStatus() MozaStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	leds := moza.ProfileFor(r.mozaDevice.ProductID, r.mozaDevice.Model).RPMLEDs
	if r.cfg.Moza.RPMLEDs > 0 {
		leds = r.cfg.Moza.RPMLEDs
	}
	port := r.mozaDevice.Port
	if port == "" {
		port = r.cfg.Moza.Port
	}
	protocol := ""
	if r.moza != nil {
		protocol = "old"
		if r.mozaProtocol == moza.ProtocolNew {
			protocol = "new"
		}
	}
	return MozaStatus{
		Enabled:   r.cfg.Moza.Enabled,
		Connected: r.moza != nil,
		Port:      port,
		Model:     r.mozaDevice.Model,
		Serial:    r.mozaDevice.Serial,
		RPMLEDs:   leds,
		Wheel:     r.mozaDevice.Wheel,
		Protocol:  protocol,
	}
}

// DetectMoza enumerates the MOZA wheels currently attached over USB, so the
// dashboard can show what is connected and let the user pick the serial port
// without typing it. Empty when none are attached (or off Linux).
func (r *Runtime) DetectMoza() []moza.Device {
	devices, err := moza.Detect()
	if err != nil {
		log.Printf("moza: detect: %v", err)
	}
	return devices
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

// TestMozaLights runs a brief rev-light sweep on the connected wheel so the user
// can confirm the LEDs work from the dashboard. The driver pointer is grabbed
// under the runtime lock and released before the (blocking) sweep, so live
// telemetry is not held up by the runtime mutex.
func (r *Runtime) TestMozaLights() error {
	r.mu.RLock()
	driver := r.moza
	r.mu.RUnlock()
	if driver == nil {
		return fmt.Errorf("MOZA wheel not connected")
	}
	return driver.TestLights()
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
	opts, _, ok := resolveMoza(next)
	if !ok {
		return fmt.Errorf("no MOZA wheel detected to preview")
	}
	return driver.Apply(opts)
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
	r.mozaDevice = moza.Device{}
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
		r.mozaDevice = moza.Device{}
		return nil
	}

	if staged != nil {
		staged.Moza = next
		if err := staged.Validate(); err != nil {
			return err
		}
	}

	options, dev, ok := resolveMoza(next)
	if !ok {
		// Enabled but nothing to open yet: no port configured and no wheel
		// detected. Not fatal — drop any stale driver and let the supervisor
		// connect once a wheel appears.
		if r.moza != nil {
			_ = r.moza.Close()
			r.moza = nil
		}
		r.mozaDevice = moza.Device{}
		r.mozaWarned = true
		return nil
	}
	// Reconfigure an existing driver on the same effective port in place.
	if r.moza != nil && r.mozaDevice.Port == options.Port {
		if err := r.moza.Apply(options); err != nil {
			// The device write failed (likely unplugged). Drop the driver; the
			// reconnect supervisor will reopen it when the wheel returns.
			log.Printf("moza: apply failed, will reconnect when the wheel is available: %v", err)
			_ = r.moza.Close()
			r.moza = nil
			r.mozaDevice = moza.Device{}
		} else {
			r.mozaDevice = dev
			r.mozaProtocol = options.Protocol
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
	r.mozaDevice = dev
	r.mozaProtocol = options.Protocol
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
	options, dev, ok := resolveMoza(r.cfg.Moza)
	if !ok {
		if !r.mozaWarned {
			log.Printf("moza: waiting for a wheel to be detected")
			r.mozaWarned = true
		}
		return
	}
	driver, err := moza.NewDriver(options)
	if err != nil {
		if !r.mozaWarned {
			log.Printf("moza: waiting for wheel on %s: %v", options.Port, err)
			r.mozaWarned = true
		}
		return
	}
	log.Printf("moza: connected on %s", options.Port)
	r.mozaWarned = false
	r.moza = driver
	r.mozaDevice = dev
	r.mozaProtocol = options.Protocol
}

// resolveMoza builds the driver options for a MOZA config, resolving both the
// serial port and the rev-light count. When cfg.Port is empty it auto-detects
// the first attached MOZA wheel, so a config may enable MOZA without naming a
// port. ok is false when MOZA is unusable right now (no port configured and no
// wheel detected) — a non-fatal "waiting for wheel" state, not an error.
func resolveMoza(cfg config.Moza) (opts moza.Options, dev moza.Device, ok bool) {
	port := cfg.Port
	if port != "" {
		// Resolve the device behind a configured port (for its identity/profile);
		// a non-match is fine — the port may be a manual override detection can't see.
		dev, _ = detectMozaPort(port)
	} else if devices, _ := moza.Detect(); len(devices) > 0 {
		dev = devices[0]
		port = dev.Port
	}
	if port == "" {
		return moza.Options{}, moza.Device{}, false
	}
	// Carry the effective port even when detection could not identify the device
	// (a manual port override), so status and the in-place-apply check are correct.
	dev.Port = port

	// The rev lights live on the rim, which USB cannot identify (only the base);
	// the base profile is a best guess. A non-zero cfg.RPMLEDs is the user's
	// manual rim override and wins.
	leds := moza.ProfileFor(dev.ProductID, dev.Model).RPMLEDs
	if cfg.RPMLEDs > 0 {
		leds = cfg.RPMLEDs
	}

	// Resolve protocol="auto" and read the rim model over serial (the rim is not
	// identifiable over USB). An explicit old/new is honoured; the model is still
	// probed so the dashboard can name the connected wheel.
	protocol, model := moza.ResolveWheel(moza.ParseProtocol(cfg.Protocol), port)
	if model != "" {
		dev.Wheel = model
	}

	return moza.Options{
		Port:          port,
		UpdateHz:      cfg.UpdateHz,
		RPMBrightness: cfg.RPMBrightness,
		RPMColors:     mozaColorsFromConfig(cfg.RPMColors),
		ButtonColors:  mozaColorsFromConfig(cfg.ButtonColors),
		ButtonMask:    cfg.ButtonMask,
		RPMLEDs:       leds,
		Protocol:      protocol,
		RPMCurve:      mozaCurveFromConfig(cfg),
	}, dev, true
}

// detectMozaPort returns the detected USB identity of the device at port, if
// MOZA detection finds a match. Used both to pick the lighting profile and to
// label the connected wheel in the dashboard status.
func detectMozaPort(port string) (moza.Device, bool) {
	if port == "" {
		return moza.Device{}, false
	}
	devices, err := moza.Detect()
	if err != nil {
		return moza.Device{}, false
	}
	for _, d := range devices {
		if d.Port == port {
			return d, true
		}
	}
	return moza.Device{}, false
}

func mozaColorsFromConfig(colors [10]config.Color) [10]moza.RGB {
	var out [10]moza.RGB
	for i, color := range colors {
		out[i] = moza.RGB{R: color[0], G: color[1], B: color[2]}
	}
	return out
}

// mozaCurveFromConfig maps the config's rev-light control points onto the moza
// driver's RPMCurve. Fewer than two points yields the zero value (linear).
func mozaCurveFromConfig(cfg config.Moza) moza.RPMCurve {
	if len(cfg.RPMCurvePoints) < 2 {
		return moza.RPMCurve{}
	}
	pts := make([]moza.CurvePoint, len(cfg.RPMCurvePoints))
	for i, p := range cfg.RPMCurvePoints {
		pts[i] = moza.CurvePoint{X: p.X, Y: p.Y}
	}
	return moza.RPMCurve{Points: pts}
}

// MozaBaseSnapshot is the read-only view of the wheelbase's stored configuration
// that the dashboard renders: what the device currently holds, what it declined
// to answer, and its temperatures.
//
// Values are keyed by the command keys in moza.BaseCommands(), which is also
// where their ranges, units and labels live — so the frontend builds its controls
// from the same source that validates the writes, and cannot offer a value the
// hardware would reject.
type MozaBaseSnapshot struct {
	// Available is false when there is no wheelbase to talk to. The rest of the
	// struct is then empty and Reason says why, so the page can explain itself
	// rather than showing a grid of zeroes.
	Available bool           `json:"available"`
	Reason    string         `json:"reason"`
	Settings  map[string]int `json:"settings"`
	// Temps are degrees Celsius. The base reports hundredths of a degree; the
	// conversion happens in moza so every consumer sees the same units.
	Temps map[string]float64 `json:"temps"`
	// State and Error are the base's raw state words, shown when the base
	// answered them. Their values are not decoded yet, but a non-zero error is
	// worth surfacing before we can name it.
	State    int  `json:"state"`
	Error    int  `json:"error"`
	HasState bool `json:"has_state"`
	HasError bool `json:"has_error"`
	// Unsupported names commands this base did not answer. They are shown as
	// unavailable rather than as a value, because an older base legitimately
	// implements only part of the command set.
	Unsupported map[string]bool `json:"unsupported"`
	// Writable reports whether the app is allowed to change these settings at all
	// (config `moza.allow_base_writes`). When false, WriteBlocked says why, and
	// the page renders as it did before writing existed — controls disabled with
	// a reason rather than hidden.
	Writable     bool   `json:"writable"`
	WriteBlocked string `json:"write_blocked"`
}

// ReadMozaBase reads the wheelbase's stored settings and status.
//
// It writes nothing. This is the milestone-2 surface: it proves the framing and
// checksums against real hardware, and lets the provisional ranges in the command
// registry be checked against Boxflat and Pit House, before anything is written
// to a device that stores what it is told.
//
// The read borrows the LED driver's connection when one is open, so a settings
// exchange can never interleave with a rev-light write; see moza.WithBase.
func (r *Runtime) ReadMozaBase() MozaBaseSnapshot {
	r.mu.RLock()
	driver, port := r.moza, r.mozaDevice.Port
	if port == "" {
		port = r.cfg.Moza.Port
	}
	r.mu.RUnlock()

	if driver == nil && port == "" {
		return MozaBaseSnapshot{Reason: "No MOZA serial port configured or detected."}
	}

	snap := MozaBaseSnapshot{
		Settings:    map[string]int{},
		Temps:       map[string]float64{},
		Unsupported: map[string]bool{},
	}
	snap.Writable, snap.WriteBlocked = r.mozaWritePermission()
	err := moza.WithBase(driver, port, func(c *moza.BaseClient) error {
		settings, err := c.ReadAllSettings()
		if err != nil {
			return err
		}
		snap.Settings = settings.Values
		snap.Unsupported = settings.Unsupported

		// Status is best-effort: the ids are unconfirmed, and a base that answers
		// none of them still has perfectly good settings worth showing.
		if status, err := c.ReadStatus(); err == nil {
			snap.Temps = status.Temps
			snap.State, snap.HasState = status.State, status.HasState
			snap.Error, snap.HasError = status.Error, status.HasError
			for key := range status.Unsupported {
				snap.Unsupported[key] = true
			}
		}
		return nil
	})
	if err != nil {
		return MozaBaseSnapshot{Reason: err.Error()}
	}
	snap.Available = true
	return snap
}

// MozaApplyResult is what came of a settings write, verified by reading the
// values back rather than by trusting the base's acknowledgements.
//
// A partial apply is a real outcome, not an exception: an older base may not
// implement one command in a patch, and the user has to be able to tell which of
// their changes are live and which are not.
type MozaApplyResult struct {
	// Ok is true only when every requested setting was written AND read back as
	// written.
	Ok bool `json:"ok"`
	// Refused is set when nothing was attempted — writes disabled, no wheelbase,
	// an unknown key, a value out of range. Nothing reached the hardware.
	Refused string `json:"refused"`
	// Applied lists the settings written, in the order they were written.
	Applied []string `json:"applied"`
	// Failed maps a setting to why its write failed. Strings rather than errors
	// because this crosses the bindings.
	Failed map[string]string `json:"failed"`
	// Mismatched names settings the base acknowledged but did not actually take.
	// This is what a dropped write looks like from the outside, and it is the
	// reason the result is read back at all.
	Mismatched map[string]int `json:"mismatched"`
	// Settings is a fresh read of everything after the write, so the page shows
	// the wheel's real state rather than what it hoped for.
	Settings map[string]int `json:"settings"`
	// Rewritten maps a setting to the one in the same patch that overwrites it as
	// a side effect. Applying still works — the ordering makes the explicit value
	// win — but the user asked for two things that interact and should be told.
	Rewritten map[string]string `json:"rewritten"`
}

// ApplyMozaBase writes settings to the wheelbase and reads them back.
//
// Three refusals happen before anything reaches the wire, because a patch that
// is half-applied and then rejected is worse than one never started:
//
//   - Writing is off unless the config says otherwise. These settings persist on
//     the hardware, so the app does not own them by default.
//   - A setting whose conversion is not confirmed on this hardware is refused.
//     An unverified scale means we do not know what number actually arrives, and
//     "write 20" landing as 200 on a torque cap is the accident worth designing
//     against.
//   - Values are validated against the registry, which is the same source the UI
//     built its controls from.
func (r *Runtime) ApplyMozaBase(patch map[string]int) MozaApplyResult {
	result := MozaApplyResult{
		Failed:     map[string]string{},
		Mismatched: map[string]int{},
		Settings:   map[string]int{},
	}
	if len(patch) == 0 {
		result.Refused = "Nothing to apply."
		return result
	}

	r.mu.RLock()
	allowed := r.cfg.Moza.AllowBaseWrites
	driver, port := r.moza, r.mozaDevice.Port
	if port == "" {
		port = r.cfg.Moza.Port
	}
	r.mu.RUnlock()

	if !allowed {
		result.Refused = "Writing wheelbase settings is disabled. Set moza.allow_base_writes in the config file to enable it."
		return result
	}
	if driver == nil && port == "" {
		result.Refused = "No MOZA serial port configured or detected."
		return result
	}

	settings := moza.SettingsPatch{}
	for key, value := range patch {
		cmd, ok := moza.LookupBaseCommand(key)
		if !ok {
			result.Refused = fmt.Sprintf("Unknown setting %q.", key)
			return result
		}
		if !cmd.Verified {
			result.Refused = fmt.Sprintf("%s cannot be written yet: its conversion is not confirmed on this hardware, "+
				"so the value that reaches the base may not be the one shown.", cmd.Name)
			return result
		}
		if err := cmd.Validate(value); err != nil {
			result.Refused = err.Error()
			return result
		}
		settings[key] = value
	}
	result.Rewritten = moza.Rewrites(settings)

	err := moza.WithBase(driver, port, func(c *moza.BaseClient) error {
		applied, err := c.ApplySettings(settings)
		if err != nil {
			return err
		}
		result.Applied = applied.Applied
		for key, err := range applied.Failed {
			result.Failed[key] = err.Error()
		}

		// Read everything back, not just what was written: a macro setting rewrites
		// values the user did not touch, and the page must show what the wheel now
		// holds rather than what it held before.
		snap, err := c.ReadAllSettings()
		if err != nil {
			return err
		}
		result.Settings = snap.Values
		for key, want := range settings {
			got, ok := snap.Values[key]
			if !ok || got != want {
				result.Mismatched[key] = got
			}
		}
		return nil
	})
	if err != nil {
		result.Refused = err.Error()
		return result
	}
	result.Ok = len(result.Failed) == 0 && len(result.Mismatched) == 0
	return result
}

// MozaBaseCommand is one wheelbase setting's metadata, as the frontend needs it.
// It mirrors moza.BaseCommand rather than exposing it directly, so the wire type
// stays stable if the internal one gains fields.
type MozaBaseCommand struct {
	Key    string   `json:"key"`
	Name   string   `json:"name"`
	Unit   string   `json:"unit"`
	Min    int      `json:"min"`
	Max    int      `json:"max"`
	Kind   string   `json:"kind"`
	Labels []string `json:"labels"`
	// Safety marks the settings written first by a grouped apply, so the UI can
	// group them together as the safety envelope.
	Safety bool `json:"safety"`
	// Verified is false while a command's range and scaling are still taken from
	// Boxflat's database rather than confirmed on this hardware. The UI should
	// say so rather than presenting a guess as fact.
	Verified bool   `json:"verified"`
	Note     string `json:"note"`
	// Affects names settings this one rewrites as a side effect — road sensitivity
	// is a macro on this firmware and moves the equalizer bands with it. The UI
	// needs this before the apply, not after, so it can say that changing one
	// control will move others the user can see on the same page.
	Affects []string `json:"affects"`
}

// mozaWritePermission reports whether settings may be written, and why not when
// they may not. The reason travels with the snapshot so the page can explain
// itself instead of showing controls that quietly do nothing.
func (r *Runtime) mozaWritePermission() (bool, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.cfg.Moza.AllowBaseWrites {
		return false, "Read-only: set moza.allow_base_writes in the config file to let the app change these settings."
	}
	return true, ""
}

// MozaBaseCommands returns the registry, so the frontend builds its controls from
// the same definitions that validate the writes.
func (r *Runtime) MozaBaseCommands() []MozaBaseCommand {
	commands := moza.BaseCommands()
	out := make([]MozaBaseCommand, 0, len(commands))
	for _, c := range commands {
		out = append(out, MozaBaseCommand{
			Key:      c.Key,
			Name:     c.Name,
			Unit:     c.Unit,
			Min:      c.Min,
			Max:      c.Max,
			Kind:     c.Kind.String(),
			Labels:   c.Labels,
			Safety:   moza.IsSafetyCommand(c.Key),
			Verified: c.Verified,
			Note:     c.Note,
			Affects:  c.Affects,
		})
	}
	return out
}
