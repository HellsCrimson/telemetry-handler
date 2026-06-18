//go:build linux

package voice

import (
	"io"
	"os"
	"os/exec"
)

// defaultCaptureCommand records mono 16 kHz signed-16 WAV via ALSA's arecord
// (which, under PipeWire's alsa bridge too, patches the WAV header on SIGINT).
func defaultCaptureCommand(out string) (string, []string, error) {
	return "arecord", []string{"-q", "-f", "S16_LE", "-r", "16000", "-c", "1", "-t", "wav", out}, nil
}

// stopRecorder interrupts the recorder so it finalizes the WAV file; arecord
// re-writes the RIFF length and exits cleanly on SIGINT.
func stopRecorder(cmd *exec.Cmd, stdin io.WriteCloser) {
	closeStdin(stdin)
	if cmd.Process == nil {
		return
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		_ = cmd.Process.Kill()
	}
}
