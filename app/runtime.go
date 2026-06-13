package app

import (
	"fmt"
	"log"
	"sync"
	"time"

	"telemetry-handler/analysis"
	"telemetry-handler/config"
	"telemetry-handler/forza"
	"telemetry-handler/moza"
	"telemetry-handler/recording"
)

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
}

// ReplaySample is a single telemetry frame from a recording, offset from the
// start of the recording in milliseconds.
type ReplaySample struct {
	OffsetMS  uint64          `json:"offset_ms"`
	Telemetry forza.Telemetry `json:"telemetry"`
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

func NewRuntime(cfg config.Config, cfgPath string, recorder *recording.Manager) *Runtime {
	return &Runtime{cfg: cfg, cfgPath: cfgPath, recorder: recorder}
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
	}
}

// SetTelemetry records the latest frame and the game it came from (source is
// "forza" or "lmu"), used by the dashboard to tailor its layout per game.
func (r *Runtime) SetTelemetry(telemetry forza.Telemetry, source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.telemetry = telemetry
	r.seen = true
	r.seenAt = time.Now()
	r.source = source
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
	return r.recorder.Start()
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
	for _, raw := range rawSamples {
		telemetry, err := forza.ParseFH6Packet(raw.Packet)
		if err != nil {
			return nil, err
		}
		samples = append(samples, ReplaySample{
			OffsetMS:  raw.OffsetMS,
			Telemetry: telemetry,
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
