package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"telemetry-handler/config"
	"telemetry-handler/forza"
	"telemetry-handler/output"
	"telemetry-handler/overlay"
	"telemetry-handler/receiver"
	"telemetry-handler/recording"
)

// Service is the Wails-bound surface of the application. Its exported methods
// are exposed to the React frontend as generated TypeScript bindings, mirroring
// the JSON API the web server used to provide. It also owns the UDP receiver
// loop and the overlay lifecycle.
type Service struct {
	runtime *Runtime
	overlay *overlay.Manager
	ctx     context.Context
}

// OverlayStatus reports whether the native overlay is currently running.
type OverlayStatus struct {
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

	if cfg.Overlay.Enabled {
		if err := s.overlay.Start(ctx, cfg.Overlay, s.telemetrySource()); err != nil {
			log.Printf("overlay start: %v", err)
		}
	}

	go s.runReceiver(ctx, cfg)
	return nil
}

// ServiceShutdown is invoked by Wails during application shutdown.
func (s *Service) ServiceShutdown() error {
	s.overlay.Stop()
	s.runtime.Close()
	return nil
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

// SetOverlayEnabled toggles the native telemetry overlay on or off at runtime.
func (s *Service) SetOverlayEnabled(enabled bool) error {
	if !enabled {
		s.overlay.Stop()
		return nil
	}
	if s.ctx == nil {
		return fmt.Errorf("overlay is not ready yet")
	}
	cfg := s.runtime.Config()
	return s.overlay.Start(s.ctx, cfg.Overlay, s.telemetrySource())
}

func (s *Service) GetOverlayStatus() OverlayStatus {
	return OverlayStatus{Running: s.overlay.Running()}
}
