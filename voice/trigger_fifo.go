package voice

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// FIFOTrigger reads push-to-talk transitions from a named pipe (FIFO). An
// external binding — a Hyprland keybind, a wheel-button script, anything — writes
// a line per transition: "press"/"down"/"1" on key-down and "release"/"up"/"0"
// on key-up. This decouples PTT from window focus, which matters because the game
// (not the dashboard) is focused while driving, and Wayland makes in-process
// global hotkeys unreliable.
//
// Example Hyprland binds (hold F8 to talk):
//
//	bindp = , F8, exec, echo press   > /run/user/1000/th-ptt
//	bindrn = , F8, exec, echo release > /run/user/1000/th-ptt
type FIFOTrigger struct {
	path   string
	events chan Event
}

// NewFIFOTrigger creates (if needed) and starts reading the FIFO at path. The
// returned trigger's Events channel closes when ctx is cancelled.
func NewFIFOTrigger(ctx context.Context, path string) (*FIFOTrigger, error) {
	if path == "" {
		return nil, fmt.Errorf("voice: fifo path required")
	}
	if err := ensureFIFO(path); err != nil {
		return nil, err
	}
	t := &FIFOTrigger{path: path, events: make(chan Event, 8)}
	go t.loop(ctx)
	return t, nil
}

func (t *FIFOTrigger) Events() <-chan Event { return t.events }

func (t *FIFOTrigger) loop(ctx context.Context) {
	defer close(t.events)
	for {
		if ctx.Err() != nil {
			return
		}
		// Opening the read end blocks until a writer connects; once the writer
		// disconnects the scanner ends and we reopen for the next press.
		f, err := os.OpenFile(t.path, os.O_RDONLY, 0)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
				continue
			}
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			if ev, ok := parseTriggerToken(scanner.Text()); ok {
				select {
				case t.events <- ev:
				case <-ctx.Done():
					f.Close()
					return
				}
			}
			if ctx.Err() != nil {
				break
			}
		}
		f.Close()
	}
}

func parseTriggerToken(line string) (Event, bool) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "press", "down", "1", "ptt", "start":
		return EventPress, true
	case "release", "up", "0", "end", "stop":
		return EventRelease, true
	default:
		return 0, false
	}
}
