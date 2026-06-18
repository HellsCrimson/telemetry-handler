//go:build windows

package voice

import (
	"io"
	"os/exec"
)

// defaultCaptureCommand records mono 16 kHz WAV with ffmpeg's DirectShow input.
// "audio=default" works on many setups; if it fails, list devices with
//
//	ffmpeg -list_devices true -f dshow -i dummy
//
// and set voice.capture_cmd, e.g.
//
//	ffmpeg -hide_banner -loglevel error -f dshow -i audio="Microphone (USB Audio)" -ar 16000 -ac 1 -y {out}
//
// ffmpeg is used (rather than a native recorder) because it finalizes the WAV
// header cleanly when told to stop via "q" on stdin — see stopRecorder.
func defaultCaptureCommand(out string) (string, []string, error) {
	return "ffmpeg", []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "dshow", "-i", "audio=default",
		"-ar", "16000", "-ac", "1", "-y", out,
	}, nil
}

// stopRecorder asks ffmpeg to quit by writing "q" to its stdin, which makes it
// flush and finalize the WAV (a Kill would truncate it). waitOrKill force-kills
// if it does not exit in time, covering recorders that ignore stdin.
func stopRecorder(_ *exec.Cmd, stdin io.WriteCloser) {
	if stdin != nil {
		io.WriteString(stdin, "q\n")
		stdin.Close()
	}
}
