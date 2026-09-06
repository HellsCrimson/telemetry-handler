//go:build !linux

package voice

import (
	"fmt"
	"os/exec"
)

// The voice assistant is Linux-only: capture, playback and the push-to-talk
// triggers all rely on PulseAudio/PipeWire and evdev. Off Linux the feature
// reports a clear error instead of half-working.
func defaultCaptureCommand() (string, []string, error) {
	return "", nil, fmt.Errorf("voice: audio capture is only supported on Linux")
}

func stopRecorder(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func defaultPlayerCommand() (string, []string, error) {
	return "", nil, fmt.Errorf("voice: audio playback is only supported on Linux")
}
