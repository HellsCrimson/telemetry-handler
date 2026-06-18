package voice

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// WhisperTranscriber shells out to a local whisper.cpp binary (whisper-cli / the
// legacy "main") to transcribe a WAV file. It is offline and host-local — no
// audio leaves the machine. Bin is the executable path, Model the ggml model
// file, Lang the language hint (e.g. "en").
type WhisperTranscriber struct {
	Bin   string
	Model string
	Lang  string
}

// bracketRe strips whisper.cpp's bracketed non-speech annotations like
// "[BLANK_AUDIO]" or "(wind blowing)" that would otherwise pollute the grammar.
var bracketRe = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)`)

// Transcribe runs whisper.cpp on wavPath and returns the recognized text. It uses
// -nt (no timestamps) so stdout is just the transcript. stderr (whisper's
// progress/log noise) is captured only for error reporting.
func (w WhisperTranscriber) Transcribe(ctx context.Context, wavPath string) (string, error) {
	if w.Bin == "" || w.Model == "" {
		return "", fmt.Errorf("whisper bin/model not configured")
	}
	lang := w.Lang
	if lang == "" {
		lang = "en"
	}
	args := []string{"-m", w.Model, "-f", wavPath, "-nt", "-l", lang}
	cmd := exec.CommandContext(ctx, w.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("whisper: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return cleanTranscript(stdout.String()), nil
}

// cleanTranscript collapses whisper's multi-line, annotation-laden output into a
// single normalized line.
func cleanTranscript(out string) string {
	out = bracketRe.ReplaceAllString(out, " ")
	out = strings.ReplaceAll(out, "\n", " ")
	out = strings.Join(strings.Fields(out), " ")
	return strings.TrimSpace(out)
}
