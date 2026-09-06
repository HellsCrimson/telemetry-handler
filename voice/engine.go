package voice

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Notice levels, shared with the overlay banner so it can colour the message.
const (
	LevelInfo    = 0 // transcript echo / neutral status
	LevelConfirm = 1 // a staged action awaiting "yes"
	LevelOK      = 2 // an action was applied
	LevelError   = 3 // something failed / not understood
	LevelAnswer  = 4 // a spoken answer from the engineer (already said aloud)
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

// Listener records one push-to-talk utterance and returns what was said. Listen
// starts capturing immediately and returns once stop is closed (the PTT release)
// or ctx is cancelled. Capture and recognition are one step because the audio is
// streamed to the voice server as it is recorded — see RemoteListener.
type Listener interface {
	Listen(ctx context.Context, stop <-chan struct{}) (string, error)
}

// Engine wires the push-to-talk pipeline together: trigger -> listen -> parse ->
// (confirm) -> apply. The confirmation state (pending plan + deadline) lives
// here; dispatch and the reaper are pure enough to unit test without any IO.
type Engine struct {
	trigger     Trigger
	listener    Listener
	interpreter Interpreter
	controller  Controller
	notify      Notifier
	speak       func(string)
	onPress     func()
	logf        func(string, ...any)
	confirmTTL  time.Duration

	pending  *Plan
	deadline time.Time
}

// Options bundles the Engine dependencies.
type Options struct {
	Trigger    Trigger
	Listener   Listener
	Controller Controller
	Notify     Notifier
	// Interpreter turns a transcript into intent. Nil uses the deterministic
	// grammar; the app supplies the LLM-backed one when the engineer is enabled.
	Interpreter Interpreter
	// Speak, if set, says one sentence immediately. The interpreter calls it as an
	// answer streams in, so a spoken reply starts before it is fully generated.
	Speak func(string)
	// OnPress, if set, runs the moment the trigger goes down — before recording
	// starts. The app uses it to silence the speaker so the driver can cut the
	// assistant off mid-sentence instead of shouting over it.
	OnPress    func()
	Logf       func(string, ...any)
	ConfirmTTL time.Duration
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
	onPress := o.OnPress
	if onPress == nil {
		onPress = func() {}
	}
	speak := o.Speak
	if speak == nil {
		speak = func(string) {}
	}
	var interpreter Interpreter = GrammarInterpreter{}
	if o.Interpreter != nil {
		interpreter = o.Interpreter
	}
	return &Engine{
		trigger:     o.Trigger,
		listener:    o.Listener,
		interpreter: interpreter,
		controller:  o.Controller,
		notify:      notify,
		speak:       speak,
		onPress:     onPress,
		logf:        logf,
		confirmTTL:  ttl,
	}
}

// Run drives the pipeline until ctx is cancelled. It records while PTT is held
// (streaming to the voice server as it goes) and dispatches the transcript that
// comes back on release. Recognition is awaited inline between events (the
// driver will not press again mid-transcription), which keeps the confirmation
// state single-threaded.
func (e *Engine) Run(ctx context.Context) {
	events := e.trigger.Events()
	var (
		stop    chan struct{}
		heardCh chan listenResult
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
					continue // already listening
				}
				e.onPress()
				stop = make(chan struct{})
				heardCh = make(chan listenResult, 1)
				go func(stop chan struct{}, out chan listenResult) {
					text, err := e.listener.Listen(ctx, stop)
					out <- listenResult{text: text, err: err}
				}(stop, heardCh)
			case EventRelease:
				if stop == nil {
					continue
				}
				released := time.Now()
				close(stop)
				res := <-heardCh
				stop, heardCh = nil, nil
				if res.err != nil {
					e.logf("voice: listen: %v", res.err)
					e.notify(listenErrorNotice(res.err), LevelError, 4*time.Second)
					continue
				}
				e.dispatchHeard(ctx, res.text, time.Since(released))
			}
		}
	}
}

type listenResult struct {
	text string
	err  error
}

// listenErrorNotice turns a failed utterance into a banner message that says
// which half broke: the voice server being down looks nothing like a dead mic,
// and there is no local fallback to hide it.
func listenErrorNotice(err error) string {
	if errors.Is(err, ErrServerUnreachable) {
		return "VOICE SERVER OFFLINE"
	}
	return "VOICE ERROR"
}

// dispatchHeard logs and dispatches a transcript. The elapsed time is the whole
// wait the driver feels after letting go of the trigger — the capture tail, the
// recorder shutdown and the server's decode — and is logged because keeping it
// small is the reason speech recognition runs on the GPU box at all.
func (e *Engine) dispatchHeard(ctx context.Context, text string, elapsed time.Duration) {
	text = strings.TrimSpace(text)
	e.logf("voice: heard %q %v after release", text, elapsed.Round(time.Millisecond))
	if text == "" {
		return
	}
	e.dispatch(ctx, text)
}

// dispatch interprets a transcript and advances the confirmation state machine.
// It is the single decision point and has no IO of its own beyond the
// interpreter and the controller (pit-menu read/write), so it is unit-testable
// with fakes.
func (e *Engine) dispatch(ctx context.Context, text string) {
	// Answering a pending confirmation is resolved by the grammar first, always.
	// "Yes" is the gate in front of every change that reaches the car, so it must
	// not depend on a model being up, warm, or in a good mood — and routing it
	// through one would put a second of latency on the most time-critical word
	// the driver says.
	if e.pending != nil {
		if u := Parse(text); u.Affirm || u.Cancel {
			if u.Affirm {
				e.applyPending(ctx)
			} else {
				e.cancelPending()
			}
			return
		}
	}

	reply, err := e.interpreter.Interpret(ctx, text, e.speak)
	if err != nil {
		// No usable answer: fall back to the grammar so a pit call still lands
		// when the model is unreachable.
		e.logf("voice: interpret: %v", err)
		reply, _ = GrammarInterpreter{}.Interpret(ctx, text, nil)
	}

	switch {
	case reply.Plan != nil:
		e.stagePlan(*reply.Plan)
	case len(reply.Actions) > 0:
		e.stageCommand(ctx, reply.Actions)
	case reply.Affirm:
		e.applyPending(ctx)
	case reply.Cancel:
		e.cancelPending()
	case reply.Speech != "":
		// Already spoken as it streamed; this only puts it on the banner and in
		// the log.
		e.notify(shorten(reply.Speech), LevelAnswer, 5*time.Second)
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
func (e *Engine) stageCommand(ctx context.Context, actions []Action) {
	// Resolving reads the live pit menu over LMU's REST API, which is the one
	// remaining round trip between the transcript and the prompt on screen — time
	// it separately so a slow game is not mistaken for slow recognition.
	t0 := time.Now()
	plan, err := Resolve(ctx, e.controller, actions)
	e.logf("voice: pit menu resolved in %v", time.Since(t0).Round(time.Millisecond))
	if err != nil {
		e.logf("voice: resolve: %v", err)
		e.notify("PIT MENU UNAVAILABLE", LevelError, 4*time.Second)
		return
	}
	if plan.Empty() {
		e.notify("NOT UNDERSTOOD "+plan.Desc, LevelError, 4*time.Second)
		e.pending = nil
		return
	}
	e.stagePlan(plan)
}

// stagePlan puts an already-resolved change in front of the driver. Everything
// that reaches the car goes through here, so there is exactly one place where a
// change waits for a spoken "yes".
func (e *Engine) stagePlan(plan Plan) {
	if plan.Empty() {
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
