package overlay

import (
	"context"
	"sync"
	"time"

	"telemetry-handler/config"
	"telemetry-handler/forza"
)

const staleAfter = 2 * time.Second

type Backend interface {
	Run(context.Context, config.Overlay, <-chan HUD) error
}

// Source provides the latest telemetry snapshot for the overlay to render.
// It is polled in-process (no HTTP) at the overlay's configured update rate.
type Source func() (telemetry forza.Telemetry, available bool, receivedAt time.Time)

// Manager owns the lifecycle of a single running overlay so it can be toggled
// on and off at runtime from the UI.
type Manager struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewManager() *Manager {
	return &Manager{}
}

// Running reports whether an overlay is currently active.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cancel != nil
}

// Start launches the overlay bound to parent's lifetime. It is a no-op if an
// overlay is already running. The overlay config is validated (with defaults
// filled in) before the backend is created.
func (m *Manager) Start(parent context.Context, ov config.Overlay, source Source) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return nil
	}

	ov = ov.WithDefaults()
	if err := ov.Validate(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	m.cancel = cancel
	m.done = done

	go func() {
		defer close(done)
		_ = run(ctx, ov, source)
		m.mu.Lock()
		m.cancel = nil
		m.done = nil
		m.mu.Unlock()
	}()
	return nil
}

// Stop cancels the running overlay and waits for it to exit. It is safe to call
// when no overlay is running.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

func run(ctx context.Context, ov config.Overlay, source Source) error {
	// Resolve which monitor to display on. When the configured output is empty
	// or "auto" this auto-detects the game window's monitor (Hyprland); a
	// concrete output name is respected as a manual override.
	ov = resolveOutput(ov)

	updates := make(chan HUD, 1)
	telemetry, available, receivedAt := source()
	updates <- FormatHUD(telemetry, available, receivedAt, time.Now())

	errc := make(chan error, 1)
	go func() {
		errc <- newBackend().Run(ctx, ov, updates)
	}()

	ticker := time.NewTicker(time.Duration(float64(time.Second) / ov.UpdateHz))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errc:
			return err
		case now := <-ticker.C:
			telemetry, available, receivedAt := source()
			hud := FormatHUD(telemetry, available, receivedAt, now)
			select {
			case updates <- hud:
			default:
				<-updates
				updates <- hud
			}
		}
	}
}
