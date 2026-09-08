package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	defaultPath         = "config.json"
	defaultListenAddr   = "0.0.0.0"
	defaultListenPort   = 20440
	defaultPrintHz      = 5
	defaultRecordDir    = "recordings"
	defaultOverlayHz    = 10
	defaultOpacity      = 0.85
	defaultSteeringSize = 60
	// defaultSteeringRangeDeg is the lock-to-lock steering rotation (degrees) the
	// overlay wheel uses when the game does not report a per-car range (Forza).
	// LMU reports its own range per car, which overrides this.
	defaultSteeringRangeDeg = 1080
	defaultOverlayWidth     = 320
	defaultOverlayHeight    = 210
	defaultOverlayAnchor    = "top-left"
	// defaultGameWindowMatch is matched (case-insensitively, as a substring)
	// against window class/title when auto-detecting which monitor the game is
	// on. The Proton/Wine window class for Forza is not guaranteed, so this is
	// configurable via overlay.game_window_match.
	defaultGameWindowMatch = "forza"
	// defaultLMUBaseURL / defaultLMUPollHz configure polling of LMU's local REST
	// API. 1 Hz is plenty for strategy data (pit estimates, fuel/energy
	// projections, weather forecast) — it changes on the scale of laps, not frames.
	defaultLMUBaseURL = "http://localhost:6397"
	defaultLMUPollHz  = 1

	// Voice (push-to-talk) defaults.
	defaultVoiceLanguage = "en"
	defaultVoiceTrigger  = "fifo"
	defaultVoiceFIFOPath = "/tmp/telemetry-handler-ptt"
	defaultVoiceConfirm  = 6.0
	// defaultVoiceServer is where the GPU voice server (deploy/voice-server/)
	// listens; override it in the dashboard if it runs elsewhere.
	defaultVoiceServer = "http://127.0.0.1:4400"
	defaultTTSVoice    = "af_sarah"
	// Engineer defaults: a short budget, because a late answer is a useless one.
	defaultEngineerTimeout     = 30.0
	defaultEngineerMaxTokens   = 220
	defaultEngineerTemperature = 0.3
	// UI scale bounds. Below 0.7 the 9.5px labels stop being legible at all;
	// above 2.0 the dense tables no longer fit a screen, which defeats them.
	minUIScale = 0.7
	maxUIScale = 2.0
)

type Color [3]uint8

type Config struct {
	ListenAddr string    `json:"listen_addr"`
	ListenPort int       `json:"listen_port"`
	PrintHz    float64   `json:"print_hz"`
	Moza       Moza      `json:"moza"`
	Recording  Recording `json:"recording"`
	Terminal   Terminal  `json:"terminal_print"`
	Overlay    Overlay   `json:"overlay"`
	LMU        LMU       `json:"lmu"`
	Voice      Voice     `json:"voice"`

	// UIScale magnifies the dashboard webview. The interface is drawn at a
	// deliberate density (9.5px micro labels, 28px table rows), which is right at
	// 1080p and small on a 1440p or 4K panel — so this scales the whole thing
	// rather than letting individual type sizes drift apart.
	UIScale float64 `json:"ui_scale,omitempty"`
}

// UIScaleValue returns the webview magnification to apply, clamped to something
// usable. 0 (an old config, or an unset field) means 1.0.
func (c Config) UIScaleValue() float64 {
	if c.UIScale <= 0 {
		return 1
	}
	return min(max(c.UIScale, minUIScale), maxUIScale)
}

// Voice configures the push-to-talk voice-command assistant (streaming STT +
// TTS on the GPU voice server, driving LMU's pit menu through its REST API with
// a deterministic grammar). Linux-only, disabled by default. The trigger is
// either an external FIFO that something writes "press"/"release" to (a Hyprland
// keybind, a wheel-button script) or a configured evdev button read directly
// from /dev/input.
type Voice struct {
	Enabled bool `json:"enabled"`
	// ServerURL is the voice server (deploy/voice-server/), e.g.
	// "http://127.0.0.1:4400". Both speech-to-text and text-to-speech run
	// there; there is no local fallback.
	ServerURL string `json:"server_url"`
	// ServerToken is the optional shared secret (VOICE_TOKEN on the server).
	ServerToken string `json:"server_token,omitempty"`
	Language    string `json:"language,omitempty"`
	// CaptureCmd optionally overrides the recorder. It must write raw s16le mono
	// 16 kHz PCM to stdout; empty uses parecord. Example:
	// "parecord --raw --format=s16le --rate=16000 --channels=1 --device=alsa_input.x".
	CaptureCmd string `json:"capture_cmd,omitempty"`
	// Trigger is "fifo" (default) or "button".
	Trigger string `json:"trigger,omitempty"`
	// FIFOPath is the named pipe for trigger=="fifo".
	FIFOPath string `json:"fifo_path,omitempty"`
	// ButtonDevice/ButtonCode are the evdev device + key code for trigger=="button"
	// (populate them with the LearnVoiceButton helper rather than by hand).
	ButtonDevice string `json:"button_device,omitempty"`
	ButtonCode   int    `json:"button_code,omitempty"`
	// ConfirmSeconds is how long a staged pit change waits for an affirmation
	// ("yes") before it is dropped. 0 uses the built-in default.
	ConfirmSeconds float64 `json:"confirm_seconds,omitempty"`
	// TTS reads the assistant's messages (confirmation prompts, results) aloud.
	TTS VoiceTTS `json:"tts"`
	// Engineer is the LLM race engineer layered on top of the deterministic
	// grammar: it answers questions and turns free-form requests into pit
	// commands. Disabled by default; the grammar works without it.
	Engineer Engineer `json:"engineer"`
}

// Engineer configures the LLM race engineer. The endpoint is any
// OpenAI-compatible chat API (Unsloth Studio, vLLM, llama.cpp).
type Engineer struct {
	Enabled bool `json:"enabled"`
	// BaseURL is the API root, with or without the /v1 suffix.
	BaseURL string `json:"base_url"`
	// APIKey authenticates to the endpoint. Prefer the TELEMETRY_ENGINEER_API_KEY
	// environment variable, which overrides this — a key in config.json is easy
	// to leak when sharing the file.
	APIKey string `json:"api_key,omitempty"`
	Model  string `json:"model"`
	// TimeoutSeconds bounds one answer. Kept short: an engineer that replies
	// after the corner is worse than one that says nothing.
	TimeoutSeconds float64 `json:"timeout_seconds,omitempty"`
	// MaxTokens caps the reply length, which is what keeps answers radio-brief.
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	// Callouts enables unprompted radio (flags, fuel, rivals pitting, traffic).
	// These are rule-based, not model-generated.
	Callouts bool `json:"callouts"`
}

// APIKeyValue returns the key to use, preferring the environment variable so the
// secret need not live in config.json.
func (e Engineer) APIKeyValue() string {
	if v := strings.TrimSpace(os.Getenv(EngineerAPIKeyEnv)); v != "" {
		return v
	}
	return strings.TrimSpace(e.APIKey)
}

// EngineerAPIKeyEnv is the environment variable holding the engineer's API key.
const EngineerAPIKeyEnv = "TELEMETRY_ENGINEER_API_KEY"

// Validate checks the engineer config when enabled.
func (e Engineer) Validate() error {
	if !e.Enabled {
		return nil
	}
	if strings.TrimSpace(e.BaseURL) == "" {
		return fmt.Errorf("voice.engineer.base_url is required when the engineer is enabled")
	}
	if strings.TrimSpace(e.Model) == "" {
		return fmt.Errorf("voice.engineer.model is required when the engineer is enabled")
	}
	if e.TimeoutSeconds < 0 {
		return fmt.Errorf("voice.engineer.timeout_seconds must be >= 0")
	}
	return nil
}

// VoiceTTS configures spoken output. Synthesis runs on the same voice server as
// speech-to-text (Kokoro on the GPU) and streams back as PCM played as it
// arrives, so speech starts on the first chunk.
type VoiceTTS struct {
	Enabled bool `json:"enabled"`
	// Voice is the Kokoro voice id (e.g. "af_sarah"); empty uses the server's.
	Voice string `json:"voice,omitempty"`
	// Speed is the speech rate, 1.0 = normal.
	Speed float64 `json:"speed,omitempty"`
	// PlayerCmd overrides the audio player; it must read raw PCM from stdin, with
	// "{rate}"/"{channels}" substituted. Empty uses pacat.
	PlayerCmd string `json:"player_cmd,omitempty"`
	// SpeakInfo also reads the neutral transcript echo aloud; by default only the
	// confirmation prompt, result and errors are spoken.
	SpeakInfo bool `json:"speak_info,omitempty"`
}

// LMU configures polling of Le Mans Ultimate's local REST API (port 6397), which
// exposes strategy/weather-forecast/pit data the rF2 shared memory does not. The
// API is reachable from the host directly, so no sidecar is involved. When
// Enabled, the app polls it while LMU telemetry is live and merges the result
// into the strategy session model.
type LMU struct {
	Enabled bool    `json:"enabled"`
	BaseURL string  `json:"base_url"`
	PollHz  float64 `json:"poll_hz"`
}

type Moza struct {
	Enabled       bool      `json:"enabled"`
	Port          string    `json:"port"`
	UpdateHz      float64   `json:"update_hz"`
	RPMBrightness uint8     `json:"rpm_brightness"`
	RPMColors     [10]Color `json:"rpm_colors"`
	ButtonColors  [10]Color `json:"button_colors"`
	ButtonMask    uint16    `json:"button_mask"`
	// RPMLEDs manually sets the number of rev-light segments on the wheel rim.
	// The rim is not identifiable over USB (only the base is), so when the base's
	// default profile does not match the attached rim, set this to override it.
	// 0 means "auto" — use the detected base's profile (or the default).
	RPMLEDs int `json:"rpm_leds,omitempty"`
	// Protocol selects the rim's LED protocol: "" / "old" for the legacy
	// telemetry-mask rims (default, unchanged), "new" for newer rims such as the
	// ESX whose LEDs live on a separate device and need a channel-config burst, or
	// "auto" to detect at connect by querying the rim over serial (the new LED
	// controller, device 0x18, answers a model-code query; legacy rims stay
	// silent). The rim is not identifiable over USB, hence the serial probe.
	Protocol string `json:"protocol,omitempty"`
	// RPMCurvePoints defines the rev-light response curve as control points in
	// normalised [0,1]×[0,1] space (input RPM ratio → output bar fill), drawn in
	// the dashboard as a draggable spline (photo-editor "curves" style). The
	// points are interpolated with a monotone cubic spline. Empty or fewer than
	// two points means a straight linear response (default, unchanged), so old
	// configs are unaffected. The dashboard's presets just populate these points.
	RPMCurvePoints []CurvePoint `json:"rpm_curve_points,omitempty"`
}

// CurvePoint is one control point of the MOZA rev-light response curve. X is the
// input RPM ratio (current/max) and Y the output bar fill, both in [0,1].
type CurvePoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Recording struct {
	Dir string `json:"dir"`
}

type Terminal struct {
	Enabled bool `json:"enabled"`
}

type Overlay struct {
	Enabled           bool    `json:"enabled"`
	Output            string  `json:"output,omitempty"`
	GameWindowMatch   string  `json:"game_window_match,omitempty"`
	Width             *int    `json:"width,omitempty"`
	Height            *int    `json:"height,omitempty"`
	Anchor            string  `json:"anchor"`
	MarginTop         *int    `json:"margin_top,omitempty"`
	MarginRight       *int    `json:"margin_right,omitempty"`
	MarginBottom      *int    `json:"margin_bottom,omitempty"`
	MarginLeft        *int    `json:"margin_left,omitempty"`
	UpdateHz          float64 `json:"update_hz"`
	Opacity           float64 `json:"opacity"`
	ShowSteering      bool    `json:"show_steering"`
	SteeringImagePath string  `json:"steering_image_path,omitempty"`
	SteeringSize      *int    `json:"steering_size,omitempty"`
	// SteeringX/SteeringY position the steering wheel within the overlay box
	// (top-left origin, logical pixels). When nil they fall back to the legacy
	// auto-placement (top-right corner) — see SteeringXValue/SteeringYValue.
	SteeringX *int `json:"steering_x,omitempty"`
	SteeringY *int `json:"steering_y,omitempty"`
	// SteeringRangeDeg is the lock-to-lock wheel rotation (degrees) used to map
	// the steering input to the displayed wheel angle. It is the fallback for
	// games that do not report a range (Forza); LMU reports a per-car range that
	// overrides it. 0 means "use the default".
	SteeringRangeDeg float64 `json:"steering_range_deg,omitempty"`
}

func Default() Config {
	return Config{
		ListenAddr: defaultListenAddr,
		ListenPort: defaultListenPort,
		PrintHz:    defaultPrintHz,
		Moza: Moza{
			Enabled:       false,
			Port:          "",
			UpdateHz:      20,
			RPMBrightness: 15,
			RPMColors: [10]Color{
				{0, 255, 0},
				{0, 255, 0},
				{0, 255, 0},
				{255, 255, 0},
				{255, 255, 0},
				{255, 128, 0},
				{255, 128, 0},
				{255, 0, 0},
				{255, 0, 0},
				{255, 0, 255},
			},
			ButtonColors: [10]Color{
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
				{255, 255, 255},
			},
			ButtonMask: 0x03ff,
		},
		Recording: Recording{
			Dir: defaultRecordDir,
		},
		Terminal: Terminal{
			Enabled: false,
		},
		Overlay: Overlay{
			Enabled:          false,
			GameWindowMatch:  defaultGameWindowMatch,
			Width:            intPtr(defaultOverlayWidth),
			Height:           intPtr(defaultOverlayHeight),
			Anchor:           defaultOverlayAnchor,
			MarginTop:        intPtr(0),
			MarginRight:      intPtr(0),
			MarginBottom:     intPtr(0),
			MarginLeft:       intPtr(0),
			UpdateHz:         defaultOverlayHz,
			Opacity:          defaultOpacity,
			ShowSteering:     true,
			SteeringSize:     intPtr(defaultSteeringSize),
			SteeringRangeDeg: defaultSteeringRangeDeg,
		},
		LMU: LMU{
			Enabled: true,
			BaseURL: defaultLMUBaseURL,
			PollHz:  defaultLMUPollHz,
		},
		Voice: Voice{
			Enabled:        false,
			ServerURL:      defaultVoiceServer,
			Language:       defaultVoiceLanguage,
			Trigger:        defaultVoiceTrigger,
			FIFOPath:       defaultVoiceFIFOPath,
			ConfirmSeconds: defaultVoiceConfirm,
			TTS: VoiceTTS{
				Enabled: false,
				Voice:   defaultTTSVoice,
				Speed:   1,
			},
			Engineer: Engineer{
				Enabled:        false,
				TimeoutSeconds: defaultEngineerTimeout,
				MaxTokens:      defaultEngineerMaxTokens,
				Temperature:    defaultEngineerTemperature,
				Callouts:       false,
			},
		},
	}
}

func LoadOptional(path string) (Config, string, error) {
	if path == "" {
		path = defaultPath
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			cfg := Default()
			return cfg, "", cfg.Validate()
		} else if err != nil {
			return Config{}, "", err
		}
	}

	cfg, err := Load(path)
	if err != nil {
		return Config{}, "", err
	}
	return cfg, path, nil
}

func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if path == "" {
		path = defaultPath
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func (c Config) Validate() error {
	if c.ListenAddr == "" {
		return fmt.Errorf("listen_addr must not be empty")
	}
	if c.ListenPort < 1 || c.ListenPort > 65535 {
		return fmt.Errorf("listen_port must be between 1 and 65535")
	}
	if c.ListenPort >= 5200 && c.ListenPort <= 5300 {
		return fmt.Errorf("listen_port must avoid Forza's reserved outgoing range 5200-5300")
	}
	if c.PrintHz <= 0 {
		return fmt.Errorf("print_hz must be greater than 0")
	}
	if c.Moza.Enabled {
		// Port may be empty: the runtime auto-detects an attached MOZA wheel over
		// USB, so enabling MOZA without naming a port is valid (it simply means
		// "use whatever is plugged in").
		if c.Moza.UpdateHz <= 0 {
			return fmt.Errorf("moza.update_hz must be greater than 0")
		}
		if c.Moza.RPMBrightness > 15 {
			return fmt.Errorf("moza.rpm_brightness must be between 0 and 15")
		}
		if c.Moza.ButtonMask > 0x03ff {
			return fmt.Errorf("moza.button_mask must fit the 10 button telemetry bits")
		}
		if c.Moza.RPMLEDs < 0 || c.Moza.RPMLEDs > 16 {
			return fmt.Errorf("moza.rpm_leds must be between 0 (auto) and 16")
		}
		var prevX float64
		for i, pt := range c.Moza.RPMCurvePoints {
			if pt.X < 0 || pt.X > 1 || pt.Y < 0 || pt.Y > 1 {
				return fmt.Errorf("moza.rpm_curve_points[%d] must be within [0,1]×[0,1]", i)
			}
			if i > 0 && pt.X <= prevX {
				return fmt.Errorf("moza.rpm_curve_points must have strictly increasing x")
			}
			prevX = pt.X
		}
	}
	if c.Recording.Dir == "" {
		return fmt.Errorf("recording.dir must not be empty")
	}
	if c.LMU.Enabled && c.LMU.PollHz <= 0 {
		return fmt.Errorf("lmu.poll_hz must be greater than 0")
	}
	if c.UIScale != 0 && (c.UIScale < minUIScale || c.UIScale > maxUIScale) {
		return fmt.Errorf("ui_scale must be between %.1f and %.1f", minUIScale, maxUIScale)
	}
	if err := c.Voice.Validate(); err != nil {
		return err
	}
	return nil
}

// Validate checks the voice config when enabled: the voice server URL is
// required, and the selected trigger needs its own field set.
func (v Voice) Validate() error {
	if !v.Enabled {
		return nil
	}
	if v.ServerURL == "" {
		return fmt.Errorf("voice.server_url is required when voice is enabled (see deploy/voice-server/)")
	}
	switch v.Trigger {
	case "", "fifo":
		if v.FIFOPath == "" {
			return fmt.Errorf("voice.fifo_path is required for the fifo trigger")
		}
	case "button":
		if v.ButtonDevice == "" {
			return fmt.Errorf("voice.button_device is required for the button trigger (use LearnVoiceButton)")
		}
	default:
		return fmt.Errorf("voice.trigger must be \"fifo\" or \"button\"")
	}
	if v.ConfirmSeconds < 0 {
		return fmt.Errorf("voice.confirm_seconds must be >= 0")
	}
	if v.TTS.Speed < 0 {
		return fmt.Errorf("voice.tts.speed must be >= 0")
	}
	return v.Engineer.Validate()
}

func (c Config) ValidateOverlayMode() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !c.Overlay.Enabled {
		return fmt.Errorf("overlay.enabled must be true to run the overlay")
	}
	return c.Overlay.Validate()
}

// WithDefaults returns a copy of the overlay config with any unset geometry
// fields filled in, so the overlay can be enabled at runtime even when the
// loaded config did not specify a full overlay block.
func (o Overlay) WithDefaults() Overlay {
	if o.Width == nil {
		o.Width = intPtr(defaultOverlayWidth)
	}
	if o.Height == nil {
		o.Height = intPtr(defaultOverlayHeight)
	}
	if o.Anchor == "" {
		o.Anchor = defaultOverlayAnchor
	}
	if o.MarginTop == nil {
		o.MarginTop = intPtr(0)
	}
	if o.MarginRight == nil {
		o.MarginRight = intPtr(0)
	}
	if o.MarginBottom == nil {
		o.MarginBottom = intPtr(0)
	}
	if o.MarginLeft == nil {
		o.MarginLeft = intPtr(0)
	}
	if o.UpdateHz <= 0 {
		o.UpdateHz = defaultOverlayHz
	}
	if o.Opacity <= 0 {
		o.Opacity = defaultOpacity
	}
	if o.SteeringSize == nil {
		o.SteeringSize = intPtr(defaultSteeringSize)
	}
	if o.SteeringRangeDeg <= 0 {
		o.SteeringRangeDeg = defaultSteeringRangeDeg
	}
	if o.GameWindowMatch == "" {
		o.GameWindowMatch = defaultGameWindowMatch
	}
	return o
}

// Validate checks the overlay-specific geometry and rendering fields. It does
// not require Overlay.Enabled, so it can be used to validate a config before
// turning the overlay on.
func (o Overlay) Validate() error {
	if o.Width == nil {
		return fmt.Errorf("overlay.width must be set")
	}
	if *o.Width <= 0 {
		return fmt.Errorf("overlay.width must be greater than 0")
	}
	if o.Height == nil {
		return fmt.Errorf("overlay.height must be set")
	}
	if *o.Height <= 0 {
		return fmt.Errorf("overlay.height must be greater than 0")
	}
	if !validOverlayAnchor(o.Anchor) {
		return fmt.Errorf("overlay.anchor must be one of top-left, top-right, bottom-left, bottom-right, top, bottom")
	}
	if o.MarginTop == nil || o.MarginRight == nil || o.MarginBottom == nil || o.MarginLeft == nil {
		return fmt.Errorf("overlay margins must all be set")
	}
	if *o.MarginTop < 0 || *o.MarginRight < 0 || *o.MarginBottom < 0 || *o.MarginLeft < 0 {
		return fmt.Errorf("overlay margins must be greater than or equal to 0")
	}
	if o.UpdateHz <= 0 {
		return fmt.Errorf("overlay.update_hz must be greater than 0")
	}
	if o.Opacity < 0 || o.Opacity > 1 {
		return fmt.Errorf("overlay.opacity must be between 0 and 1")
	}
	return nil
}

func validOverlayAnchor(anchor string) bool {
	switch anchor {
	case "top-left", "top-right", "bottom-left", "bottom-right", "top", "bottom":
		return true
	default:
		return false
	}
}

func (o Overlay) WidthValue() int {
	if o.Width == nil {
		return 0
	}
	return *o.Width
}

func (o Overlay) HeightValue() int {
	if o.Height == nil {
		return 0
	}
	return *o.Height
}

func (o Overlay) MarginTopValue() int {
	if o.MarginTop == nil {
		return 0
	}
	return *o.MarginTop
}

func (o Overlay) MarginRightValue() int {
	if o.MarginRight == nil {
		return 0
	}
	return *o.MarginRight
}

func (o Overlay) MarginBottomValue() int {
	if o.MarginBottom == nil {
		return 0
	}
	return *o.MarginBottom
}

func (o Overlay) MarginLeftValue() int {
	if o.MarginLeft == nil {
		return 0
	}
	return *o.MarginLeft
}

func (o Overlay) SteeringSizeValue() int {
	if o.SteeringSize == nil {
		return defaultSteeringSize
	}
	return *o.SteeringSize
}

// steeringGap is the legacy right-edge gap used when the steering wheel position
// is not explicitly configured (kept so existing configs render unchanged).
const steeringGap = 64

// SteeringXValue returns the steering wheel's x offset within the overlay box.
// When unset it reproduces the legacy auto-placement near the top-right corner.
func (o Overlay) SteeringXValue() int {
	if o.SteeringX == nil {
		return o.WidthValue() - o.SteeringSizeValue() - steeringGap
	}
	return *o.SteeringX
}

// SteeringYValue returns the steering wheel's y offset within the overlay box.
// When unset it reproduces the legacy top margin.
func (o Overlay) SteeringYValue() int {
	if o.SteeringY == nil {
		return 8
	}
	return *o.SteeringY
}

func intPtr(v int) *int {
	return &v
}
