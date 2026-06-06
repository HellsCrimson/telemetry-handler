//go:build linux

package overlay

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"telemetry-handler/config"
)

type waylandBackend struct{}

func newBackend() Backend {
	return waylandBackend{}
}

func (waylandBackend) Run(ctx context.Context, cfg config.Overlay, updates <-chan HUD) error {
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		return fmt.Errorf("Wayland unavailable: XDG_RUNTIME_DIR is not set")
	}

	client, err := connectWayland()
	if err != nil {
		return err
	}
	defer client.Close()

	surface, err := client.CreateLayerSurface(cfg)
	if err != nil {
		return err
	}
	defer surface.Close()

	buffer, err := newSHMBuffer(cfg.WidthValue(), cfg.HeightValue())
	if err != nil {
		return err
	}
	defer buffer.Close()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case hud := <-updates:
			drawHUD(buffer.pixels, cfg.WidthValue(), cfg.HeightValue(), cfg.Opacity, hud)
			if err := surface.CommitBuffer(buffer); err != nil {
				if err == syscall.EPIPE {
					return fmt.Errorf("Wayland compositor connection closed")
				}
				return err
			}
		}
	}
}
