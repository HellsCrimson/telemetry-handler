package voice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ExecCapturer records microphone audio by running an external recorder process
// (arecord by default on Linux) that writes a WAV file, then stopping it on the
// PTT release. CmdTemplate optionally overrides the recorder: a space-separated
// command where the literal token "{out}" is replaced with the output WAV path
// (e.g. "parecord --file-format=wav --rate=16000 --channels=1 {out}"). Empty uses
// the platform default. WAV is mono 16 kHz, what whisper.cpp expects.
type ExecCapturer struct {
	CmdTemplate string
}

// Capture starts the recorder, waits for stop (PTT release) or ctx cancellation,
// then interrupts the recorder so it finalizes the WAV header and returns the
// file path. The caller removes the file via Cleanup.
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

	// The recorder runs on its own; we control its lifetime by signalling it.
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		os.Remove(out)
		return "", fmt.Errorf("start %s: %w", name, err)
	}

	select {
	case <-stop:
	case <-ctx.Done():
	}

	// Interrupt so a seekable WAV recorder (arecord) patches the RIFF length and
	// closes cleanly; fall back to Kill if it ignores the signal.
	stopProcess(cmd)
	_ = cmd.Wait()

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
