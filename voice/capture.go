package voice

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gracefulStopTimeout is how long we wait for the recorder to finalize and exit
// after asking it to stop (SIGINT on Unix, "q" on stdin for ffmpeg on Windows)
// before force-killing it.
const gracefulStopTimeout = 3 * time.Second

// ExecCapturer records microphone audio by running an external recorder process
// that writes a WAV file, then stopping it on the PTT release. CmdTemplate
// optionally overrides the recorder: a space-separated command where the literal
// token "{out}" is replaced with the output WAV path. Empty uses the platform
// default (arecord on Linux, ffmpeg/dshow on Windows). WAV is mono 16 kHz, what
// whisper.cpp expects.
type ExecCapturer struct {
	CmdTemplate string
}

// Capture starts the recorder, waits for stop (PTT release) or ctx cancellation,
// then stops the recorder cleanly so it finalizes the WAV, and returns the file
// path. The caller removes the file via Cleanup.
func (e ExecCapturer) Capture(ctx context.Context, stop <-chan struct{}) (string, error) {
	f, err := os.CreateTemp("", "voice-*.wav")
	if err != nil {
		return "", fmt.Errorf("temp wav: %w", err)
	}
	out := f.Name()
	f.Close()

	name, args, err := e.command(out)
	if err != nil {
		os.Remove(out)
		return "", err
	}

	cmd := exec.Command(name, args...)
	// A stdin pipe lets platforms that stop the recorder via a stdin command
	// (ffmpeg's "q") finalize the file; recorders that ignore stdin are unaffected.
	stdin, _ := cmd.StdinPipe()
	if err := cmd.Start(); err != nil {
		os.Remove(out)
		return "", fmt.Errorf("start %s: %w", name, err)
	}

	select {
	case <-stop:
	case <-ctx.Done():
	}

	stopRecorder(cmd, stdin)
	waitOrKill(cmd, gracefulStopTimeout)

	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		os.Remove(out)
		return "", fmt.Errorf("no audio captured")
	}
	return out, nil
}

// Cleanup removes a finished capture file.
func (e ExecCapturer) Cleanup(wavPath string) {
	if wavPath != "" {
		os.Remove(wavPath)
	}
}

// command resolves the recorder command line: the user template (with {out}
// substituted) or the platform default.
func (e ExecCapturer) command(out string) (string, []string, error) {
	if strings.TrimSpace(e.CmdTemplate) != "" {
		fields := strings.Fields(e.CmdTemplate)
		for i, f := range fields {
			fields[i] = strings.ReplaceAll(f, "{out}", out)
		}
		return fields[0], fields[1:], nil
	}
	return defaultCaptureCommand(out)
}

// waitOrKill waits for the recorder to exit after a graceful stop, force-killing
// it if it does not finish within grace (so a recorder that ignores the stop
// signal cannot hang the pipeline).
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

// closeStdin closes a recorder's stdin pipe if present (shared by the platform
// stopRecorder implementations).
func closeStdin(stdin io.WriteCloser) {
	if stdin != nil {
		stdin.Close()
	}
}
