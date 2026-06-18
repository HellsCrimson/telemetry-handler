//go:build !linux

package voice

import (
	"context"
	"fmt"
)

// The evdev button trigger and FIFO creation are Linux-only in the MVP; off Linux
// these return a clear error so the voice feature degrades gracefully.

func newButtonTrigger(context.Context, string, int) (*ButtonTrigger, error) {
	return nil, fmt.Errorf("voice: button trigger is only supported on Linux")
}

// ButtonTrigger is declared off-Linux only to satisfy newButtonTrigger's return
// type; it is never constructed.
type ButtonTrigger struct{ events chan Event }

func (t *ButtonTrigger) Events() <-chan Event { return t.events }

func learnButton(context.Context) (Button, error) {
	return Button{}, fmt.Errorf("voice: button learning is only supported on Linux")
}

func ensureFIFO(string) error {
	return fmt.Errorf("voice: FIFO trigger is only supported on Linux")
}
