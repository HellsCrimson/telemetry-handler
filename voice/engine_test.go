package voice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"telemetry-handler/game/lmu/rest"
)

// recNotifier records the last notice. It is mutex-guarded because the
// Engine.Run tests notify from the engine's own goroutine.
type recNotifier struct {
	mu    sync.Mutex
	text  string
	level int
	count int
}

func (r *recNotifier) notify(text string, level int, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.text = text
	r.level = level
	r.count++
}

// last returns the most recent notice.
func (r *recNotifier) last() (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text, r.level
}

func newTestEngine(c Controller, n *recNotifier) *Engine {
	return NewEngine(Options{
		Controller: c,
		Notify:     n.notify,
		ConfirmTTL: 5 * time.Second,
	})
}

func TestEngineConfirmFlow(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	n := &recNotifier{}
	e := newTestEngine(c, n)
	ctx := context.Background()

	// A command stages a confirmation but does NOT touch the game yet.
	e.dispatch(ctx, "energy to 50")
	if e.pending == nil {
		t.Fatal("expected a pending plan after a command")
	}
	if text, level := n.last(); level != LevelConfirm || !strings.Contains(text, "CONFIRM") {
		t.Fatalf("expected confirm prompt, got level=%d text=%q", level, text)
	}
	if len(c.writes) != 0 {
		t.Fatalf("nothing should be written before confirmation, got %v", c.writes)
	}

	// Affirmation applies it.
	e.dispatch(ctx, "yes")
	if e.pending != nil {
		t.Error("pending should clear after applying")
	}
	if text, level := n.last(); level != LevelOK || !strings.Contains(text, "DONE") {
		t.Errorf("expected DONE, got level=%d text=%q", level, text)
	}
	if len(c.writes) != 1 || c.writes[0] != [2]int{6, 2} {
		t.Errorf("expected one energy write {6,2}, got %v", c.writes)
	}
}

func TestEngineCancel(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	n := &recNotifier{}
	e := newTestEngine(c, n)
	ctx := context.Background()

	e.dispatch(ctx, "change all tyres")
	e.dispatch(ctx, "cancel")
	if e.pending != nil {
		t.Error("cancel should drop the pending plan")
	}
	if len(c.writes) != 0 {
		t.Errorf("cancel must not write to the game, got %v", c.writes)
	}
}

func TestEngineConfirmTimeout(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	n := &recNotifier{}
	e := newTestEngine(c, n)
	ctx := context.Background()

	e.dispatch(ctx, "energy to 50")
	// Force the confirmation window to elapse.
	e.reapPending(time.Now().Add(time.Hour))
	if e.pending != nil {
		t.Error("expired plan should be reaped")
	}
	// A late "yes" finds nothing to confirm and does not write.
	e.dispatch(ctx, "yes")
	if len(c.writes) != 0 {
		t.Errorf("expired confirmation must not write, got %v", c.writes)
	}
	if text, _ := n.last(); !strings.Contains(text, "NOTHING") {
		t.Errorf("expected NOTHING TO CONFIRM, got %q", text)
	}
}

func TestEngineUnmappableCommand(t *testing.T) {
	// No fuel component in the menu -> the command resolves to nothing and is
	// reported, with no pending confirmation staged.
	c := &fakeController{menu: []rest.PitMenuItem{}}
	n := &recNotifier{}
	e := newTestEngine(c, n)

	e.dispatch(context.Background(), "fuel to 30")
	if e.pending != nil {
		t.Error("an unmappable command should not stage a confirmation")
	}
	if text, level := n.last(); level != LevelError {
		t.Errorf("expected an error notice, got level=%d text=%q", level, text)
	}
}

func TestEngineAffirmWithoutPending(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	n := &recNotifier{}
	e := newTestEngine(c, n)

	e.dispatch(context.Background(), "yes")
	if len(c.writes) != 0 {
		t.Errorf("affirm with nothing pending must not write, got %v", c.writes)
	}
}

// failingInterpreter stands in for an LLM that is down or too slow.
type failingInterpreter struct{ calls int }

func (f *failingInterpreter) Interpret(context.Context, string, func(string)) (Reply, error) {
	f.calls++
	return Reply{}, errors.New("model unavailable")
}

// With the engineer in the main path, a model outage must not take pit commands
// with it: the deterministic grammar still has to land the call.
func TestEngineFallsBackToGrammarWhenTheModelFails(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	n := &recNotifier{}
	fi := &failingInterpreter{}
	e := NewEngine(Options{Interpreter: fi, Controller: c, Notify: n.notify, ConfirmTTL: time.Minute})

	e.dispatch(context.Background(), "energy to 50")
	if fi.calls != 1 {
		t.Errorf("the interpreter should have been tried once, got %d", fi.calls)
	}
	if e.pending == nil {
		t.Fatal("the grammar should still have staged the command")
	}
	if _, level := n.last(); level != LevelConfirm {
		t.Errorf("expected a confirmation prompt, got level %d", level)
	}
}

// answeringInterpreter returns spoken prose, as the engineer does for a question.
type answeringInterpreter struct{ spoken []string }

func (a *answeringInterpreter) Interpret(_ context.Context, _ string, speak func(string)) (Reply, error) {
	if speak != nil {
		speak("Fuel is good for eleven laps.")
	}
	return Reply{Speech: "Fuel is good for eleven laps."}, nil
}

// A spoken answer is said as it streams, so the engine must not queue it a
// second time through the notice path.
func TestEngineDoesNotRespeakAStreamedAnswer(t *testing.T) {
	n := &recNotifier{}
	var spoken []string
	e := NewEngine(Options{
		Interpreter: &answeringInterpreter{},
		Controller:  &fakeController{menu: sampleMenu()},
		Notify:      n.notify,
		Speak:       func(s string) { spoken = append(spoken, s) },
	})

	e.dispatch(context.Background(), "how's my fuel?")
	if len(spoken) != 1 {
		t.Fatalf("the answer should be spoken exactly once, got %q", spoken)
	}
	if _, level := n.last(); level != LevelAnswer {
		t.Errorf("an answer should be notified at LevelAnswer, got %d", level)
	}
}

// The confirmation word must never depend on the model: it is the gate in front
// of every change that reaches the car.
func TestEngineConfirmsWithoutConsultingTheModel(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	n := &recNotifier{}
	fi := &failingInterpreter{}
	e := NewEngine(Options{Interpreter: fi, Controller: c, Notify: n.notify, ConfirmTTL: time.Minute})

	e.dispatch(context.Background(), "energy to 50") // staged via the grammar fallback
	before := fi.calls
	e.dispatch(context.Background(), "yes")
	if fi.calls != before {
		t.Errorf("confirming should not call the interpreter (%d extra calls)", fi.calls-before)
	}
	if len(c.writes) != 1 {
		t.Errorf("expected the confirmed change to be applied, got %v", c.writes)
	}
}

// setupInterpreter stands in for the engineer proposing a garage change.
type setupInterpreter struct{ plan Plan }

func (s *setupInterpreter) Interpret(context.Context, string, func(string)) (Reply, error) {
	p := s.plan
	return Reply{Plan: &p}, nil
}

// A setup change is confirmed like any other: read back, then applied only on
// "yes". This is the invariant that lets the engineer touch the car at all.
func TestSetupChangeIsConfirmedBeforeItReachesTheCar(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	n := &recNotifier{}
	plan := Plan{
		Setup: []SetupWrite{{Key: "REARWING", Value: 10, Name: "Rear Wing", Label: "10"}},
		Desc:  "REAR WING TO 10",
	}
	e := NewEngine(Options{Interpreter: &setupInterpreter{plan}, Controller: c, Notify: n.notify, ConfirmTTL: time.Minute})
	ctx := context.Background()

	e.dispatch(ctx, "give me a bit more rear wing")
	if e.pending == nil {
		t.Fatal("the setup change should be staged")
	}
	if len(c.setupWrites) != 0 {
		t.Fatalf("nothing may reach the car before confirmation, got %v", c.setupWrites)
	}
	if text, level := n.last(); level != LevelConfirm || !strings.Contains(text, "REAR WING") {
		t.Errorf("expected a readable confirmation prompt, got level=%d %q", level, text)
	}

	e.dispatch(ctx, "yes")
	if len(c.setupWrites) != 1 || c.setupWrites[0].Key != "REARWING" || c.setupWrites[0].Value != 10 {
		t.Errorf("expected the confirmed setup write, got %v", c.setupWrites)
	}
}

func TestSetupChangeIsDroppedOnCancel(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	n := &recNotifier{}
	plan := Plan{Setup: []SetupWrite{{Key: "BRAKEBIAS", Value: 57, Name: "Brake Bias"}}, Desc: "BRAKE BIAS TO 57"}
	e := NewEngine(Options{Interpreter: &setupInterpreter{plan}, Controller: c, Notify: n.notify, ConfirmTTL: time.Minute})
	ctx := context.Background()

	e.dispatch(ctx, "move the brake bias back")
	e.dispatch(ctx, "no")
	if e.pending != nil {
		t.Error("cancel should drop the staged setup change")
	}
	if len(c.setupWrites) != 0 {
		t.Errorf("cancel must not write to the car, got %v", c.setupWrites)
	}
}

// A failed write must be reported, not silently swallowed — the driver has to
// know the change did not take.
func TestFailedSetupWriteIsReported(t *testing.T) {
	c := &fakeController{menu: sampleMenu(), setupErr: errors.New("http 400")}
	n := &recNotifier{}
	plan := Plan{Setup: []SetupWrite{{Key: "REARWING", Value: 10, Name: "Rear Wing"}}, Desc: "REAR WING TO 10"}
	e := NewEngine(Options{Interpreter: &setupInterpreter{plan}, Controller: c, Notify: n.notify, ConfirmTTL: time.Minute})
	ctx := context.Background()

	e.dispatch(ctx, "more wing")
	e.dispatch(ctx, "yes")
	if text, level := n.last(); level != LevelError || !strings.Contains(text, "FAILED") {
		t.Errorf("a rejected write should be reported, got level=%d %q", level, text)
	}
}
