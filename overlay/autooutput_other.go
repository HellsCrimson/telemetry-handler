//go:build !linux

package overlay

import (
	"strings"

	"telemetry-handler/config"
)

// resolveOutput is a no-op on platforms without game-window auto-detection. It
// normalizes the "auto" sentinel to an empty output (the compositor/OS default)
// so backends never try to match a literal output named "auto".
func resolveOutput(ov config.Overlay) config.Overlay {
	if strings.EqualFold(strings.TrimSpace(ov.Output), "auto") {
		ov.Output = ""
	}
	return ov
}
