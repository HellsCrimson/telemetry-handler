//go:build linux

package voice

import (
	"os"
	"os/exec"
	"strconv"
)

// defaultCaptureCommand records raw mono PCM to stdout via PulseAudio/PipeWire's
// parecord. --latency-msec keeps the flush granularity small so audio reaches the
// server continuously while the trigger is held, rather than in large periods.
func defaultCaptureCommand() (string, []string, error) {
	return "parecord", []string{
		"--raw",
		"--format=s16le",
		"--rate=" + strconv.Itoa(captureSampleRate),
		"--channels=1",
		"--latency-msec=50",
	}, nil
}

// stopRecorder interrupts the recorder so it exits cleanly and its stdout hits
// EOF, ending the pump.
func stopRecorder(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		_ = cmd.Process.Kill()
	}
}

// defaultPlayerCommand plays raw PCM from stdin through PulseAudio/PipeWire.
// The format flags are filled in from the stream's "begin" message.
func defaultPlayerCommand() (string, []string, error) {
	return "pacat", []string{
		"--playback",
		"--raw",
		"--format=s16le",
		"--rate={rate}",
		"--channels={channels}",
		"--latency-msec=50",
	}, nil
}
