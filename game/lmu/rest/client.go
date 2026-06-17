// Package rest is a typed client for Le Mans Ultimate's local HTTP/REST API
// (the game's own web-UI backend, served on 127.0.0.1:6397).
//
// # Why this exists
//
// LMU exposes far more than the rF2 shared-memory buffers the sidecar reads:
// pit-stop time estimates, per-rival fuel/tyre/virtual-energy projections, a
// multi-node weather *forecast*, fuel-tank capacity and richer standings. None
// of that is in the shared memory the wire.Frame carries. This client polls the
// useful read endpoints and folds them into one Snapshot the strategy engine can
// merge onto its frame-driven SessionState.
//
// # State gating
//
// The API only serves live data while the game is in an active session. Before
// that (menus / garage) the data endpoints return 503 with a small JSON body
// (`{"status":"unavailable","reason":...}`), and some race-only endpoints return
// 400. Fetch detects the unavailable state from GetGameState and returns a
// Snapshot with Available=false rather than hammering the rest. Every individual
// endpoint is best-effort: one failing leaves its field zero, never failing the
// whole snapshot.
//
// Unlike the shared-memory buffers (which live inside the game's Proton/Wine
// namespace and need the in-prefix lmu-bridge), this HTTP server is reachable
// from the Linux host directly, so the main app can poll it with no sidecar.
package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultBaseURL is where LMU serves its REST API.
const DefaultBaseURL = "http://localhost:6397"

// Client is a thin typed wrapper over the LMU REST API.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient builds a client for the given base URL (DefaultBaseURL if empty).
// timeout bounds each individual request; pass 0 for a sensible default.
func NewClient(baseURL string, timeout time.Duration) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: timeout}}
}

// Snapshot is one aggregated read of the LMU REST API. Available is false when
// the game is not in an active session (Reason explains why), in which case the
// data sections are nil. Each section is a pointer/slice/map so "absent" is
// distinguishable from "present but zero".
type Snapshot struct {
	FetchedAt time.Time `json:"fetched_at"`
	Available bool      `json:"available"`
	Reason    string    `json:"reason,omitempty"`

	GameState   *GameState              `json:"game_state,omitempty"`
	PitEstimate *PitEstimate            `json:"pit_estimate,omitempty"`
	Usage       map[string][]UsageEntry `json:"usage,omitempty"`
	Condition   *VehicleCondition       `json:"condition,omitempty"`
	Forecast    []ForecastNode          `json:"forecast,omitempty"`
	Standings   []Standing              `json:"standings,omitempty"`
	PitMenu     []PitMenuItem           `json:"pit_menu,omitempty"`
}

// GameState mirrors /rest/sessions/GetGameState in an active session. The same
// endpoint returns the unavailable shape (below) outside a session.
type GameState struct {
	MultiStintState  string  `json:"MultiStintState"`
	PitState         string  `json:"PitState"`
	PitEntryDist     float64 `json:"PitEntryDist"`
	GamePhase        string  `json:"gamePhase"`
	TimeOfDay        float64 `json:"timeOfDay"`
	InControl        bool    `json:"inControlOfVehicle"`
	InMonitor        bool    `json:"inMonitor"`
	ReplayActive     bool    `json:"isReplayActive"`
	PlayerVehLoaded  bool    `json:"playerVehicleLoaded"`
	RaceFinished     bool    `json:"raceFinished"`
	TeamVehicleState string  `json:"teamVehicleState"`
}

// gameStateUnavailable is the body returned (with HTTP 503) before a session is
// active, e.g. {"status":"unavailable","reason":"Game is not in an active
// session","gameState":"Setup"}.
type gameStateUnavailable struct {
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	GameState string `json:"gameState"`
}

// PitEstimate mirrors /rest/strategy/pitstop-estimate: the projected pit-stop
// duration broken down by activity, in seconds.
type PitEstimate struct {
	Fuel       float64 `json:"fuel"`
	Tires      float64 `json:"tires"`
	Brakes     float64 `json:"brakes"`
	BrakeDucts float64 `json:"brakeDucts"`
	Damage     float64 `json:"damage"`
	DriverSwap float64 `json:"driverSwap"`
	Penalties  float64 `json:"penalties"`
	VE         float64 `json:"ve"`
	Total      float64 `json:"total"`
}

// UsageEntry is one stint's projected resource usage for a driver, from
// /rest/strategy/usage (keyed by driver name → list of stints). Fuel/Tyres are
// only populated for the player's own car. VE is virtual energy (the hypercar
// hybrid budget), 0..1.
type UsageEntry struct {
	Lap   int        `json:"lap"`
	Stint int        `json:"stint"`
	Pit   bool       `json:"pit"`
	VE    float64    `json:"ve"`
	Fuel  float64    `json:"fuel"`
	Tyres [4]float64 `json:"tyres"`
}

// VehicleCondition mirrors /rest/garage/getVehicleCondition: the player car's
// consumables and damage. fuelCapacity is the tank size, which the shared-memory
// telemetry does not expose. Conditions are 0..1 (1 = fresh/undamaged).
type VehicleCondition struct {
	Fuel             float64    `json:"fuel"`
	FuelCapacity     float64    `json:"fuelCapacity"`
	TireCondition    [4]float64 `json:"tireCondition"`
	BrakeCondition   [4]float64 `json:"brakeCondition"`
	SuspensionDamage [4]float64 `json:"suspensionDamage"`
	VehicleDamage    float64    `json:"vehicleDamage"`
}

// ForecastNode is one weather-forecast point: a session phase (e.g. "PRACTICE")
// and a node along it ("FINISH", "NODE_25"…) with the predicted conditions. It
// is the flattened form of /rest/sessions/weather's nested map, so the frontend
// gets a simple list. Values keys are the WNV_* names (WNV_RAIN_CHANCE,
// WNV_TEMPERATURE, WNV_SKY, WNV_WINDSPEED, …).
type ForecastNode struct {
	Session string                   `json:"session"`
	Node    string                   `json:"node"`
	Values  map[string]ForecastValue `json:"values"`
}

// ForecastValue is one forecast field, with both the numeric value and the
// game's pre-formatted display string (e.g. {54, "54%"} or {1, "Light Clouds"}).
type ForecastValue struct {
	CurrentValue float64 `json:"currentValue"`
	StringValue  string  `json:"stringValue"`
}

// Standing is one car's row from /rest/watch/standings — a subset of the ~65
// fields, picking the strategy-relevant ones. FuelFraction/VEFraction are 0..1.
type Standing struct {
	SlotID           int32   `json:"slotID"`
	DriverName       string  `json:"driverName"`
	CarNumber        string  `json:"carNumber"`
	CarClass         string  `json:"carClass"`
	FullTeamName     string  `json:"fullTeamName"`
	VehicleName      string  `json:"vehicleName"`
	Position         int     `json:"position"`
	BestLapTime      float64 `json:"bestLapTime"`
	LastLapTime      float64 `json:"lastLapTime"`
	EstimatedLapTime float64 `json:"estimatedLapTime"`
	TimeBehindLeader float64 `json:"timeBehindLeader"`
	TimeBehindNext   float64 `json:"timeBehindNext"`
	LapsCompleted    int     `json:"lapsCompleted"`
	FuelFraction     float64 `json:"fuelFraction"`
	VEFraction       float64 `json:"veFraction"`
	Pitstops         int     `json:"pitstops"`
	Penalties        int     `json:"penalties"`
	PitState         string  `json:"pitState"`
	Pitting          bool    `json:"pitting"`
	InGarageStall    bool    `json:"inGarageStall"`
	Flag             string  `json:"flag"`
}

// PitMenuItem is one row of the in-game pit menu (/rest/garage/PitMenu/
// receivePitMenu): a setting name, its current/default index, and the list of
// selectable option labels (e.g. the VIRTUAL ENERGY rows "31% 6 laps").
type PitMenuItem struct {
	Name           string   `json:"name"`
	PMCValue       int      `json:"PMC Value"`
	CurrentSetting int      `json:"currentSetting"`
	Default        int      `json:"default"`
	Settings       []string `json:"-"`
}

// pitMenuItemRaw matches the wire shape (settings are objects with a "text"
// field); UnmarshalJSON flattens them to plain strings.
func (p *PitMenuItem) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name           string `json:"name"`
		PMCValue       int    `json:"PMC Value"`
		CurrentSetting int    `json:"currentSetting"`
		Default        int    `json:"default"`
		Settings       []struct {
			Text string `json:"text"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Name = raw.Name
	p.PMCValue = raw.PMCValue
	p.CurrentSetting = raw.CurrentSetting
	p.Default = raw.Default
	p.Settings = make([]string, len(raw.Settings))
	for i, s := range raw.Settings {
		p.Settings[i] = s.Text
	}
	return nil
}

// Fetch reads the useful endpoints into one Snapshot. It first checks the game
// state: if the game is not in an active session it returns early with
// Available=false. Otherwise it pulls every data section best-effort (a failing
// endpoint leaves its field nil and is not fatal). now is the timestamp stamped
// on the snapshot (injected so callers/tests stay deterministic).
func (c *Client) Fetch(ctx context.Context, now time.Time) Snapshot {
	snap := Snapshot{FetchedAt: now}

	gs, reason, ok := c.gameState(ctx)
	if !ok {
		snap.Reason = reason
		return snap
	}
	snap.Available = true
	snap.GameState = gs

	if v, err := c.pitEstimate(ctx); err == nil {
		snap.PitEstimate = v
	}
	if v, err := c.usage(ctx); err == nil {
		snap.Usage = v
	}
	if v, err := c.condition(ctx); err == nil {
		snap.Condition = v
	}
	if v, err := c.forecast(ctx); err == nil {
		snap.Forecast = v
	}
	if v, err := c.standings(ctx); err == nil {
		snap.Standings = v
	}
	if v, err := c.pitMenu(ctx); err == nil {
		snap.PitMenu = v
	}
	return snap
}

// gameState reads /rest/sessions/GetGameState. ok is false (with a human reason)
// when the game is not in an active session — either the 503 unavailable body or
// a transport error.
func (c *Client) gameState(ctx context.Context) (gs *GameState, reason string, ok bool) {
	body, status, err := c.get(ctx, "/rest/sessions/GetGameState")
	if err != nil {
		return nil, err.Error(), false
	}
	// The unavailable response carries a "status":"unavailable" body (HTTP 503).
	var un gameStateUnavailable
	if json.Unmarshal(body, &un) == nil && un.Status == "unavailable" {
		reason := un.Reason
		if reason == "" {
			reason = "game not in an active session"
		}
		return nil, reason, false
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Sprintf("game state http %d", status), false
	}
	var out GameState
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, "game state decode: " + err.Error(), false
	}
	return &out, "", true
}

func (c *Client) pitEstimate(ctx context.Context) (*PitEstimate, error) {
	var out PitEstimate
	if err := c.getJSON(ctx, "/rest/strategy/pitstop-estimate", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) usage(ctx context.Context) (map[string][]UsageEntry, error) {
	var out map[string][]UsageEntry
	if err := c.getJSON(ctx, "/rest/strategy/usage", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) condition(ctx context.Context) (*VehicleCondition, error) {
	var out VehicleCondition
	if err := c.getJSON(ctx, "/rest/garage/getVehicleCondition", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) standings(ctx context.Context) ([]Standing, error) {
	var out []Standing
	if err := c.getJSON(ctx, "/rest/watch/standings", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) pitMenu(ctx context.Context) ([]PitMenuItem, error) {
	var out []PitMenuItem
	if err := c.getJSON(ctx, "/rest/garage/PitMenu/receivePitMenu", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// forecast reads /rest/sessions/weather and flattens its nested
// session→node→field map into a list of ForecastNode.
func (c *Client) forecast(ctx context.Context) ([]ForecastNode, error) {
	var raw map[string]map[string]map[string]ForecastValue
	if err := c.getJSON(ctx, "/rest/sessions/weather", &raw); err != nil {
		return nil, err
	}
	var out []ForecastNode
	for session, nodes := range raw {
		for node, values := range nodes {
			out = append(out, ForecastNode{Session: session, Node: node, Values: values})
		}
	}
	return out, nil
}

// getJSON fetches path and decodes a JSON body into out. A non-2xx status, a
// transport error or a JSON "null" body (which several endpoints return when the
// data is not ready) is reported as an error so the caller leaves the field nil.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	body, status, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("%s: http %d", path, status)
	}
	if len(body) == 0 || string(body) == "null" {
		return fmt.Errorf("%s: empty", path)
	}
	return json.Unmarshal(body, out)
}

func (c *Client) get(ctx context.Context, path string) (body []byte, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}
