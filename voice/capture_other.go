//go:build !linux && !windows

package voice

import (
	"fmt"
	"io"
	"os/exec"
)

// defaultCaptureCommand has no built-in recorder on these platforms; configure a
// CmdTemplate to use the voice MVP.
func defaultCaptureCommand(string) (string, []string, error) {
	return "", nil, fmt.Errorf("no default audio capture on this platform; set voice.capture_cmd")
}

func stopRecorder(cmd *exec.Cmd, stdin io.WriteCloser) {
	closeStdin(stdin)
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
