//go:build linux

package overlay

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"telemetry-handler/config"
)

// resolveOutput fills in ov.Output by auto-detecting which monitor the game
// window is currently on, when the configured output is empty or "auto". A
// concrete output name is treated as a manual override and returned unchanged.
//
// Detection is Hyprland-specific (via hyprctl IPC); on other compositors the
// Wayland protocol does not expose other clients' positions, so we fall back to
// the compositor's default output.
func resolveOutput(ov config.Overlay) config.Overlay {
	out := strings.TrimSpace(ov.Output)
	if out != "" && !strings.EqualFold(out, "auto") {
		return ov // manual override — respect the configured monitor
	}

	name, err := detectHyprlandOutput(ov.GameWindowMatch)
	if err != nil {
		log.Printf("overlay: auto output detection unavailable, using compositor default (%v)", err)
		ov.Output = ""
		return ov
	}
	if name == "" {
		log.Printf("overlay: game window %q not found, using compositor default output", matchOrDefault(ov.GameWindowMatch))
		ov.Output = ""
		return ov
	}
	log.Printf("overlay: auto-detected game window on output %q", name)
	ov.Output = name
	return ov
}

func matchOrDefault(m string) string {
	if strings.TrimSpace(m) == "" {
		return "forza"
	}
	return m
}

type hyprClient struct {
	Class        string `json:"class"`
	Title        string `json:"title"`
	InitialClass string `json:"initialClass"`
	InitialTitle string `json:"initialTitle"`
	Monitor      int    `json:"monitor"`
	Mapped       bool   `json:"mapped"`
	Hidden       bool   `json:"hidden"`
}

type hyprMonitor struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// detectHyprlandOutput returns the Wayland output name of the monitor the game
// window is on, or "" if no matching window was found. It returns an error only
// when Hyprland/hyprctl is not usable at all.
func detectHyprlandOutput(match string) (string, error) {
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" {
		return "", fmt.Errorf("not running under Hyprland")
	}
	needle := strings.ToLower(matchOrDefault(match))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var clients []hyprClient
	if err := hyprctlJSON(ctx, &clients, "clients"); err != nil {
		return "", err
	}

	monitorID := matchClientMonitor(clients, needle, true)
	if monitorID < 0 {
		// Fall back to ignoring mapped/hidden flags in case the game window is
		// reported with unexpected state during startup.
		monitorID = matchClientMonitor(clients, needle, false)
	}
	if monitorID < 0 {
		return "", nil
	}

	var monitors []hyprMonitor
	if err := hyprctlJSON(ctx, &monitors, "monitors"); err != nil {
		return "", err
	}
	for _, m := range monitors {
		if m.ID == monitorID {
			return m.Name, nil
		}
	}
	return "", nil
}

func matchClientMonitor(clients []hyprClient, needle string, requireVisible bool) int {
	for _, c := range clients {
		if requireVisible && (c.Hidden || !c.Mapped) {
			continue
		}
		if clientMatches(c, needle) {
			return c.Monitor
		}
	}
	return -1
}

func clientMatches(c hyprClient, lowerNeedle string) bool {
	for _, field := range []string{c.Class, c.Title, c.InitialClass, c.InitialTitle} {
		if field != "" && strings.Contains(strings.ToLower(field), lowerNeedle) {
			return true
		}
	}
	return false
}

func hyprctlJSON(ctx context.Context, v any, args ...string) error {
	out, err := exec.CommandContext(ctx, "hyprctl", append([]string{"-j"}, args...)...).Output()
	if err != nil {
		return fmt.Errorf("hyprctl %s: %w", strings.Join(args, " "), err)
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("parse hyprctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
