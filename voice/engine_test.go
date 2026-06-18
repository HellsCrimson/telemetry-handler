package voice

import (
	"context"
	"strings"
	"testing"
	"time"

	"telemetry-handler/game/lmu/rest"
)

type recNotifier struct {
	text  string
	level int
	count int
}

func (r *recNotifier) notify(text string, level int, _ time.Duration) {
	r.text = text
	r.level = level
	r.count++
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
	if n.level != LevelConfirm || !strings.Contains(n.text, "CONFIRM") {
		t.Fatalf("expected confirm prompt, got level=%d text=%q", n.level, n.text)
	}
	if len(c.writes) != 0 {
		t.Fatalf("nothing should be written before confirmation, got %v", c.writes)
	}

	// Affirmation applies it.
	e.dispatch(ctx, "yes")
	if e.pending != nil {
		t.Error("pending should clear after applying")
	}
	if n.level != LevelOK || !strings.Contains(n.text, "DONE") {
		t.Errorf("expected DONE, got level=%d text=%q", n.level, n.text)
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
	if !strings.Contains(n.text, "NOTHING") {
		t.Errorf("expected NOTHING TO CONFIRM, got %q", n.text)
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
	if n.level != LevelError {
		t.Errorf("expected an error notice, got level=%d text=%q", n.level, n.text)
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
