//go:build linux

package overlay

import (
	"context"
	"fmt"
	"log"
	"os"
	"syscall"

	"telemetry-handler/config"
)

type waylandBackend struct {
	steeringWheel *SteeringWheel
}

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

	var steeringWheel *SteeringWheel
	if cfg.ShowSteering {
		if cfg.SteeringImagePath != "" {
			sw, err := LoadSteeringWheel(cfg.SteeringImagePath, cfg.SteeringSizeValue())
			if err != nil {
				log.Printf("failed to load steering wheel image: %v, using default", err)
				steeringWheel = NewSteeringWheel(cfg.SteeringSizeValue())
			} else {
				steeringWheel = sw
			}
		} else {
			steeringWheel = NewSteeringWheel(cfg.SteeringSizeValue())
		}
	}

	width := cfg.WidthValue()
	height := cfg.HeightValue()
	steeringSize := cfg.SteeringSizeValue()
	steeringX := width - steeringSize - 64
	steeringY := 8

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case hud := <-updates:
			drawHUD(buffer.pixels, width, height, cfg.Opacity, hud)
			if steeringWheel != nil {
				steeringPixels := steeringWheel.GetRotated(hud.SteeringAngle)
				drawSteering(buffer.pixels, width, height, steeringX, steeringY, steeringSize, cfg.Opacity, steeringPixels)
			}
			if err := surface.CommitBuffer(buffer); err != nil {
				if err == syscall.EPIPE {
					return fmt.Errorf("Wayland compositor connection closed")
				}
				return err
			}
		}
	}
}
