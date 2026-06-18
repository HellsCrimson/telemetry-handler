package voice

import (
	"context"
	"fmt"
	"time"
)

// Config is the runtime configuration for the voice MVP, mapped by the app from
// config.Voice (so this package stays independent of the config package).
type Config struct {
	WhisperBin   string        // path to the whisper.cpp executable
	WhisperModel string        // path to the ggml model file
	Language     string        // language hint (default "en")
	CaptureCmd   string        // optional recorder override ("{out}" = WAV path)
	Trigger      string        // "fifo" (default) or "button"
	FIFOPath     string        // FIFO path for Trigger=="fifo"
	ButtonDevice string        // /dev/input/eventX for Trigger=="button"
	ButtonCode   int           // evdev code for Trigger=="button"
	ConfirmTTL   time.Duration // confirmation window for important actions
}

// Button identifies a learned evdev button: the device node and the key code,
// plus the device's human name for display.
type Button struct {
	Device string `json:"device"`
	Code   int    `json:"code"`
	Name   string `json:"name"`
}

// NewTrigger builds the configured push-to-talk trigger.
func NewTrigger(ctx context.Context, cfg Config) (Trigger, error) {
	switch cfg.Trigger {
	case "button":
		return newButtonTrigger(ctx, cfg.ButtonDevice, cfg.ButtonCode)
	case "fifo", "":
		return NewFIFOTrigger(ctx, cfg.FIFOPath)
	default:
		return nil, fmt.Errorf("voice: unknown trigger %q (want fifo or button)", cfg.Trigger)
	}
}

// Build wires a ready-to-run Engine from cfg, the LMU pit controller, and the
// notifier/logger the app supplies. It constructs the trigger, recorder and
// whisper transcriber. The caller runs engine.Run(ctx) on a goroutine.
func Build(ctx context.Context, cfg Config, controller Controller, notify Notifier, logf func(string, ...any)) (*Engine, error) {
	if cfg.WhisperBin == "" || cfg.WhisperModel == "" {
		return nil, fmt.Errorf("voice: whisper_bin and whisper_model must be set")
	}
	trigger, err := NewTrigger(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return NewEngine(Options{
		Trigger:     trigger,
		Capturer:    ExecCapturer{CmdTemplate: cfg.CaptureCmd},
		Transcriber: WhisperTranscriber{Bin: cfg.WhisperBin, Model: cfg.WhisperModel, Lang: cfg.Language},
		Controller:  controller,
		Notify:      notify,
		Logf:        logf,
		ConfirmTTL:  cfg.ConfirmTTL,
	}), nil
}

// LearnButton blocks until a button/key is pressed on any input device and
// returns it, so the user can bind a wheel-rim button without knowing its evdev
// code. The caller should pass a ctx with a timeout.
func LearnButton(ctx context.Context) (Button, error) {
	return learnButton(ctx)
}
