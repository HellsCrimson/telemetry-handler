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

	"telemetry-handler/config"
	"telemetry-handler/forza"
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
		if err := s.runtime.ApplyMoza(cfg.Moza); err != nil {
			return fmt.Errorf("moza: %w", err)
		}
		log.Printf("moza enabled: port=%s update_hz=%.2f", cfg.Moza.Port, cfg.Moza.UpdateHz)
	}

	s.overlayDesired.Store(cfg.Overlay.Enabled)
	go s.superviseOverlay(ctx)

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
	return func() (forza.Telemetry, bool, time.Time) {
		snap := s.runtime.LatestTelemetry()
		return snap.Telemetry, snap.Available, snap.ReceivedAt
	}
}

func (s *Service) runReceiver(ctx context.Context, cfg config.Config) {
	addr := fmt.Sprintf("%s:%d", cfg.ListenAddr, cfg.ListenPort)
	formatter := output.NewFormatter()
	var lastPrint time.Time
	var badSizes uint64

	log.Printf("listening for telemetry on %s", addr)
	err := receiver.Listen(ctx, addr, func(_ context.Context, packet []byte) error {
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

		if err := s.runtime.RecordPacket(packet, time.Now()); err != nil {
			return err
		}

		s.runtime.SetTelemetry(telemetry)
		if s.runtime.MozaEnabled() {
			currentRPM := telemetry.CurrentEngineRpm
			if telemetry.IsRaceOn == 0 {
				currentRPM = 0
			}
			if err := s.runtime.UpdateMozaRPM(currentRPM, telemetry.EngineMaxRpm); err != nil {
				return err
			}
		}

		if s.runtime.TerminalPrintEnabled() {
			now := time.Now()
			if lastPrint.IsZero() || now.Sub(lastPrint) >= s.runtime.PrintEvery() {
				lastPrint = now
				fmt.Println(formatter.Format(telemetry))
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("receiver: %v", err)
	}
}

// Bound methods exposed to the frontend.

func (s *Service) GetConfig() config.Config {
	return s.runtime.Config()
}

func (s *Service) GetTelemetry() TelemetrySnapshot {
	return s.runtime.LatestTelemetry()
}

func (s *Service) ApplyConfig(cfg config.Config) (config.Config, error) {
	if err := s.runtime.ApplyConfig(cfg); err != nil {
		return config.Config{}, err
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
