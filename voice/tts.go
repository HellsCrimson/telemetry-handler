package voice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// speakTimeout bounds one utterance end to end (connect, synthesize, play).
const speakTimeout = 60 * time.Second

// TTSConfig configures spoken output. Synthesis happens on the GPU voice server
// (see deploy/voice-server/); audio streams back as raw PCM and is played as it
// arrives, so speech starts on the first chunk instead of after the whole
// utterance has been rendered.
type TTSConfig struct {
	Remote RemoteConfig
	// Voice is the Kokoro voice id (e.g. "af_sarah"); empty uses the server default.
	Voice string
	// Speed is the speech rate, 1.0 = normal; 0 uses the server default.
	Speed float64
	// PlayerCmd overrides the audio player. "{rate}" and "{channels}" are replaced
	// with the stream's format; the command must read raw PCM from stdin.
	PlayerCmd string
}

// Speaker turns a message into spoken audio. Speak is non-blocking and fire-and-
// forget: synthesis and playback happen on a worker so the caller (the engine's
// notify path) is never stalled.
type Speaker interface {
	Speak(text string)
	// Stop silences whatever is being spoken and drops anything queued behind it.
	// It is what makes the assistant interruptible: the driver pressing the
	// trigger should cut it off mid-sentence, not talk over it.
	Stop()
}

// NewSpeaker builds an async Speaker that runs until ctx is cancelled. The socket
// is dialed lazily on the first utterance and redialed after an error, so a
// voice server that is still booting does not block startup. Failures at speak
// time are logged, not fatal.
func NewSpeaker(ctx context.Context, cfg TTSConfig, logf func(string, ...any)) (Speaker, error) {
	if _, err := socketURL(cfg.Remote.BaseURL, "/v1/tts", cfg.Remote.Token); err != nil {
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	// The queue holds a streamed answer's sentences as well as one-off notices,
	// so it is deep enough that a multi-sentence reply is never clipped.
	s := &remoteSpeaker{ctx: ctx, cfg: cfg, logf: logf, ch: make(chan string, 16)}
	go s.loop()
	return s, nil
}

// SpeakTiming reports where an utterance's time went.
//
// FirstAudio is the only latency figure: the wait from asking for the phrase to
// the first sample reaching the player. Total also covers *speaking* it, so it
// grows with the length of the message — a longer sentence taking longer to say
// is not lag, and comparing Totals across phrases measures nothing.
type SpeakTiming struct {
	FirstAudio time.Duration `json:"first_audio_ms"`
	Total      time.Duration `json:"total_ms"`
}

// SpeakOnce synthesizes and plays text synchronously on a throwaway connection,
// returning the timing and any error. Used by the dashboard's "Test voice"
// button so the user gets immediate feedback on a misconfigured server.
func SpeakOnce(ctx context.Context, cfg TTSConfig, text string) (SpeakTiming, error) {
	conn, err := dialSocket(ctx, cfg.Remote, "/v1/tts")
	if err != nil {
		return SpeakTiming{}, err
	}
	defer conn.CloseNow()
	return utter(ctx, conn, cfg, text, nil)
}

type remoteSpeaker struct {
	ctx  context.Context
	cfg  TTSConfig
	logf func(string, ...any)
	ch   chan string

	// playing and cancel belong to the utterance in flight and are read by Stop
	// from another goroutine, so they are mutex-guarded.
	mu      sync.Mutex
	playing *rawPlayer
	cancel  context.CancelFunc
}

// Speak queues text, dropping it if the worker is already backed up (a few quick
// messages shouldn't pile up an unbounded backlog of speech).
func (s *remoteSpeaker) Speak(text string) {
	select {
	case s.ch <- text:
	default:
	}
}

// Stop cuts off the current utterance: the player is killed for immediate
// silence, and cancelling the utterance context unblocks the worker's read.
// That closes the socket, which the server sees as a disconnect and uses to
// abandon the synthesis still queued on the GPU.
func (s *remoteSpeaker) Stop() {
drain:
	for {
		select {
		case <-s.ch:
		default:
			break drain
		}
	}
	s.mu.Lock()
	play, cancel := s.playing, s.cancel
	s.mu.Unlock()
	if play != nil {
		play.kill()
	}
	if cancel != nil {
		cancel()
	}
}

func (s *remoteSpeaker) setPlaying(p *rawPlayer) {
	s.mu.Lock()
	s.playing = p
	s.mu.Unlock()
}

func (s *remoteSpeaker) loop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case text := <-s.ch:
			if err := s.utter(text); err != nil {
				s.logf("voice: tts: %v", err)
			}
		}
	}
}

// utter speaks one message on its own connection. Like the listener, the socket
// is not pooled between messages: a dial costs about a millisecond on a LAN, and
// a connection held open across a quiet spell is one the server will close
// underneath us.
func (s *remoteSpeaker) utter(text string) error {
	ctx, cancel := context.WithTimeout(s.ctx, speakTimeout)
	defer cancel()
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
	}()

	conn, err := dialSocket(ctx, s.cfg.Remote, "/v1/tts")
	if err != nil {
		return err
	}
	defer conn.CloseNow()

	timing, err := utter(ctx, conn, s.cfg, text, s.setPlaying)
	if err != nil {
		// An interruption is the driver talking over us, not a failure.
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	// Log the wait before the phrase is audible, not how long it took to say —
	// keeping that number small is why synthesis runs on the GPU box.
	s.logf("voice: spoke %q, audio started in %v", text, timing.FirstAudio.Round(time.Millisecond))
	return nil
}

// utter sends one speak request and plays the PCM the server streams back. The
// player is started from the "begin" message so it matches the stream's format.
func utter(ctx context.Context, conn *websocket.Conn, cfg TTSConfig, text string, onPlayer func(*rawPlayer)) (SpeakTiming, error) {
	var timing SpeakTiming
	if onPlayer == nil {
		onPlayer = func(*rawPlayer) {}
	}
	defer onPlayer(nil)
	if strings.TrimSpace(text) == "" {
		return timing, nil
	}
	started := time.Now()
	if err := sendJSON(ctx, conn, message{Type: "speak", Text: text, Voice: cfg.Voice, Speed: cfg.Speed}); err != nil {
		return timing, fmt.Errorf("speak: %w", err)
	}

	var play *rawPlayer
	defer func() {
		if play != nil {
			play.kill()
		}
	}()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return timing, fmt.Errorf("tts read: %w", err)
		}
		if typ == websocket.MessageBinary {
			if play == nil {
				continue // audio before "begin" — nothing to play it with
			}
			if err := play.write(data); err != nil {
				// A dead player mid-stream means the driver interrupted us, which
				// is normal operation rather than a fault.
				if play.killed.Load() {
					return timing, context.Canceled
				}
				return timing, fmt.Errorf("play: %w", err)
			}
			if timing.FirstAudio == 0 {
				timing.FirstAudio = time.Since(started)
			}
			continue
		}
		var m message
		if err := decodeJSON(data, &m); err != nil {
			return timing, err
		}
		switch m.Type {
		case "begin":
			play, err = startPlayer(cfg.PlayerCmd, m.SampleRate, m.Channels)
			if err != nil {
				return timing, err
			}
			onPlayer(play)
		case "end":
			if play == nil {
				return timing, nil
			}
			p := play
			play = nil
			err := p.finish()
			timing.Total = time.Since(started)
			return timing, err
		case "error":
			return timing, fmt.Errorf("tts: %s", m.Message)
		}
	}
}

// rawPlayer is an external player consuming raw PCM on stdin.
//
// It is killable from another goroutine (barge-in) while the speaker's worker is
// still writing to it, so the process is reaped exactly once by a dedicated
// goroutine and both finish and kill just wait on that — calling either twice,
// or both at once, is safe.
type rawPlayer struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	closeIn sync.Once
	done    chan struct{} // closed once Wait has returned
	waitErr error         // valid after done is closed
	killed  atomic.Bool   // set by kill, so a failed write reads as a barge-in
}

// startPlayer launches the player for a stream of the given format. override, if
// set, replaces the platform default; "{rate}"/"{channels}" are substituted.
func startPlayer(override string, rate, channels int) (*rawPlayer, error) {
	if rate <= 0 {
		rate = 24000
	}
	if channels <= 0 {
		channels = 1
	}
	var (
		name string
		args []string
		err  error
	)
	if strings.TrimSpace(override) != "" {
		fields := strings.Fields(override)
		name, args = fields[0], fields[1:]
	} else if name, args, err = defaultPlayerCommand(); err != nil {
		return nil, err
	}
	for i, a := range args {
		a = strings.ReplaceAll(a, "{rate}", strconv.Itoa(rate))
		args[i] = strings.ReplaceAll(a, "{channels}", strconv.Itoa(channels))
	}
	cmd := exec.Command(name, args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("player stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	p := &rawPlayer{cmd: cmd, in: in, done: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

func (p *rawPlayer) write(b []byte) error {
	_, err := p.in.Write(b)
	return err
}

// finish closes the stream and waits for playback to drain.
func (p *rawPlayer) finish() error {
	p.closeStdin()
	select {
	case <-p.done:
		return p.waitErr
	case <-time.After(speakTimeout):
		p.kill()
		return fmt.Errorf("player did not exit")
	}
}

// kill stops playback immediately. Safe to call concurrently with finish, and
// more than once.
func (p *rawPlayer) kill() {
	p.killed.Store(true)
	p.closeStdin()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	<-p.done
}

func (p *rawPlayer) closeStdin() {
	p.closeIn.Do(func() { _ = p.in.Close() })
}
