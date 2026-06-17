package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"telemetry-handler/analysis"
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

	go s.runReceiver(ctx, cfg)
	return nil
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
		if err := s.overlay.Start(ctx, cfg.Overlay, s.telemetrySource()); err != nil {
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
