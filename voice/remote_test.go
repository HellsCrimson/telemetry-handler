package voice

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestSocketURL(t *testing.T) {
	cases := []struct {
		base, path, token, want string
	}{
		{"http://127.0.0.1:4400", "/v1/stt", "", "ws://127.0.0.1:4400/v1/stt"},
		{"127.0.0.1:4400", "/v1/tts", "", "ws://127.0.0.1:4400/v1/tts"},
		{"ws://box:4400/", "/v1/stt", "", "ws://box:4400/v1/stt"},
		{"https://box", "/v1/tts", "", "wss://box/v1/tts"},
		{"http://box:4400", "/v1/stt", "s3cret", "ws://box:4400/v1/stt?token=s3cret"},
	}
	for _, c := range cases {
		got, err := socketURL(c.base, c.path, c.token)
		if err != nil {
			t.Fatalf("socketURL(%q): %v", c.base, err)
		}
		if got != c.want {
			t.Errorf("socketURL(%q, %q) = %q, want %q", c.base, c.path, got, c.want)
		}
	}
}

func TestSocketURLRejectsBadInput(t *testing.T) {
	for _, base := range []string{"", "   ", "ftp://box:4400"} {
		if _, err := socketURL(base, "/v1/stt", ""); err == nil {
			t.Errorf("socketURL(%q) should have failed", base)
		}
	}
}

// A voice server that cannot be reached must be distinguishable from any other
// failure, because there is no local fallback and the driver needs to know which
// half is broken.
func TestListenErrorNoticeSeparatesServerOutage(t *testing.T) {
	unreachable := fmt.Errorf("stt: %w: dial", ErrServerUnreachable)
	if got := listenErrorNotice(unreachable); got != "VOICE SERVER OFFLINE" {
		t.Errorf("unreachable server notice = %q", got)
	}
	if got := listenErrorNotice(errors.New("recorder died")); got != "VOICE ERROR" {
		t.Errorf("generic notice = %q", got)
	}
}

// fakeListener stands in for the streaming STT client: it reports what the
// driver "said" once the trigger is released.
type fakeListener struct {
	text     string
	err      error
	released chan struct{}
}

func (f *fakeListener) Listen(_ context.Context, stop <-chan struct{}) (string, error) {
	<-stop
	if f.released != nil {
		close(f.released)
	}
	return f.text, f.err
}

// manualTrigger drives press/release from the test.
type manualTrigger struct{ ch chan Event }

func (m *manualTrigger) Events() <-chan Event { return m.ch }

func TestEngineRunListensBetweenPressAndRelease(t *testing.T) {
	c := &fakeController{menu: sampleMenu()}
	n := &recNotifier{}
	trig := &manualTrigger{ch: make(chan Event, 2)}
	lis := &fakeListener{text: "energy to 50", released: make(chan struct{})}
	e := NewEngine(Options{Trigger: trig, Listener: lis, Controller: c, Notify: n.notify, ConfirmTTL: time.Minute})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	trig.ch <- EventPress
	trig.ch <- EventRelease
	select {
	case <-lis.released:
	case <-time.After(2 * time.Second):
		t.Fatal("listener was never released")
	}

	// The transcript should stage a confirmation without writing to the game.
	deadline := time.After(2 * time.Second)
	for {
		text, level := n.last()
		if level == LevelConfirm {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no confirmation staged (level=%d text=%q)", level, text)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if len(c.writes) != 0 {
		t.Errorf("nothing should be written before confirmation, got %v", c.writes)
	}
}

func TestEngineRunReportsServerOutage(t *testing.T) {
	n := &recNotifier{}
	trig := &manualTrigger{ch: make(chan Event, 2)}
	lis := &fakeListener{err: fmt.Errorf("stt: %w: dial", ErrServerUnreachable)}
	e := NewEngine(Options{Trigger: trig, Listener: lis, Controller: &fakeController{menu: sampleMenu()}, Notify: n.notify})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	trig.ch <- EventPress
	trig.ch <- EventRelease
	deadline := time.After(2 * time.Second)
	for {
		text, _ := n.last()
		if text == "VOICE SERVER OFFLINE" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("expected an offline notice, got %q", text)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
