package voice

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const (
	// captureSampleRate is the rate the recorder produces and the server's Whisper
	// model expects. Mono, signed 16-bit little-endian.
	captureSampleRate = 16000
	// captureTail is how long recording continues past the trigger release. The
	// last word typically ends as the driver lets go, and the recorder's period
	// would otherwise clip it.
	captureTail = 150 * time.Millisecond
	// gracefulStopTimeout is how long the recorder gets to exit after being asked
	// to stop, before it is killed.
	gracefulStopTimeout = 2 * time.Second
	// transcribeTimeout bounds the wait for the server's decode.
	transcribeTimeout = 20 * time.Second
)

// pcmRecorder is a running external recorder writing raw PCM (s16le, mono,
// captureSampleRate) to its stdout, which the listener streams straight to the
// voice server. Unlike the old file-based capture there is no WAV to finalize,
// so nothing has to happen between the trigger release and the transcript.
type pcmRecorder struct {
	cmd *exec.Cmd
	out io.ReadCloser
}

// startRecorder launches the recorder. cmdTemplate optionally overrides the
// platform default; it must be a command that writes raw s16le mono PCM at
// captureSampleRate to stdout (e.g. "parecord --raw --format=s16le --rate=16000
// --channels=1 --device=alsa_input.foo").
func startRecorder(cmdTemplate string) (*pcmRecorder, error) {
	name, args, err := recorderCommand(cmdTemplate)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(name, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("recorder stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	return &pcmRecorder{cmd: cmd, out: out}, nil
}

// signalStop waits out the tail, then asks the recorder to finish. Its stdout
// hits EOF once it exits, which is what ends the pump. It deliberately does not
// reap the process: exec closes the stdout pipe in Wait, so waiting before the
// pump has drained would truncate the last audio.
func (r *pcmRecorder) signalStop(tail time.Duration) {
	if tail > 0 {
		time.Sleep(tail)
	}
	stopRecorder(r.cmd)
}

// kill forces the recorder down, for a recorder that ignored the stop signal.
func (r *pcmRecorder) kill() {
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
}

// wait reaps the recorder. Call it only once the pump has finished reading.
func (r *pcmRecorder) wait() {
	waitOrKill(r.cmd, gracefulStopTimeout)
}

// recorderCommand resolves the recorder command line: the user override or the
// platform default.
func recorderCommand(cmdTemplate string) (string, []string, error) {
	if strings.TrimSpace(cmdTemplate) != "" {
		fields := strings.Fields(cmdTemplate)
		return fields[0], fields[1:], nil
	}
	return defaultCaptureCommand()
}

// waitOrKill waits for the recorder to exit after a graceful stop, force-killing
// it if it does not finish within grace.
func waitOrKill(cmd *exec.Cmd, grace time.Duration) {
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}
}
