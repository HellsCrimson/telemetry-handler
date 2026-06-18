package voice

import (
	"context"
	"strings"
	"time"
)

// Notice levels, shared with the overlay banner so it can colour the message.
const (
	LevelInfo    = 0 // transcript echo / neutral status
	LevelConfirm = 1 // a staged action awaiting "yes"
	LevelOK      = 2 // an action was applied
	LevelError   = 3 // something failed / not understood
)

// Notifier surfaces a short message to the driver (the overlay banner). ttl is
// how long the message should stay up.
type Notifier func(text string, level int, ttl time.Duration)

// Event is a push-to-talk transition from a Trigger.
type Event int

const (
	EventPress Event = iota
	EventRelease
)

// Trigger emits push-to-talk press/release events (an external FIFO, or a
// configured evdev button). Events is closed when the trigger is torn down.
type Trigger interface {
	Events() <-chan Event
}

// Capturer records microphone audio between a press and release. Capture blocks
// until stop is closed (the PTT release) or ctx is cancelled, then returns the
// path to a finalized WAV file the caller is responsible for removing.
type Capturer interface {
	Capture(ctx context.Context, stop <-chan struct{}) (wavPath string, err error)
}

// Transcriber turns a WAV file into text (whisper.cpp, locally).
type Transcriber interface {
	Transcribe(ctx context.Context, wavPath string) (string, error)
}

// Cleaner is an optional Capturer extension to remove a finished WAV file.
type Cleaner interface {
	Cleanup(wavPath string)
}

// Engine wires the push-to-talk pipeline together: trigger -> capture ->
// transcribe -> parse -> (confirm) -> apply. The confirmation state (pending
// plan + deadline) lives here; dispatch and the reaper are pure enough to unit
// test without any IO.
type Engine struct {
	trigger     Trigger
	capturer    Capturer
	transcriber Transcriber
	controller  Controller
	notify      Notifier
	logf        func(string, ...any)
	confirmTTL  time.Duration

	pending  *Plan
	deadline time.Time
}

// Options bundles the Engine dependencies.
type Options struct {
	Trigger     Trigger
	Capturer    Capturer
	Transcriber Transcriber
	Controller  Controller
	Notify      Notifier
	Logf        func(string, ...any)
	ConfirmTTL  time.Duration
}

func NewEngine(o Options) *Engine {
	ttl := o.ConfirmTTL
	if ttl <= 0 {
		ttl = 6 * time.Second
	}
	notify := o.Notify
	if notify == nil {
		notify = func(string, int, time.Duration) {}
	}
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Engine{
		trigger:     o.Trigger,
		capturer:    o.Capturer,
		transcriber: o.Transcriber,
		controller:  o.Controller,
		notify:      notify,
		logf:        logf,
		confirmTTL:  ttl,
	}
}

// Run drives the pipeline until ctx is cancelled. It records while PTT is held,
// transcribes on release, and dispatches the resulting transcript. Capture and
// transcription run inline between events (the driver will not press again mid-
// transcription), which keeps the confirmation state single-threaded.
func (e *Engine) Run(ctx context.Context) {
	events := e.trigger.Events()
	var (
		stop  chan struct{}
		capCh chan captureResult
	)
	reaper := time.NewTicker(500 * time.Millisecond)
	defer reaper.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-reaper.C:
			e.reapPending(time.Now())
		case ev, ok := <-events:
			if !ok {
				return
			}
			switch ev {
			case EventPress:
				if stop != nil {
					continue // already capturing
				}
				stop = make(chan struct{})
				capCh = make(chan captureResult, 1)
				go func(stop chan struct{}, out chan captureResult) {
					wav, err := e.capturer.Capture(ctx, stop)
					out <- captureResult{wav: wav, err: err}
				}(stop, capCh)
			case EventRelease:
				if stop == nil {
					continue
				}
				close(stop)
				res := <-capCh
				stop, capCh = nil, nil
				if res.err != nil {
					e.logf("voice: capture: %v", res.err)
					e.notify("MIC ERROR", LevelError, 4*time.Second)
					continue
				}
				e.transcribeAndDispatch(ctx, res.wav)
			}
		}
	}
}

type captureResult struct {
	wav string
	err error
}

// transcribeAndDispatch transcribes a captured WAV and dispatches the text, then
// cleans up the file.
func (e *Engine) transcribeAndDispatch(ctx context.Context, wav string) {
	if c, ok := e.capturer.(Cleaner); ok {
		defer c.Cleanup(wav)
	}
	text, err := e.transcriber.Transcribe(ctx, wav)
	if err != nil {
		e.logf("voice: transcribe: %v", err)
		e.notify("STT ERROR", LevelError, 4*time.Second)
		return
	}
	text = strings.TrimSpace(text)
	e.logf("voice: heard %q", text)
	if text == "" {
		return
	}
	e.dispatch(ctx, text)
}

// dispatch parses a transcript and advances the confirmation state machine. It
// is the single decision point and has no IO of its own beyond the controller
// (pit-menu read/write), so it is unit-testable with a fake controller.
func (e *Engine) dispatch(ctx context.Context, text string) {
	u := Parse(text)

	switch {
	case len(u.Actions) > 0:
		e.stageCommand(ctx, u)
	case u.Affirm:
		e.applyPending(ctx)
	case u.Cancel:
		e.cancelPending()
	default:
		// Heard speech but nothing actionable: echo it so the driver sees what the
		// recognizer caught (helps tune phrasing), but don't disturb a pending
		// confirmation.
		if e.pending == nil {
			e.notify(shorten(text), LevelInfo, 3*time.Second)
		}
	}
}

// stageCommand resolves a command to a pit-menu plan and stages it for
// confirmation (every pit change is important). A command supersedes any
// previously pending one.
func (e *Engine) stageCommand(ctx context.Context, u Utterance) {
	plan, err := Resolve(ctx, e.controller, u.Actions)
	if err != nil {
		e.logf("voice: resolve: %v", err)
		e.notify("PIT MENU UNAVAILABLE", LevelError, 4*time.Second)
		return
	}
	if len(plan.Writes) == 0 {
		e.notify("NOT UNDERSTOOD "+plan.Desc, LevelError, 4*time.Second)
		e.pending = nil
		return
	}
	e.pending = &plan
	e.deadline = time.Now().Add(e.confirmTTL)
	e.notify("CONFIRM "+plan.Desc+" SAY YES", LevelConfirm, e.confirmTTL)
}

// applyPending applies the staged plan in response to an affirmation.
func (e *Engine) applyPending(ctx context.Context) {
	if e.pending == nil || time.Now().After(e.deadline) {
		e.pending = nil
		e.notify("NOTHING TO CONFIRM", LevelInfo, 2*time.Second)
		return
	}
	plan := e.pending
	e.pending = nil
	if err := plan.Apply(ctx, e.controller); err != nil {
		e.logf("voice: apply: %v", err)
		e.notify("FAILED "+plan.Desc, LevelError, 4*time.Second)
		return
	}
	e.notify("DONE "+plan.Desc, LevelOK, 4*time.Second)
}

func (e *Engine) cancelPending() {
	if e.pending == nil {
		return
	}
	e.pending = nil
	e.notify("CANCELLED", LevelInfo, 2*time.Second)
}

// reapPending drops a staged plan once its confirmation window has elapsed.
func (e *Engine) reapPending(now time.Time) {
	if e.pending != nil && now.After(e.deadline) {
		e.pending = nil
		e.notify("CONFIRM TIMED OUT", LevelInfo, 2*time.Second)
	}
}

// shorten trims an echoed transcript to a banner-friendly length.
func shorten(s string) string {
	const max = 24
	s = strings.ToUpper(s)
	if len(s) > max {
		return s[:max]
	}
	return s
}
