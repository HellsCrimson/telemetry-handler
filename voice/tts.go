package voice

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// TTSConfig configures spoken output. Synthesis is delegated to a local CLI (the
// Cmd template) that writes a WAV, which is then played. This single, command-
// based path drives anything from espeak-ng to kokoro-tts; there is no embedded
// model and no server.
//
// Cmd placeholders: "{out}" is the output WAV path, and "{txt}" — when present —
// is a temp file holding the text (tools like kokoro-tts read the text from a
// file rather than stdin). If "{txt}" is absent, the text is fed on stdin
// (espeak-ng / piper style). Examples:
//
//	espeak-ng -w {out}
//	kokoro-tts {txt} {out} --voice af_sarah --model /path/kokoro-v1.0.onnx --voices /path/voices-v1.0.bin
type TTSConfig struct {
	Cmd       string // synth command template ("{out}" WAV, optional "{txt}" text file)
	PlayerCmd string // override the audio player ("{out}" = WAV path)
}

// Speaker turns a message into spoken audio. Speak is non-blocking and fire-and-
// forget: synthesis/playback happen on a worker so the caller (the engine's
// notify path) is never stalled.
type Speaker interface {
	Speak(text string)
}

// synthFunc renders text to a WAV file at out.
type synthFunc func(ctx context.Context, text, out string) error

// NewSpeaker builds an async Speaker that runs until ctx is cancelled. It returns
// an error if the command/player cannot be resolved. Synthesis failures at speak
// time are logged, not fatal.
func NewSpeaker(ctx context.Context, cfg TTSConfig, logf func(string, ...any)) (Speaker, error) {
	synth, err := buildSynth(cfg)
	if err != nil {
		return nil, err
	}
	play, err := newPlayer(cfg.PlayerCmd)
	if err != nil {
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &asyncSpeaker{ctx: ctx, synth: synth, play: play, logf: logf, ch: make(chan string, 4)}
	go s.loop()
	return s, nil
}

// SpeakOnce synthesizes and plays text synchronously, returning any error. Used
// by the dashboard's "Test voice" button so the user gets immediate feedback.
func SpeakOnce(ctx context.Context, cfg TTSConfig, text string, _ func(string, ...any)) error {
	synth, err := buildSynth(cfg)
	if err != nil {
		return err
	}
	play, err := newPlayer(cfg.PlayerCmd)
	if err != nil {
		return err
	}
	return utter(ctx, synth, play, text)
}

func buildSynth(cfg TTSConfig) (synthFunc, error) {
	if strings.TrimSpace(cfg.Cmd) == "" {
		return nil, fmt.Errorf("voice.tts.cmd is required (e.g. \"espeak-ng -w {out}\")")
	}
	return commandSynth(cfg.Cmd), nil
}

type asyncSpeaker struct {
	ctx   context.Context
	synth synthFunc
	play  player
	logf  func(string, ...any)
	ch    chan string
}

// Speak queues text, dropping it if the worker is already backed up (a few quick
// messages shouldn't pile up an unbounded backlog of speech).
func (s *asyncSpeaker) Speak(text string) {
	select {
	case s.ch <- text:
	default:
	}
}

func (s *asyncSpeaker) loop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case text := <-s.ch:
			if err := utter(s.ctx, s.synth, s.play, text); err != nil {
				s.logf("voice: tts: %v", err)
			}
		}
	}
}

// utter renders text to a temp WAV and plays it, cleaning up the file.
func utter(ctx context.Context, synth synthFunc, play player, text string) error {
	f, err := os.CreateTemp("", "tts-*.wav")
	if err != nil {
		return fmt.Errorf("temp wav: %w", err)
	}
	out := f.Name()
	f.Close()
	defer os.Remove(out)

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := synth(cctx, text, out); err != nil {
		return fmt.Errorf("synth: %w", err)
	}
	if err := play.play(cctx, out); err != nil {
		return fmt.Errorf("play: %w", err)
	}
	return nil
}

// commandSynth runs a local TTS CLI. "{out}" is the output WAV; "{txt}", when
// present, is a temp file holding the text (for tools that read a file), else the
// text is fed on stdin.
func commandSynth(tmpl string) synthFunc {
	usesTxt := strings.Contains(tmpl, "{txt}")
	return func(ctx context.Context, text, out string) error {
		fields := strings.Fields(tmpl)
		if len(fields) == 0 {
			return fmt.Errorf("empty tts command")
		}

		txtFile := ""
		if usesTxt {
			tf, err := os.CreateTemp("", "tts-*.txt")
			if err != nil {
				return fmt.Errorf("temp text: %w", err)
			}
			txtFile = tf.Name()
			defer os.Remove(txtFile)
			if _, err := tf.WriteString(text); err != nil {
				tf.Close()
				return err
			}
			tf.Close()
		}

		args := make([]string, len(fields))
		for i, f := range fields {
			f = strings.ReplaceAll(f, "{out}", out)
			f = strings.ReplaceAll(f, "{txt}", txtFile)
			args[i] = f
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		if !usesTxt {
			cmd.Stdin = strings.NewReader(text)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}

// player plays a WAV file via an external command.
type player struct {
	name string
	args []string // may contain the "{out}" placeholder
}

func newPlayer(override string) (player, error) {
	if strings.TrimSpace(override) != "" {
		fields := strings.Fields(override)
		return player{name: fields[0], args: fields[1:]}, nil
	}
	name, args := defaultPlayer()
	if name == "" {
		return player{}, fmt.Errorf("no default audio player on %s; set voice.tts.player_cmd", runtime.GOOS)
	}
	return player{name: name, args: args}, nil
}

func (p player) play(ctx context.Context, wav string) error {
	args := make([]string, len(p.args))
	for i, a := range p.args {
		args[i] = strings.ReplaceAll(a, "{out}", wav)
	}
	return exec.CommandContext(ctx, p.name, args...).Run()
}

// defaultPlayer picks a built-in WAV player per OS. paplay covers PulseAudio and
// PipeWire (via pipewire-pulse); Windows uses the .NET SoundPlayer via PowerShell.
func defaultPlayer() (string, []string) {
	switch runtime.GOOS {
	case "linux":
		return "paplay", []string{"{out}"}
	case "windows":
		return "powershell", []string{"-NoProfile", "-Command", "(New-Object Media.SoundPlayer '{out}').PlaySync()"}
	default:
		return "", nil
	}
}
