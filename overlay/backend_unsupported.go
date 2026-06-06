//go:build !linux

package overlay

import (
	"context"
	"fmt"

	"telemetry-handler/config"
)

type unsupportedBackend struct{}

func newBackend() Backend {
	return unsupportedBackend{}
}

func (unsupportedBackend) Run(context.Context, config.Overlay, <-chan HUD) error {
	return fmt.Errorf("native overlay backend is not implemented on this platform")
}
