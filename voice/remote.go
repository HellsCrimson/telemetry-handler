package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// RemoteConfig points the voice client at the GPU voice server (see
// deploy/voice-server/). BaseURL is an http(s) or ws(s) URL — scheme and path
// are normalized per socket — and Token is the optional shared secret.
type RemoteConfig struct {
	BaseURL string
	Token   string
}

// ErrServerUnreachable marks a failure to reach the voice server at all, as
// opposed to a failure once connected. The engine turns it into a distinct
// on-screen message so the driver knows the box is down rather than the mic.
var ErrServerUnreachable = errors.New("voice server unreachable")

const (
	// dialTimeout is deliberately short: on a LAN the server either answers at
	// once or is not there, and a hung dial would stall the push-to-talk flow.
	dialTimeout = 3 * time.Second
	// readLimit caps a single server message. Audio frames are ~10 KB; the margin
	// is for JSON only.
	readLimit = 1 << 20
)

// socketURL builds the ws(s) URL for a server path from the configured base.
// "127.0.0.1:4400", "http://host:4400" and "ws://host:4400/" all work.
func socketURL(base, path, token string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", fmt.Errorf("voice.server_url is not set")
	}
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("voice.server_url %q: %w", base, err)
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("voice.server_url %q: unsupported scheme %q", base, u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("voice.server_url %q: missing host", base)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	if token != "" {
		q := u.Query()
		q.Set("token", token)
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// dialSocket opens one of the server's WebSocket endpoints, tagging a connection
// failure with ErrServerUnreachable.
func dialSocket(ctx context.Context, cfg RemoteConfig, path string) (*websocket.Conn, error) {
	target, err := socketURL(cfg.BaseURL, path, cfg.Token)
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(dctx, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrServerUnreachable, target, err)
	}
	conn.SetReadLimit(readLimit)
	return conn, nil
}

// message is the JSON envelope both sockets speak. Only the fields relevant to a
// given message kind are populated.
type message struct {
	Type       string  `json:"type"`
	Text       string  `json:"text,omitempty"`
	Message    string  `json:"message,omitempty"`
	Voice      string  `json:"voice,omitempty"`
	Speed      float64 `json:"speed,omitempty"`
	Language   string  `json:"language,omitempty"`
	SampleRate int     `json:"sample_rate,omitempty"`
	Channels   int     `json:"channels,omitempty"`
	Format     string  `json:"format,omitempty"`
}

func sendJSON(ctx context.Context, conn *websocket.Conn, m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

// decodeJSON unmarshals a server text frame.
func decodeJSON(data []byte, m *message) error {
	if err := json.Unmarshal(data, m); err != nil {
		return fmt.Errorf("bad server message: %w", err)
	}
	return nil
}

// readJSON reads the next text frame, skipping binary ones. It is used on the
// STT socket, which never sends binary.
func readJSON(ctx context.Context, conn *websocket.Conn) (message, error) {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return message{}, err
		}
		if typ != websocket.MessageText {
			continue
		}
		var m message
		if err := decodeJSON(data, &m); err != nil {
			return message{}, err
		}
		return m, nil
	}
}

// RemoteListener records one push-to-talk utterance and returns its transcript.
//
// The audio is streamed to the server *while the trigger is held*, so the only
// work left at release is the GPU decode — this is what removes the multi-second
// pause the local whisper.cpp path had.
//
// Each utterance gets its own connection. Holding one open between presses saves
// about 1 ms on a LAN and costs an entire utterance whenever the socket dies
// while idle: the library only answers the server's keepalive pings while a read
// is in flight, and between presses nothing is reading, so the server eventually
// closes the connection. Worse, the loss is silent until it is far too late —
// the opening write still succeeds against a closed peer, and the failure only
// surfaces part-way through streaming the audio, by which point the driver has
// already said their piece.
type RemoteListener struct {
	cfg      RemoteConfig
	recorder string // capture command override; empty uses the platform default
	language string
	logf     func(string, ...any)
}

// NewRemoteListener builds the streaming STT client. Nothing is dialed until an
// utterance, so a server that is still booting does not block startup.
func NewRemoteListener(cfg RemoteConfig, recorderCmd, language string, logf func(string, ...any)) (*RemoteListener, error) {
	if _, err := socketURL(cfg.BaseURL, "/v1/stt", cfg.Token); err != nil {
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &RemoteListener{cfg: cfg, recorder: recorderCmd, language: language, logf: logf}, nil
}

// Listen records until stop is closed, streaming the audio as it is captured,
// and returns the transcript the server decodes at the end.
func (l *RemoteListener) Listen(ctx context.Context, stop <-chan struct{}) (string, error) {
	conn, err := dialSocket(ctx, l.cfg, "/v1/stt")
	if err != nil {
		return "", err
	}
	defer conn.CloseNow()
	if err := sendJSON(ctx, conn, message{Type: "start", Language: l.language, SampleRate: captureSampleRate}); err != nil {
		return "", fmt.Errorf("stt start: %w", err)
	}

	rec, err := startRecorder(l.recorder)
	if err != nil {
		return "", err
	}

	// Pump the recorder into the socket until it is stopped and drained.
	pumped := make(chan error, 1)
	go func() { pumped <- l.pump(ctx, conn, rec) }()

	select {
	case <-stop:
	case <-ctx.Done():
	}
	// Keep recording briefly past the release: the last word usually ends right as
	// the driver lets go, and the recorder's period would otherwise clip it.
	rec.signalStop(captureTail)
	// Drain the recorder before reaping it (exec closes the stdout pipe in Wait),
	// forcing it down if it ignored the stop signal.
	var pumpErr error
	select {
	case pumpErr = <-pumped:
	case <-time.After(gracefulStopTimeout):
		rec.kill()
		pumpErr = <-pumped
	}
	rec.wait()

	if pumpErr != nil {
		return "", fmt.Errorf("stt stream: %w", pumpErr)
	}
	if err := sendJSON(ctx, conn, message{Type: "eos"}); err != nil {
		return "", fmt.Errorf("stt eos: %w", err)
	}

	// The decode is bounded so a wedged server cannot hang the engine.
	rctx, cancel := context.WithTimeout(ctx, transcribeTimeout)
	defer cancel()
	for {
		m, err := readJSON(rctx, conn)
		if err != nil {
			return "", fmt.Errorf("stt read: %w", err)
		}
		switch m.Type {
		case "partial":
			l.logf("voice: partial %q", m.Text)
		case "final":
			return m.Text, nil
		case "error":
			return "", fmt.Errorf("stt: %s", m.Message)
		}
	}
}

// pump copies raw PCM from the recorder to the server until the recorder exits.
func (l *RemoteListener) pump(ctx context.Context, conn *websocket.Conn, rec *pcmRecorder) error {
	buf := make([]byte, 4096)
	for {
		n, err := rec.out.Read(buf)
		if n > 0 {
			if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return err
		}
	}
}
