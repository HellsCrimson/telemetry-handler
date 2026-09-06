package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// stubServer speaks the voice server's wire protocol without any models, so the
// streaming client can be exercised end to end: the STT socket reports how much
// audio actually arrived, and the TTS socket streams a fixed PCM payload back.
type stubServer struct {
	audio []byte // PCM the TTS socket sends
}

func (s *stubServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stt", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		var got int
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				got += len(data)
				continue
			}
			var m message
			if err := json.Unmarshal(data, &m); err != nil {
				return
			}
			switch m.Type {
			case "start":
				got = 0
			case "eos":
				reply, _ := json.Marshal(message{Type: "final", Text: fmt.Sprintf("received %d bytes", got)})
				if err := conn.Write(ctx, websocket.MessageText, reply); err != nil {
					return
				}
			}
		}
	})
	mux.HandleFunc("/v1/tts", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m message
			if err := json.Unmarshal(data, &m); err != nil || m.Type != "speak" {
				continue
			}
			begin, _ := json.Marshal(message{Type: "begin", SampleRate: 24000, Channels: 1, Format: "s16le"})
			if err := conn.Write(ctx, websocket.MessageText, begin); err != nil {
				return
			}
			// Two frames, to prove the client plays a stream rather than one blob.
			half := len(s.audio) / 2
			for _, frame := range [][]byte{s.audio[:half], s.audio[half:]} {
				if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
					return
				}
			}
			end, _ := json.Marshal(message{Type: "end"})
			if err := conn.Write(ctx, websocket.MessageText, end); err != nil {
				return
			}
		}
	})
	return mux
}

// TestListenStreamsAudioAndReturnsTranscript checks the whole input path: the
// recorder's PCM is streamed over the socket while the trigger is held, and the
// transcript the server returns at "eos" comes back from Listen.
func TestListenStreamsAudioAndReturnsTranscript(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the voice client is Linux-only")
	}
	srv := httptest.NewServer((&stubServer{}).handler())
	defer srv.Close()

	// A fixed amount of silence stands in for the microphone.
	const wantBytes = 32000 // 1s of 16 kHz mono s16
	listener, err := NewRemoteListener(
		RemoteConfig{BaseURL: srv.URL},
		fmt.Sprintf("head -c %d /dev/zero", wantBytes),
		"en",
		t.Logf,
	)
	if err != nil {
		t.Fatalf("NewRemoteListener: %v", err)
	}

	stop := make(chan struct{})
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(stop)
	}()
	text, err := listener.Listen(context.Background(), stop)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if want := fmt.Sprintf("received %d bytes", wantBytes); text != want {
		t.Errorf("transcript = %q, want %q (audio did not stream intact)", text, want)
	}
}

// TestListenHandlesConsecutiveUtterances covers back-to-back presses, each on
// its own connection.
func TestListenHandlesConsecutiveUtterances(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the voice client is Linux-only")
	}
	srv := httptest.NewServer((&stubServer{}).handler())
	defer srv.Close()

	listener, err := NewRemoteListener(RemoteConfig{BaseURL: srv.URL}, "head -c 16000 /dev/zero", "en", t.Logf)
	if err != nil {
		t.Fatalf("NewRemoteListener: %v", err)
	}

	for i := range 2 {
		stop := make(chan struct{})
		close(stop)
		text, err := listener.Listen(context.Background(), stop)
		if err != nil {
			t.Fatalf("utterance %d: %v", i, err)
		}
		if text != "received 16000 bytes" {
			t.Errorf("utterance %d transcript = %q", i, text)
		}
	}
}

// TestSpeakOncePlaysTheStreamedAudio checks the output path: every PCM frame the
// server streams reaches the player, in order.
func TestSpeakOncePlaysTheStreamedAudio(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the voice client is Linux-only")
	}
	audio := make([]byte, 4800)
	for i := range audio {
		audio[i] = byte(i)
	}
	srv := httptest.NewServer((&stubServer{audio: audio}).handler())
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "played.pcm")
	cfg := TTSConfig{
		Remote:    RemoteConfig{BaseURL: srv.URL},
		Voice:     "af_sarah",
		Speed:     1,
		PlayerCmd: "dd of=" + out + " status=none",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	timing, err := SpeakOnce(ctx, cfg, "box this lap")
	if err != nil {
		t.Fatalf("SpeakOnce: %v", err)
	}
	if timing.FirstAudio <= 0 {
		t.Error("FirstAudio was not measured")
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read played audio: %v", err)
	}
	if len(got) != len(audio) {
		t.Fatalf("played %d bytes, want %d", len(got), len(audio))
	}
	for i := range got {
		if got[i] != audio[i] {
			t.Fatalf("played audio differs at byte %d", i)
		}
	}
}

// TestSpeakOnceReportsAnOfflineServer is the no-fallback contract: a dead server
// must surface as ErrServerUnreachable rather than silently doing nothing.
func TestSpeakOnceReportsAnOfflineServer(t *testing.T) {
	cfg := TTSConfig{Remote: RemoteConfig{BaseURL: "http://127.0.0.1:1"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := SpeakOnce(ctx, cfg, "anyone there")
	if err == nil {
		t.Fatal("expected an error from an unreachable server")
	}
	if notice := listenErrorNotice(err); notice != "VOICE SERVER OFFLINE" {
		t.Errorf("error %v was not recognized as an outage (notice %q)", err, notice)
	}
}

// slowTTSServer streams audio in small frames with a gap between them, so a
// barge-in can land in the middle of an utterance.
func slowTTSServer(t *testing.T, frames int, gap time.Duration) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/tts", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m message
			if err := json.Unmarshal(data, &m); err != nil || m.Type != "speak" {
				continue
			}
			begin, _ := json.Marshal(message{Type: "begin", SampleRate: 24000, Channels: 1, Format: "s16le"})
			if err := conn.Write(ctx, websocket.MessageText, begin); err != nil {
				return
			}
			for range frames {
				if err := conn.Write(ctx, websocket.MessageBinary, make([]byte, 480)); err != nil {
					return
				}
				time.Sleep(gap)
			}
			end, _ := json.Marshal(message{Type: "end"})
			_ = conn.Write(ctx, websocket.MessageText, end)
		}
	})
	return httptest.NewServer(mux)
}

// TestSpeakerStopInterruptsPlayback is the barge-in contract: pressing the
// trigger must cut the assistant off part-way through, not wait for it to
// finish the phrase.
func TestSpeakerStopInterruptsPlayback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the voice client is Linux-only")
	}
	srv := slowTTSServer(t, 40, 25*time.Millisecond) // ~1s of streaming
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "played.pcm")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp, err := NewSpeaker(ctx, TTSConfig{
		Remote:    RemoteConfig{BaseURL: srv.URL},
		PlayerCmd: "dd of=" + out + " status=none",
	}, t.Logf)
	if err != nil {
		t.Fatalf("NewSpeaker: %v", err)
	}

	sp.Speak("a long message the driver wants to interrupt")
	time.Sleep(200 * time.Millisecond) // let a few frames play
	stopped := time.Now()
	sp.Stop()

	// Playback must end promptly rather than running the full ~1s stream.
	if elapsed := time.Since(stopped); elapsed > 300*time.Millisecond {
		t.Errorf("Stop took %v to silence playback", elapsed)
	}
	// And the queue must be empty, so nothing resumes behind it.
	time.Sleep(300 * time.Millisecond)
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read played audio: %v", err)
	}
	if len(got) >= 40*480 {
		t.Errorf("played %d bytes — the whole utterance got through despite Stop", len(got))
	}
	t.Logf("interrupted after %d of %d bytes", len(got), 40*480)
}

// TestSpeakerStopDropsQueuedMessages checks the backlog is discarded too: after
// a barge-in the assistant must not carry on with what it was going to say.
func TestSpeakerStopDropsQueuedMessages(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the voice client is Linux-only")
	}
	srv := slowTTSServer(t, 20, 20*time.Millisecond)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp, err := NewSpeaker(ctx, TTSConfig{
		Remote:    RemoteConfig{BaseURL: srv.URL},
		PlayerCmd: "dd of=/dev/null status=none",
	}, t.Logf)
	if err != nil {
		t.Fatalf("NewSpeaker: %v", err)
	}
	rs := sp.(*remoteSpeaker)

	sp.Speak("first")
	sp.Speak("second")
	sp.Speak("third")
	time.Sleep(100 * time.Millisecond)
	sp.Stop()

	if n := len(rs.ch); n != 0 {
		t.Errorf("%d messages still queued after Stop", n)
	}
}

// droppingServer closes each connection after one utterance, reproducing an idle
// socket the server has reaped between presses.
func droppingServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	conns := 0
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stt", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		conns++
		mu.Unlock()
		defer conn.CloseNow()
		ctx := r.Context()
		var got int
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				got += len(data)
				continue
			}
			var m message
			if err := json.Unmarshal(data, &m); err != nil {
				return
			}
			if m.Type == "eos" {
				reply, _ := json.Marshal(message{Type: "final", Text: fmt.Sprintf("received %d bytes", got)})
				_ = conn.Write(ctx, websocket.MessageText, reply)
				return // drop the connection, as an idle-timeout would
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &conns
}

// A socket the server closed while idle must not cost the driver an utterance:
// the client has to notice on the opening write and redial transparently.
func TestListenRecoversFromAStaleConnection(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the voice client is Linux-only")
	}
	srv, conns := droppingServer(t)

	listener, err := NewRemoteListener(RemoteConfig{BaseURL: srv.URL}, "head -c 16000 /dev/zero", "en", t.Logf)
	if err != nil {
		t.Fatalf("NewRemoteListener: %v", err)
	}

	for i := range 3 {
		stop := make(chan struct{})
		close(stop)
		text, err := listener.Listen(context.Background(), stop)
		if err != nil {
			t.Fatalf("utterance %d failed on a stale socket: %v", i, err)
		}
		if text != "received 16000 bytes" {
			t.Errorf("utterance %d transcript = %q", i, text)
		}
	}
	if *conns < 3 {
		t.Errorf("expected a redial per utterance, got %d connections", *conns)
	}
}
