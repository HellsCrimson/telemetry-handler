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

// Monitor has no auto-detection on platforms without game-window/monitor
// querying, so it reports ok=false and the UI falls back to a manual resolution.
func Monitor(_ config.Overlay) (width, height int, name string, ok bool) {
	return 0, 0, "", false
}

// Monitors has no monitor enumeration on platforms without compositor querying.
func Monitors() []string {
	return nil
}
