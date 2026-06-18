//go:build !linux

package voice

import (
	"fmt"
	"os/exec"
)

// defaultCaptureCommand has no built-in recorder off Linux; configure a
// CmdTemplate to use the voice MVP on other platforms.
func defaultCaptureCommand(string) (string, []string, error) {
	return "", nil, fmt.Errorf("no default audio capture on this platform; set voice.capture_cmd")
}

func stopProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
