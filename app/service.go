package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"telemetry-handler/analysis"
	"telemetry-handler/assistant"
	"telemetry-handler/config"
	"telemetry-handler/engineer"
	"telemetry-handler/game/forza"
	"telemetry-handler/game/lmu"
	"telemetry-handler/game/lmu/rest"
	"telemetry-handler/game/lmu/wire"
	"telemetry-handler/output"
	"telemetry-handler/overlay"
	"telemetry-handler/receiver"
	"telemetry-handler/recording"
	"telemetry-handler/store"
	"telemetry-handler/voice"
	"telemetry-handler/wheelbase/moza"
)

const (
	// overlayReconcileEvery is how often the supervisor checks whether the
	// overlay should be shown or hidden based on telemetry flow.
	overlayReconcileEvery = 500 * time.Millisecond
	// overlayShowWithin shows the overlay when a telemetry packet arrived within
	// this window (the game is running and sending). overlayHideAfter tears it
	// down once packets have stopped for this long. The gap between the two
	// provides hysteresis so a brief stall does not flap the native window.
	overlayShowWithin = 2 * time.Second
	overlayHideAfter  = 5 * time.Second

	// mozaReconnectEvery is how often the supervisor retries opening the MOZA
	// wheel while it is enabled but not connected (e.g. powered on after the app
	// started).
	mozaReconnectEvery = 3 * time.Second

	// voiceReconnectEvery is how often the voice supervisor retries starting the
	// push-to-talk engine while its trigger device is unavailable (e.g. a wheel
	// powered on after the app started, so its /dev/input node appears late).
	voiceReconnectEvery = 3 * time.Second
	// engineerPingTimeout bounds a check of the LLM endpoint. It is generous
	// because the first request to an evicted model includes loading the weights
	// — measured at ~44s for a 27B GGUF — and that wait is the whole reason to
	// make it here instead of on the driver's first question.
	engineerPingTimeout = 90 * time.Second
	// engineerWarmEvery re-pings so the server does not evict the model during a
	// quiet stint and hand the next question a cold load.
	engineerWarmEvery = 4 * time.Minute
	// calloutEvery is how often the unprompted-radio rules are evaluated. The
	// rules themselves decide whether anything is worth saying.
	calloutEvery = 5 * time.Second
	// calloutTTL is how long a callout stays on the overlay banner.
	calloutTTL = 6 * time.Second

	// referenceReconcileEvery is how often the strategy supervisor loads the
	// reference lap on a context change, persists a newly-beaten PB, and updates
	// the session-history row. Kept off the hot frame path on purpose.
	referenceReconcileEvery = 2 * time.Second

	// lmuRESTPresentWithin gates REST polling: the LMU REST API is only polled
	// while LMU telemetry has arrived this recently (i.e. the game is running and
	// it is LMU, not Forza). The same data also only serves in an active session.
	lmuRESTPresentWithin = 5 * time.Second
	// lmuRESTTimeout bounds each individual REST request so a hung endpoint cannot
	// stall the poller.
	lmuRESTTimeout = 2 * time.Second
)

// Service is the Wails-bound surface of the application. Its exported methods
// are exposed to the React frontend as generated TypeScript bindings, mirroring
// the JSON API the web server used to provide. It also owns the UDP receiver
// loop and the overlay lifecycle.
type Service struct {
	runtime *Runtime
	overlay *overlay.Manager
	ctx     context.Context

	// lmuClient talks to LMU's local REST API. It is created at startup when LMU
	// polling is enabled and shared by the poller and the on-demand setup bindings;
	// nil when LMU polling is disabled.
	lmuClient *rest.Client

	// speaker reads the voice assistant's messages aloud, when voice TTS is
	// enabled; nil otherwise. Built at startup and rebuilt by ApplyConfig, so it is
	// guarded by speakerMu (the notify path reads it from the engine goroutine).
	speaker   voice.Speaker
	speakerMu sync.Mutex

	// overlayDesired is the user's on/off intent (the UI toggle). The actual
	// native window is started/stopped by the supervisor only while the game is
	// also sending telemetry.
	overlayDesired atomic.Bool
	// overlayMu serializes reconcile so the periodic supervisor and an explicit
	// SetOverlayEnabled call cannot interleave start/stop decisions.
	overlayMu sync.Mutex
}

// OverlayStatus reports the overlay's desired (user toggle) and actual
// (native window) state. Enabled without Running means the overlay is on but
// waiting for the game to start sending telemetry.
type OverlayStatus struct {
	Enabled bool `json:"enabled"`
	Running bool `json:"running"`
}

// MonitorInfo reports the logical resolution of the monitor the overlay will
// appear on, so the dashboard can render an accurate placement preview. When
// Detected is false the resolution was not auto-detectable (e.g. not running
// under Hyprland) and the UI should let the user pick a resolution manually.
type MonitorInfo struct {
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Name     string `json:"name"`
	Detected bool   `json:"detected"`
}

func NewService(runtime *Runtime) *Service {
	return &Service{
		runtime: runtime,
		overlay: overlay.NewManager(),
	}
}

func (s *Service) ServiceName() string {
	return "telemetry-handler"
}

// ServiceStartup is invoked by Wails during application startup. It applies the
// initial MOZA configuration, starts the overlay if enabled, and launches the
// UDP receiver loop. The provided context is cancelled right before shutdown.
func (s *Service) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	s.ctx = ctx
	cfg := s.runtime.Config()

	if cfg.Moza.Enabled {
		// A missing/powered-off wheel is no longer fatal: ApplyMoza logs and the
		// supervisor (below) keeps retrying so the app starts and connects later.
		if err := s.runtime.ApplyMoza(cfg.Moza); err != nil {
			log.Printf("moza: %v", err)
		}
	}

	s.overlayDesired.Store(cfg.Overlay.Enabled)
	go s.superviseOverlay(ctx)
	go s.superviseMoza(ctx)
	if s.runtime.HasStore() {
		go s.superviseReference(ctx)
	}
	if cfg.LMU.Enabled {
		s.lmuClient = rest.NewClient(cfg.LMU.BaseURL, lmuRESTTimeout)
		go s.superviseREST(ctx, cfg.LMU)
	}

	if cfg.Voice.Enabled {
		s.startVoice(ctx, cfg)
	} else {
		log.Printf("voice: disabled (set voice.enabled=true in config.json and restart)")
	}

	go s.runReceiver(ctx, cfg)
	return nil
}

// startVoice builds and runs the push-to-talk voice assistant: it records on a
// PTT trigger, streams the audio to the GPU voice server for transcription,
// parses pit commands, stages them for confirmation on the overlay banner, and
// applies confirmed ones to LMU's pit menu over the REST API. A construction
// failure (bad trigger, bad server URL) is logged and non-fatal — the rest of
// the app runs unchanged.
func (s *Service) startVoice(ctx context.Context, cfg config.Config) {
	// The spoken-output speaker does not depend on the input device, so build it up
	// front — it is usable (and testable) even while we wait for the wheel.
	if cfg.Voice.TTS.Enabled {
		s.applyVoiceTTS(ctx, cfg.Voice)
	}
	go s.superviseVoice(ctx, cfg)
}

// superviseVoice starts the push-to-talk engine, retrying until its trigger
// device is available. A wheel powered on after the app starts (so its
// /dev/input node does not exist yet) is then picked up automatically, instead of
// leaving voice permanently disabled.
func (s *Service) superviseVoice(ctx context.Context, cfg config.Config) {
	// Voice needs the LMU REST client (pit menu read/write) even when periodic
	// REST polling is off, so create one if the poller did not.
	client := s.lmuClient
	if client == nil {
		client = rest.NewClient(cfg.LMU.BaseURL, lmuRESTTimeout)
	}

	// Surface every voice notice on the overlay banner and in the log, so the
	// feedback (confirmation prompt, result, errors) is visible even when the
	// overlay is off — essential for bring-up — and speak the important ones. The
	// speaker and speak-info are read live so an ApplyConfig TTS change takes effect
	// without restarting.
	notify := func(text string, level int, ttl time.Duration) {
		log.Printf("voice: %s", text)
		s.runtime.SetVoiceNotice(text, level, ttl)
		// An engineer's answer was already spoken sentence by sentence as it
		// streamed; saying the truncated banner version again would repeat it.
		if level == voice.LevelAnswer {
			return
		}
		sp := s.currentSpeaker()
		if sp != nil && (level != voice.LevelInfo || s.runtime.Config().Voice.TTS.SpeakInfo) {
			sp.Speak(spokenText(text))
		}
	}

	// speak says one sentence as-is — engineer answers are already natural prose
	// and must not go through the banner tidy-up.
	speak := func(text string) {
		if sp := s.currentSpeaker(); sp != nil {
			sp.Speak(text)
		}
	}

	// Pressing the trigger silences the assistant, so the driver can cut it off
	// mid-sentence rather than talk over it.
	bargeIn := func() {
		if sp := s.currentSpeaker(); sp != nil {
			sp.Stop()
		}
	}

	interpreter := s.buildEngineer(cfg.Voice.Engineer, client)
	// Callouts are rule-based and never consult the model, so they run whether or
	// not the LLM half is configured.
	go s.superviseCallouts(ctx, cfg.Voice.Engineer, notify, speak)

	attempt := func() (bool, error) {
		engine, err := voice.Build(ctx, voiceConfig(cfg.Voice), voice.Deps{
			Controller:  client,
			Notify:      notify,
			Interpreter: interpreter,
			Speak:       speak,
			OnPress:     bargeIn,
			Logf:        log.Printf,
		})
		if err != nil {
			return false, err
		}
		log.Printf("voice: push-to-talk ready (trigger=%s)", voiceTriggerName(cfg.Voice))
		go engine.Run(ctx)
		return true, nil
	}

	if ok, err := attempt(); ok {
		return
	} else {
		// Log why once (e.g. the wheel/device is not present yet, or a bad whisper
		// path), then retry quietly until it succeeds.
		log.Printf("voice: not ready (%v) — retrying every %s until the device is available", err, voiceReconnectEvery)
	}

	ticker := time.NewTicker(voiceReconnectEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ok, _ := attempt(); ok {
				return
			}
		}
	}
}

// buildEngineer constructs the LLM race engineer, or returns nil (the engine
// then uses the deterministic grammar alone). A misconfigured or unreachable
// model is logged and non-fatal: voice commands keep working without it.
func (s *Service) buildEngineer(cfg config.Engineer, garage assistant.Garage) voice.Interpreter {
	if !cfg.Enabled {
		return nil
	}
	llm := assistant.NewLLM(assistant.LLMConfig{
		BaseURL:     cfg.BaseURL,
		APIKey:      cfg.APIKeyValue(),
		Model:       cfg.Model,
		Timeout:     time.Duration(cfg.TimeoutSeconds * float64(time.Second)),
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
	})
	// Check it up front so a bad key or model id shows in the log at startup
	// rather than on the first corner, then keep it warm.
	go s.superviseEngineerWarmth(llm, cfg)
	in := assistant.NewInterpreter(llm, s.runtime.EngineerState, log.Printf)
	// The garage tools read and write the car setup over LMU's REST API. The
	// caller passes the client it resolved, which exists even when periodic REST
	// polling is switched off.
	if garage != nil {
		in.Tools().Add(assistant.GetSetupTool(garage))
		in.Tools().Add(assistant.SetSetupTool(garage, s.runtime.EngineerState))
	}
	return in
}

// superviseEngineerWarmth pings the model at startup and periodically after.
//
// The ping is the warm-up: a server that has evicted the weights spends tens of
// seconds loading them on the next request, and that delay landing on a question
// asked mid-corner is indistinguishable from the engineer being broken.
func (s *Service) superviseEngineerWarmth(llm *assistant.LLM, cfg config.Engineer) {
	ping := func(first bool) {
		ctx, cancel := context.WithTimeout(s.requestContext(), engineerPingTimeout)
		defer cancel()
		started := time.Now()
		if err := llm.Ping(ctx); err != nil {
			log.Printf("engineer: %v (voice commands still work; answers fall back to the grammar)", err)
			return
		}
		if first {
			log.Printf("engineer: ready (%s at %s, warmed in %s)",
				cfg.Model, cfg.BaseURL, time.Since(started).Round(time.Millisecond))
		}
	}
	ping(true)

	ticker := time.NewTicker(engineerWarmEvery)
	defer ticker.Stop()
	for {
		select {
		case <-s.requestContext().Done():
			return
		case <-ticker.C:
			ping(false)
		}
	}
}

// superviseCallouts runs the unprompted radio: it samples the session on a timer
// and speaks anything the rule set thinks the driver should know. Rate limiting
// lives in the Callouts type, not here.
func (s *Service) superviseCallouts(ctx context.Context, cfg config.Engineer, notify voice.Notifier, speak func(string)) {
	if !cfg.Callouts {
		log.Printf("voice: callouts off (set voice.engineer.callouts=true to hear lap summaries, flags and fuel warnings)")
		return
	}
	log.Printf("voice: callouts on — lap summaries, flags, fuel, tyres, rivals pitting")
	calls := assistant.NewCallouts()
	ticker := time.NewTicker(calloutEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			// notify logs it and puts it on the overlay banner; LevelAnswer means it
			// does not also speak, which speak below does.
			for _, msg := range calls.Observe(s.runtime.EngineerState(), now) {
				notify(msg, voice.LevelAnswer, calloutTTL)
				speak(msg)
			}
		}
	}
}

// applyVoiceTTS (re)builds the spoken-output speaker from the TTS config, or
// clears it when TTS is disabled. Safe to call at startup and from ApplyConfig.
// The speaker dials the voice server lazily, so this succeeds even when the
// server is still booting.
func (s *Service) applyVoiceTTS(ctx context.Context, cfg config.Voice) {
	if !cfg.TTS.Enabled {
		s.setSpeaker(nil)
		return
	}
	sp, err := voice.NewSpeaker(ctx, voiceTTSConfig(cfg), log.Printf)
	if err != nil {
		log.Printf("voice: tts disabled: %v", err)
		s.setSpeaker(nil)
		return
	}
	s.setSpeaker(sp)
}

func (s *Service) setSpeaker(sp voice.Speaker) {
	s.speakerMu.Lock()
	s.speaker = sp
	s.speakerMu.Unlock()
}

func (s *Service) currentSpeaker() voice.Speaker {
	s.speakerMu.Lock()
	defer s.speakerMu.Unlock()
	return s.speaker
}

// voiceConfig maps the persisted config.Voice onto the voice package's Config.
func voiceConfig(v config.Voice) voice.Config {
	ttl := time.Duration(v.ConfirmSeconds * float64(time.Second))
	return voice.Config{
		Remote:       voiceRemote(v),
		Language:     v.Language,
		CaptureCmd:   v.CaptureCmd,
		Trigger:      v.Trigger,
		FIFOPath:     v.FIFOPath,
		ButtonDevice: v.ButtonDevice,
		ButtonCode:   v.ButtonCode,
		ConfirmTTL:   ttl,
	}
}

func voiceTriggerName(v config.Voice) string {
	if v.Trigger == "button" {
		return "button"
	}
	return "fifo"
}

// voiceRemote maps the persisted server settings onto the voice client's.
func voiceRemote(v config.Voice) voice.RemoteConfig {
	return voice.RemoteConfig{BaseURL: v.ServerURL, Token: v.ServerToken}
}

// voiceTTSConfig maps the persisted voice config onto the voice package's
// TTSConfig.
func voiceTTSConfig(v config.Voice) voice.TTSConfig {
	return voice.TTSConfig{
		Remote:    voiceRemote(v),
		Voice:     v.TTS.Voice,
		Speed:     v.TTS.Speed,
		PlayerCmd: v.TTS.PlayerCmd,
	}
}

// spokenText tidies a banner notice for speech: lower-case (so "FUEL" isn't
// spelled out) and drop the "(no X)" miss annotation, which is noise aloud.
func spokenText(text string) string {
	if i := strings.Index(text, "(no "); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	return strings.ToLower(text)
}

// superviseReference bridges the strategy engine and the local store off the hot
// frame path: when the track/car context changes it loads that car's reference
// lap + corner names, it persists a newly-beaten PB, and it keeps the session
// history row up to date. Runs only when persistence is available.
func (s *Service) superviseReference(ctx context.Context) {
	ticker := time.NewTicker(referenceReconcileEvery)
	defer ticker.Stop()
	var lastTrack, lastCar string
	var sessionID int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			track, car, class := s.runtime.EngineerContext()
			if track == "" {
				continue
			}
			if track != lastTrack || car != lastCar {
				s.runtime.LoadReference(track, car, class)
				sessionID = s.runtime.StartSession(track, car)
				lastTrack, lastCar = track, car
			}
			if laps, best, ok := playerProgress(s.runtime.EngineerState()); ok {
				s.runtime.UpdateSession(sessionID, laps, best)
			}
			s.runtime.PersistDirty()
		}
	}
}

// playerProgress pulls the player car's lap count and best lap from a session
// snapshot, for the session-history row.
func playerProgress(state engineer.SessionState) (laps int, best float64, ok bool) {
	for i := range state.Cars {
		if state.Cars[i].ID == state.PlayerID || state.Cars[i].IsPlayer {
			c := state.Cars[i]
			b := c.BestLap
			if c.BestMeasured > 0 && (b == 0 || c.BestMeasured < b) {
				b = c.BestMeasured
			}
			return c.TotalLaps, b, true
		}
	}
	return 0, 0, false
}

// ServiceShutdown is invoked by Wails during application shutdown.
func (s *Service) ServiceShutdown() error {
	s.overlayDesired.Store(false)
	s.overlay.Stop()
	s.runtime.Close()
	return nil
}

// superviseOverlay periodically reconciles the native overlay against the
// user's intent and live telemetry: it is shown only while the overlay is
// enabled AND the game is sending telemetry, and torn down when either stops.
func (s *Service) superviseOverlay(ctx context.Context) {
	ticker := time.NewTicker(overlayReconcileEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileOverlay(ctx)
		}
	}
}

// superviseREST polls LMU's local REST API and folds the result (pit estimate,
// fuel capacity, weather forecast, per-driver projections) into the strategy
// engine. It only polls while LMU telemetry is live: when LMU is not the active
// source (Forza, or nothing running) it skips the request and clears any stale
// data, so the poller never hammers a closed port or mixes games. The API itself
// also only serves data in an active session — Fetch returns an unavailable
// snapshot otherwise, which clears the overlay.
func (s *Service) superviseREST(ctx context.Context, cfg config.LMU) {
	every := time.Second
	if cfg.PollHz > 0 {
		every = time.Duration(float64(time.Second) / cfg.PollHz)
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.lmuPresent() {
				if s.runtime.RESTData() != nil {
					s.runtime.SetRESTData(nil)
				}
				continue
			}
			snap := s.lmuClient.Fetch(ctx, time.Now())
			s.runtime.SetRESTData(&snap)
		}
	}
}

// lmuPresent reports whether LMU telemetry has arrived recently, i.e. the game is
// running and the active source is LMU (not Forza). Used to gate REST polling.
func (s *Service) lmuPresent() bool {
	snap := s.runtime.LatestTelemetry()
	if !snap.Available || snap.Source != "lmu" || snap.ReceivedAt.IsZero() {
		return false
	}
	return time.Since(snap.ReceivedAt) <= lmuRESTPresentWithin
}

// superviseMoza periodically retries connecting the MOZA wheel while it is
// enabled but not connected, so the app can start with the wheel off and pick it
// up once it is powered on.
func (s *Service) superviseMoza(ctx context.Context) {
	ticker := time.NewTicker(mozaReconnectEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runtime.reconnectMoza()
		}
	}
}

// reconcileOverlay starts or stops the overlay window to match the desired
// state and current telemetry presence. It is safe to call concurrently.
func (s *Service) reconcileOverlay(ctx context.Context) {
	s.overlayMu.Lock()
	defer s.overlayMu.Unlock()

	running := s.overlay.Running()

	if !s.overlayDesired.Load() {
		if running {
			s.overlay.Stop()
		}
		return
	}

	switch present := s.gamePresent(running); {
	case present && !running:
		cfg := s.runtime.Config()
		if err := s.overlay.Start(ctx, cfg.Overlay, s.telemetrySource(), s.voiceNoticeSource()); err != nil {
			log.Printf("overlay start: %v", err)
		}
	case !present && running:
		s.overlay.Stop()
	}
}

// gamePresent reports whether telemetry is flowing, with hysteresis: once the
// overlay is shown it stays up until packets have been absent for longer, so a
// momentary stall does not flap the native window.
func (s *Service) gamePresent(running bool) bool {
	snap := s.runtime.LatestTelemetry()
	if !snap.Available || snap.ReceivedAt.IsZero() {
		return false
	}
	age := time.Since(snap.ReceivedAt)
	if running {
		return age <= overlayHideAfter
	}
	return age <= overlayShowWithin
}

func (s *Service) telemetrySource() overlay.Source {
	return func() (forza.Telemetry, bool, time.Time, float64) {
		snap := s.runtime.LatestTelemetry()
		return snap.Telemetry, snap.Available, snap.ReceivedAt, snap.Meta.SteeringRangeDeg
	}
}

// voiceNoticeSource feeds the voice assistant's transient banner to the overlay.
// It returns "" when voice is disabled or no message is active, so the overlay
// simply draws nothing.
func (s *Service) voiceNoticeSource() overlay.NoticeSource {
	return func() (string, int) {
		return s.runtime.VoiceNotice()
	}
}

func (s *Service) runReceiver(ctx context.Context, cfg config.Config) {
	addr := fmt.Sprintf("%s:%d", cfg.ListenAddr, cfg.ListenPort)
	formatter := output.NewFormatter()
	var lastPrint time.Time
	var badSizes uint64

	// apply pushes one telemetry frame to every consumer: the shared snapshot
	// (overlay + dashboard), MOZA RPM lighting, and optional terminal output.
	// source identifies the game ("forza"/"lmu") so the dashboard can tailor
	// which tabs and readouts it shows.
	apply := func(t forza.Telemetry, source string, meta TelemetryMeta) error {
		s.runtime.SetTelemetry(t, source, meta)
		if s.runtime.MozaEnabled() {
			currentRPM := t.CurrentEngineRpm
			if t.IsRaceOn == 0 {
				currentRPM = 0
			}
			// MOZA RPM lighting is a best-effort side effect. A transient USB
			// serial hiccup (EIO on /dev/ttyACM*) must not tear down the whole
			// telemetry receiver — log it and keep feeding dashboard/overlay.
			if err := s.runtime.UpdateMozaRPM(currentRPM, t.EngineMaxRpm); err != nil {
				log.Printf("moza: rpm update failed: %v", err)
			}
		}
		if s.runtime.TerminalPrintEnabled() {
			now := time.Now()
			if lastPrint.IsZero() || now.Sub(lastPrint) >= s.runtime.PrintEvery() {
				lastPrint = now
				fmt.Println(formatter.Format(t))
			}
		}
		return nil
	}

	// reassembler stitches the lmu-bridge's chunked binary frames back together.
	// receiver.Listen calls the handler from a single goroutine, so it needs no
	// locking.
	var reassembler wire.Reassembler

	log.Printf("listening for telemetry on %s", addr)
	err := receiver.Listen(ctx, addr, func(_ context.Context, packet []byte) error {
		// One UDP port carries three things, demultiplexed by content:
		//   1. the lmu-bridge's chunked binary frames (start with the wire magic),
		//   2. the legacy lmu-bridge JSON (starts '{'), kept for old recordings,
		//   3. Forza's fixed-size binary packets.
		if wire.IsEnvelope(packet) {
			// Record every chunk so replay reassembles exactly like the live path.
			if err := s.runtime.RecordPacket(packet, time.Now()); err != nil {
				return err
			}
			payload, complete, err := reassembler.Add(packet)
			if err != nil {
				log.Printf("ignored lmu chunk: %v", err)
				return nil
			}
			if !complete {
				return nil
			}
			frame, err := wire.UnmarshalFrame(payload)
			if err != nil {
				log.Printf("ignored malformed lmu frame: %v", err)
				return nil
			}
			s.runtime.SetFrame(&frame)
			s.runtime.ObserveFrame(&frame)
			return apply(frameToTelemetry(&frame), "lmu", frameToMeta(&frame))
		}

		if lmu.LooksLikePacket(packet) {
			p, err := lmu.Parse(packet)
			if err != nil {
				log.Printf("ignored malformed lmu packet: %v", err)
				return nil
			}
			if err := s.runtime.RecordPacket(packet, time.Now()); err != nil {
				return err
			}
			s.runtime.SetFrame(nil)
			s.runtime.ObserveFrame(nil)
			return apply(lmuToTelemetry(p), "lmu", lmuToMeta(p))
		}

		telemetry, err := forza.ParseFH6Packet(packet)
		if err != nil {
			var sizeErr *forza.PacketSizeError
			if errors.As(err, &sizeErr) {
				badSizes++
				log.Printf("ignored packet with unexpected size got=%d want=%d total_bad=%d", sizeErr.Got, sizeErr.Want, badSizes)
				return nil
			}
			return err
		}

		if err := s.runtime.RecordPacket(packet, time.Now()); err != nil {
			return err
		}
		s.runtime.SetFrame(nil)
		s.runtime.ObserveFrame(nil)
		return apply(telemetry, "forza", TelemetryMeta{})
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("receiver: %v", err)
	}
}

// Bound methods exposed to the frontend.

func (s *Service) GetConfig() config.Config {
	return s.runtime.Config()
}

// ConfigStatus reports whether the config file failed to load at startup. When
// Error is non-empty the app is running on default settings and the dashboard
// shows a warning banner.
type ConfigStatus struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

func (s *Service) GetConfigStatus() ConfigStatus {
	path, msg := s.runtime.LoadError()
	return ConfigStatus{Path: path, Error: msg}
}

func (s *Service) GetTelemetry() TelemetrySnapshot {
	return s.runtime.LatestTelemetry()
}

// GetLatestFrame returns the most recent full LMU telemetry frame — every car's
// complete telemetry (engine, wheels/tires, suspension, forces, aero, damage,
// electric boost) plus session globals (weather, rules, driving aids). It is
// nil when the active source is Forza or no LMU frame has arrived yet.
func (s *Service) GetLatestFrame() *wire.Frame {
	return s.runtime.LatestFrame()
}

// GetEngineerState returns the Strategy Planner's game-agnostic session model:
// every car's position, gaps, fuel, tires and lap times, plus the global flag and
// weather state. Available is false until the first LMU frame arrives (and resets
// when the source switches to Forza). This is the single method the strategy
// frontend polls.
func (s *Service) GetEngineerState() engineer.SessionState {
	return s.runtime.EngineerState()
}

// GetStrategyData returns the latest raw poll of LMU's REST API: pit-stop time
// estimate, per-driver fuel/energy projections, vehicle condition, weather
// forecast, full standings and the in-game pit menu. It is nil until the API has
// been polled in an active LMU session (the higher-value bits are also merged
// into GetEngineerState().Strategy; this exposes the full detail). The frontend
// polls it for the Strategy Planner's richer views.
func (s *Service) GetStrategyData() *rest.Snapshot {
	return s.runtime.RESTData()
}

// GetCarSetup fetches the player car's active setup from LMU's REST API on demand
// (the full garage setup sheet: aero, suspension, gearing, tyres, brakes, engine
// maps), data the shared-memory telemetry does not expose. It is fetched live
// rather than polled because the setup only changes in the garage, not during a
// stint. Errors when LMU polling is disabled or the API is unreachable / not in a
// state that serves the garage; the frontend surfaces that as "setup unavailable".
func (s *Service) GetCarSetup() (*rest.CarSetup, error) {
	if s.lmuClient == nil {
		return nil, fmt.Errorf("LMU REST polling is disabled")
	}
	ctx, cancel := context.WithTimeout(s.requestContext(), lmuRESTTimeout)
	defer cancel()
	return s.lmuClient.Setup(ctx)
}

// GetSetupList returns the saved setups available for the current car (for a
// future setup picker). Errors as GetCarSetup does.
func (s *Service) GetSetupList() ([]rest.SetupFile, error) {
	if s.lmuClient == nil {
		return nil, fmt.Errorf("LMU REST polling is disabled")
	}
	ctx, cancel := context.WithTimeout(s.requestContext(), lmuRESTTimeout)
	defer cancel()
	return s.lmuClient.SetupList(ctx)
}

// SetSetupValue changes one setup setting (by its VM_*/WM_* key) to a step index,
// writing it to LMU via the REST API. Only effective in the garage. The frontend
// calls it from the editable Setup sheet; it should re-fetch GetCarSetup after to
// pick up the resulting string value (the game may clamp).
func (s *Service) SetSetupValue(key string, value int) error {
	if s.lmuClient == nil {
		return fmt.Errorf("LMU REST polling is disabled")
	}
	ctx, cancel := context.WithTimeout(s.requestContext(), lmuRESTTimeout)
	defer cancel()
	return s.lmuClient.SetSetupValue(ctx, key, value)
}

// GetPitMenu returns the current in-game pit menu (each component with its
// selectable option labels and selected index), for the editable pit-menu panel.
func (s *Service) GetPitMenu() ([]rest.PitMenuItem, error) {
	if s.lmuClient == nil {
		return nil, fmt.Errorf("LMU REST polling is disabled")
	}
	ctx, cancel := context.WithTimeout(s.requestContext(), lmuRESTTimeout)
	defer cancel()
	return s.lmuClient.PitMenu(ctx)
}

// SetPitMenuValue selects option index currentSetting on the pit-menu component
// identified by pmc (its "PMC Value" from GetPitMenu). The client round-trips the
// whole menu as a JSON array (a bare object crashes the game).
func (s *Service) SetPitMenuValue(pmc int, currentSetting int) error {
	if s.lmuClient == nil {
		return fmt.Errorf("LMU REST polling is disabled")
	}
	ctx, cancel := context.WithTimeout(s.requestContext(), lmuRESTTimeout)
	defer cancel()
	return s.lmuClient.SetPitMenuValue(ctx, pmc, currentSetting)
}

// LearnVoiceButton listens across the system's input devices and returns the
// first button/key pressed, so the dashboard can capture a wheel-rim button for
// the voice push-to-talk trigger without the user knowing its evdev code. It
// blocks up to timeoutSeconds (default 10). Linux only.
func (s *Service) LearnVoiceButton(timeoutSeconds int) (voice.Button, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 10
	}
	ctx, cancel := context.WithTimeout(s.requestContext(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	return voice.LearnButton(ctx)
}

// VoiceTestResult reports how long the test phrase took to become audible.
// FirstAudioMS is the latency figure; TotalMS also includes speaking the phrase.
type VoiceTestResult struct {
	FirstAudioMS int64 `json:"first_audio_ms"`
	TotalMS      int64 `json:"total_ms"`
}

// TestVoiceTTS synthesizes and plays a sample phrase synchronously using the
// given voice settings, so the dashboard can verify the voice server without
// enabling voice or restarting. Returns an error (surfaced in the UI) if the
// server is unreachable or playback fails.
func (s *Service) TestVoiceTTS(v config.Voice) (VoiceTestResult, error) {
	const sample = "Confirm, all tyres new wet, say yes."
	ctx, cancel := context.WithTimeout(s.requestContext(), 30*time.Second)
	defer cancel()
	t, err := voice.SpeakOnce(ctx, voiceTTSConfig(v), sample)
	return VoiceTestResult{
		FirstAudioMS: t.FirstAudio.Milliseconds(),
		TotalMS:      t.Total.Milliseconds(),
	}, err
}

// requestContext returns the service context for a bound call, falling back to
// Background before startup has stored one.
func (s *Service) requestContext() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

// SetComparisonCar tells the strategy engine which rival to buffer a driven line
// for (the Driver Vs. "line" overlay). The frontend calls it when the user picks
// a comparison car; -1 clears the selection.
func (s *Service) SetComparisonCar(id int32) {
	s.runtime.SetCompareCar(id)
}

func (s *Service) ApplyConfig(cfg config.Config) (config.Config, error) {
	if err := s.runtime.ApplyConfig(cfg); err != nil {
		return config.Config{}, err
	}
	// The overlay reads its geometry/appearance config only when started, so a
	// running overlay must be restarted to pick up placement/size/opacity edits.
	if s.ctx != nil && s.overlay.Running() {
		s.overlay.Stop()
		s.reconcileOverlay(s.ctx)
	}
	// Rebuild the spoken-output speaker so TTS edits (server, voice, speed) take
	// effect without a restart — the rest of the voice engine (trigger, recorder)
	// still needs one. Only when voice itself is running (ctx set).
	if s.ctx != nil {
		s.applyVoiceTTS(s.ctx, cfg.Voice)
	}
	return s.runtime.Config(), nil
}

func (s *Service) SaveConfig(cfg config.Config) error {
	return s.runtime.SaveConfig(cfg)
}

func (s *Service) PreviewMoza(moza config.Moza) error {
	return s.runtime.PreviewMoza(moza)
}

// GetMozaStatus reports whether the MOZA wheel is connected and, when USB
// detection identifies it, its model/serial/rev-light count. Polled by the
// dashboard's MOZA section to show live wheel info.
func (s *Service) GetMozaStatus() MozaStatus {
	return s.runtime.MozaStatus()
}

// TestMozaLights runs a short rev-light sweep on the connected wheel so the user
// can confirm the LEDs work from the dashboard's MOZA section (the same effect
// as the -moza-test CLI). Errors if no wheel is connected.
func (s *Service) TestMozaLights() error {
	return s.runtime.TestMozaLights()
}

// DetectMoza lists the MOZA wheels currently attached over USB so the dashboard
// can show what is connected and let the user pick the serial port. Empty when
// none are attached (or on platforms without detection).
func (s *Service) DetectMoza() []moza.Device {
	return s.runtime.DetectMoza()
}

func (s *Service) GetRecordingStatus() recording.Status {
	return s.runtime.RecordingStatus()
}

func (s *Service) StartRecording() (recording.Status, error) {
	return s.runtime.StartRecording()
}

func (s *Service) StopRecording() (recording.Status, error) {
	status, err := s.runtime.StopRecording()
	if err == nil {
		// Index the file just written, tagged with the current session's game/car/
		// track, so the recordings list carries searchable metadata.
		snap := s.runtime.LatestTelemetry()
		s.runtime.IndexLatestRecording(snap.Meta.Track, snap.Meta.Car, snap.Source)
	}
	return status, err
}

func (s *Service) ListRecordings() ([]recording.Info, error) {
	return s.runtime.ListRecordings()
}

// ListSessions returns the persisted session/stint history (newest first), or an
// empty list when persistence is unavailable. Bound for the Strategy History view.
func (s *Service) ListSessions() []store.SessionRow {
	return s.runtime.ListSessions(50)
}

// ListIndexedRecordings returns the recordings index with track/car/source
// metadata (newest first), or an empty list when persistence is unavailable.
func (s *Service) ListIndexedRecordings() []store.RecordingRow {
	return s.runtime.ListIndexedRecordings()
}

func (s *Service) ReplayRecording(name string, maxSamples int) ([]ReplaySample, error) {
	return s.runtime.ReplayRecording(name, maxSamples)
}

// AnalyzeRecording replays a saved recording and returns coaching analysis: a
// per-lap scorecard plus a list of detected events. Pass maxSamples 0 to analyze
// the whole recording.
func (s *Service) AnalyzeRecording(name string, maxSamples int) (analysis.Report, error) {
	return s.runtime.AnalyzeRecording(name, maxSamples)
}

// SetOverlayEnabled toggles the user's intent to show the native telemetry
// overlay. The window itself only appears while the game is sending telemetry;
// enabling it before the game starts simply arms it to show automatically.
func (s *Service) SetOverlayEnabled(enabled bool) error {
	s.overlayDesired.Store(enabled)
	if s.ctx == nil {
		if enabled {
			return fmt.Errorf("overlay is not ready yet")
		}
		return nil
	}
	s.reconcileOverlay(s.ctx)
	return nil
}

func (s *Service) GetOverlayStatus() OverlayStatus {
	return OverlayStatus{Enabled: s.overlayDesired.Load(), Running: s.overlay.Running()}
}

// GetMonitorInfo reports the logical resolution of the monitor the overlay will
// appear on, used by the dashboard placement preview.
func (s *Service) GetMonitorInfo() MonitorInfo {
	w, h, name, ok := overlay.Monitor(s.runtime.Config().Overlay)
	return MonitorInfo{Width: w, Height: h, Name: name, Detected: ok}
}

// ListMonitors returns the names of all connected monitors for the overlay
// output dropdown (empty when enumeration is unavailable, e.g. non-Hyprland).
func (s *Service) ListMonitors() []string {
	return overlay.Monitors()
}
