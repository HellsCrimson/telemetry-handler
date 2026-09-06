package voice

import (
	"context"
	"fmt"
	"time"
)

// Config is the runtime configuration for the voice assistant, mapped by the app
// from config.Voice (so this package stays independent of the config package).
type Config struct {
	Remote       RemoteConfig  // GPU voice server (STT + TTS)
	Language     string        // language hint (default "en")
	CaptureCmd   string        // optional recorder override (raw s16le mono PCM on stdout)
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

// Deps are the collaborators the app injects into a built Engine. Only
// Controller and Notify are required; the rest default to no-ops or the
// deterministic grammar.
type Deps struct {
	// Controller reads and writes LMU's pit menu.
	Controller Controller
	// Notify surfaces a message on the overlay banner and in the log.
	Notify Notifier
	// Interpreter turns transcripts into intent. Nil uses the grammar.
	Interpreter Interpreter
	// Speak says one sentence immediately (streamed engineer answers).
	Speak func(string)
	// OnPress runs when the trigger goes down, so the app can silence speech.
	OnPress func()
	Logf    func(string, ...any)
}

// Build wires a ready-to-run Engine from cfg and the app's dependencies. It
// constructs the trigger and the streaming listener that talks to the voice
// server. The caller runs engine.Run(ctx) on a goroutine.
func Build(ctx context.Context, cfg Config, deps Deps) (*Engine, error) {
	listener, err := NewRemoteListener(cfg.Remote, cfg.CaptureCmd, cfg.Language, deps.Logf)
	if err != nil {
		return nil, err
	}
	trigger, err := NewTrigger(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return NewEngine(Options{
		Trigger:     trigger,
		Listener:    listener,
		Interpreter: deps.Interpreter,
		Controller:  deps.Controller,
		Notify:      deps.Notify,
		Speak:       deps.Speak,
		OnPress:     deps.OnPress,
		Logf:        deps.Logf,
		ConfirmTTL:  cfg.ConfirmTTL,
	}), nil
}

// LearnButton blocks until a button/key is pressed on any input device and
// returns it, so the user can bind a wheel-rim button without knowing its evdev
// code. The caller should pass a ctx with a timeout.
func LearnButton(ctx context.Context) (Button, error) {
	return learnButton(ctx)
}
