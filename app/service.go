package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"telemetry-handler/analysis"
	"telemetry-handler/config"
	"telemetry-handler/forza"
	"telemetry-handler/lmu"
	"telemetry-handler/output"
	"telemetry-handler/overlay"
	"telemetry-handler/receiver"
	"telemetry-handler/recording"
)

const (
	// overlayReconcileEvery is how often the supervisor checks whether the
	// overlay should be shown or hidden based on telemetry flow.
	overlayReconcileEvery = 500 * time.Millisecond
	// overlayShowWithin shows the overlay when a telemetry packet arrived within
	// this window (the game is running and sending). overlayHideAfter tears it
	// down once packets have stopped for this long. The gap between the two
	// provides hysteresis so a brief stall does not flap the native window.
	overlayShowWithin = 2 * time.Second
	overlayHideAfter  = 5 * time.Second

	// mozaReconnectEvery is how often the supervisor retries opening the MOZA
	// wheel while it is enabled but not connected (e.g. powered on after the app
	// started).
	mozaReconnectEvery = 3 * time.Second
)

// Service is the Wails-bound surface of the application. Its exported methods
// are exposed to the React frontend as generated TypeScript bindings, mirroring
// the JSON API the web server used to provide. It also owns the UDP receiver
// loop and the overlay lifecycle.
type Service struct {
	runtime *Runtime
	overlay *overlay.Manager
	ctx     context.Context

	// overlayDesired is the user's on/off intent (the UI toggle). The actual
	// native window is started/stopped by the supervisor only while the game is
	// also sending telemetry.
	overlayDesired atomic.Bool
	// overlayMu serializes reconcile so the periodic supervisor and an explicit
	// SetOverlayEnabled call cannot interleave start/stop decisions.
	overlayMu sync.Mutex
}

// OverlayStatus reports the overlay's desired (user toggle) and actual
// (native window) state. Enabled without Running means the overlay is on but
// waiting for the game to start sending telemetry.
type OverlayStatus struct {
	Enabled bool `json:"enabled"`
	Running bool `json:"running"`
}

// MonitorInfo reports the logical resolution of the monitor the overlay will
// appear on, so the dashboard can render an accurate placement preview. When
// Detected is false the resolution was not auto-detectable (e.g. not running
// under Hyprland) and the UI should let the user pick a resolution manually.
type MonitorInfo struct {
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Name     string `json:"name"`
	Detected bool   `json:"detected"`
}

func NewService(runtime *Runtime) *Service {
	return &Service{
		runtime: runtime,
		overlay: overlay.NewManager(),
	}
}

func (s *Service) ServiceName() string {
	return "telemetry-handler"
}

// ServiceStartup is invoked by Wails during application startup. It applies the
// initial MOZA configuration, starts the overlay if enabled, and launches the
// UDP receiver loop. The provided context is cancelled right before shutdown.
func (s *Service) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	s.ctx = ctx
	cfg := s.runtime.Config()

	if cfg.Moza.Enabled {
		// A missing/powered-off wheel is no longer fatal: ApplyMoza logs and the
		// supervisor (below) keeps retrying so the app starts and connects later.
		if err := s.runtime.ApplyMoza(cfg.Moza); err != nil {
			log.Printf("moza: %v", err)
		}
	}

	s.overlayDesired.Store(cfg.Overlay.Enabled)
	go s.superviseOverlay(ctx)
	go s.superviseMoza(ctx)

	go s.runReceiver(ctx, cfg)
	return nil
}

// ServiceShutdown is invoked by Wails during application shutdown.
func (s *Service) ServiceShutdown() error {
	s.overlayDesired.Store(false)
	s.overlay.Stop()
	s.runtime.Close()
	return nil
}

// superviseOverlay periodically reconciles the native overlay against the
// user's intent and live telemetry: it is shown only while the overlay is
// enabled AND the game is sending telemetry, and torn down when either stops.
func (s *Service) superviseOverlay(ctx context.Context) {
	ticker := time.NewTicker(overlayReconcileEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileOverlay(ctx)
		}
	}
}

// superviseMoza periodically retries connecting the MOZA wheel while it is
// enabled but not connected, so the app can start with the wheel off and pick it
// up once it is powered on.
func (s *Service) superviseMoza(ctx context.Context) {
	ticker := time.NewTicker(mozaReconnectEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runtime.reconnectMoza()
		}
	}
}

// reconcileOverlay starts or stops the overlay window to match the desired
// state and current telemetry presence. It is safe to call concurrently.
func (s *Service) reconcileOverlay(ctx context.Context) {
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()

	running := s.overlay.Running()

	if !s.overlayDesired.Load() {
		if running {
			s.overlay.Stop()
		}
		return
	}

	switch present := s.gamePresent(running); {
	case present && !running:
		cfg := s.runtime.Config()
		if err := s.overlay.Start(ctx, cfg.Overlay, s.telemetrySource()); err != nil {
			log.Printf("overlay start: %v", err)
		}
	case !present && running:
		s.overlay.Stop()
	}
}

// gamePresent reports whether telemetry is flowing, with hysteresis: once the
// overlay is shown it stays up until packets have been absent for longer, so a
// momentary stall does not flap the native window.
func (s *Service) gamePresent(running bool) bool {
	snap := s.runtime.LatestTelemetry()
	if !snap.Available || snap.ReceivedAt.IsZero() {
		return false
	}
	age := time.Since(snap.ReceivedAt)
	if running {
		return age <= overlayHideAfter
	}
	return age <= overlayShowWithin
}

func (s *Service) telemetrySource() overlay.Source {
	return func() (forza.Telemetry, bool, time.Time, float64) {
		snap := s.runtime.LatestTelemetry()
		return snap.Telemetry, snap.Available, snap.ReceivedAt, snap.Meta.SteeringRangeDeg
	}
}

func (s *Service) runReceiver(ctx context.Context, cfg config.Config) {
	addr := fmt.Sprintf("%s:%d", cfg.ListenAddr, cfg.ListenPort)
	formatter := output.NewFormatter()
	var lastPrint time.Time
	var badSizes uint64

	// apply pushes one telemetry frame to every consumer: the shared snapshot
	// (overlay + dashboard), MOZA RPM lighting, and optional terminal output.
	// source identifies the game ("forza"/"lmu") so the dashboard can tailor
	// which tabs and readouts it shows.
	apply := func(t forza.Telemetry, source string, meta TelemetryMeta) error {
		s.runtime.SetTelemetry(t, source, meta)
		if s.runtime.MozaEnabled() {
			currentRPM := t.CurrentEngineRpm
			if t.IsRaceOn == 0 {
				currentRPM = 0
			}
			// MOZA RPM lighting is a best-effort side effect. A transient USB
			// serial hiccup (EIO on /dev/ttyACM*) must not tear down the whole
			// telemetry receiver — log it and keep feeding dashboard/overlay.
			if err := s.runtime.UpdateMozaRPM(currentRPM, t.EngineMaxRpm); err != nil {
				log.Printf("moza: rpm update failed: %v", err)
			}
		}
		if s.runtime.TerminalPrintEnabled() {
			now := time.Now()
			if lastPrint.IsZero() || now.Sub(lastPrint) >= s.runtime.PrintEvery() {
				lastPrint = now
				fmt.Println(formatter.Format(t))
			}
		}
		return nil
	}

	log.Printf("listening for telemetry on %s", addr)
	err := receiver.Listen(ctx, addr, func(_ context.Context, packet []byte) error {
		// One UDP port carries both Forza's fixed-size binary packets and the
		// lmu-bridge sidecar's JSON; demultiplex by content (JSON starts '{').
		if lmu.LooksLikePacket(packet) {
			p, err := lmu.Parse(packet)
			if err != nil {
				log.Printf("ignored malformed lmu packet: %v", err)
				return nil
			}
			return apply(lmuToTelemetry(p), "lmu", lmuToMeta(p))
		}

		telemetry, err := forza.ParseFH6Packet(packet)
		if err != nil {
			var sizeErr *forza.PacketSizeError
			if errors.As(err, &sizeErr) {
				badSizes++
				log.Printf("ignored packet with unexpected size got=%d want=%d total_bad=%d", sizeErr.Got, sizeErr.Want, badSizes)
				return nil
			}
			return err
		}

		// Only Forza's raw packets are recorded for now — recordings replay
		// through the FH6 parser, so LMU needs its own format (future work).
		if err := s.runtime.RecordPacket(packet, time.Now()); err != nil {
			return err
		}
		return apply(telemetry, "forza", TelemetryMeta{})
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("receiver: %v", err)
	}
}

// Bound methods exposed to the frontend.

func (s *Service) GetConfig() config.Config {
	return s.runtime.Config()
}

// ConfigStatus reports whether the config file failed to load at startup. When
// Error is non-empty the app is running on default settings and the dashboard
// shows a warning banner.
type ConfigStatus struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

func (s *Service) GetConfigStatus() ConfigStatus {
	path, msg := s.runtime.LoadError()
	return ConfigStatus{Path: path, Error: msg}
}

func (s *Service) GetTelemetry() TelemetrySnapshot {
	return s.runtime.LatestTelemetry()
}

func (s *Service) ApplyConfig(cfg config.Config) (config.Config, error) {
	if err := s.runtime.ApplyConfig(cfg); err != nil {
		return config.Config{}, err
	}
	// The overlay reads its geometry/appearance config only when started, so a
	// running overlay must be restarted to pick up placement/size/opacity edits.
	if s.ctx != nil && s.overlay.Running() {
		s.overlay.Stop()
		s.reconcileOverlay(s.ctx)
	}
	return s.runtime.Config(), nil
}

func (s *Service) SaveConfig(cfg config.Config) error {
	return s.runtime.SaveConfig(cfg)
}

func (s *Service) PreviewMoza(moza config.Moza) error {
	return s.runtime.PreviewMoza(moza)
}

func (s *Service) GetRecordingStatus() recording.Status {
	return s.runtime.RecordingStatus()
}

func (s *Service) StartRecording() (recording.Status, error) {
	return s.runtime.StartRecording()
}

func (s *Service) StopRecording() (recording.Status, error) {
	return s.runtime.StopRecording()
}

func (s *Service) ListRecordings() ([]recording.Info, error) {
	return s.runtime.ListRecordings()
}

func (s *Service) ReplayRecording(name string, maxSamples int) ([]ReplaySample, error) {
	return s.runtime.ReplayRecording(name, maxSamples)
}

// AnalyzeRecording replays a saved recording and returns coaching analysis: a
// per-lap scorecard plus a list of detected events. Pass maxSamples 0 to analyze
// the whole recording.
func (s *Service) AnalyzeRecording(name string, maxSamples int) (analysis.Report, error) {
	return s.runtime.AnalyzeRecording(name, maxSamples)
}

// SetOverlayEnabled toggles the user's intent to show the native telemetry
// overlay. The window itself only appears while the game is sending telemetry;
// enabling it before the game starts simply arms it to show automatically.
func (s *Service) SetOverlayEnabled(enabled bool) error {
	s.overlayDesired.Store(enabled)
	if s.ctx == nil {
		if enabled {
			return fmt.Errorf("overlay is not ready yet")
		}
		return nil
	}
	s.reconcileOverlay(s.ctx)
	return nil
}

func (s *Service) GetOverlayStatus() OverlayStatus {
	return OverlayStatus{Enabled: s.overlayDesired.Load(), Running: s.overlay.Running()}
}

// GetMonitorInfo reports the logical resolution of the monitor the overlay will
// appear on, used by the dashboard placement preview.
func (s *Service) GetMonitorInfo() MonitorInfo {
	w, h, name, ok := overlay.Monitor(s.runtime.Config().Overlay)
	return MonitorInfo{Width: w, Height: h, Name: name, Detected: ok}
}

// ListMonitors returns the names of all connected monitors for the overlay
// output dropdown (empty when enumeration is unavailable, e.g. non-Hyprland).
func (s *Service) ListMonitors() []string {
	return overlay.Monitors()
}
