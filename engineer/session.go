// Package engineer turns the raw, game-specific telemetry frames into a single,
// game-agnostic "session model" that the Strategy Planner frontend consumes.
//
// # Why this layer exists
//
// The strategy interface is a pit-wall view: it cares about EVERY car (positions,
// gaps, fuel, tires, lap times), not just the player's. The rest of the app maps
// telemetry into forza.Telemetry, which is a single-car shape — the wrong fit for
// strategy. So engineer defines its own multi-car model (SessionState) and maps
// the LMU wire.Frame into it.
//
// Keeping this mapping in Go (not the frontend) has two payoffs:
//   - The frontend stays a thin renderer: it polls SessionState and draws it.
//   - Adding another game later means writing ONE new map<Game>Frame function that
//     fills the same SessionState — the whole frontend is reused unchanged.
//
// Most fields here are "instantaneous" — mapped straight from the current frame.
// The stateful, per-corner accumulation (tire wear per mini-sector, fuel per
// straight, the driven line) lives in lapaccum.go / minisectors.go, and the race
// timeline in events.go; the Engineer (engineer.go) folds those onto the snapshot.
package engineer

import (
	"math"

	"telemetry-handler/game/lmu/wire"
)

// SessionState is the complete, game-agnostic snapshot the strategy UI renders.
// One of these is produced per observed frame. Available is false before any
// frame has arrived (or for Forza, which produces no multi-car frame).
type SessionState struct {
	Available      bool         `json:"available"`
	Game           string       `json:"game"`             // "lmu" (future: other games)
	Track          string       `json:"track"`            // track name
	TrackLength    float64      `json:"track_length"`     // meters (one lap)
	SessionType    int32        `json:"session_type"`     // rF2 session enum (10-13 = race)
	SessionTime    float64      `json:"session_time"`     // elapsed session time (s)
	SessionEndTime float64      `json:"session_end_time"` // scheduled end (s, 0 if lap-limited)
	MaxLaps        int32        `json:"max_laps"`         // race lap count (0 if time-limited)
	PlayerID       int32        `json:"player_id"`        // slot ID of the player's car (-1 unknown)
	Flags          FlagState    `json:"flags"`
	Weather        WeatherState `json:"weather"`
	Cars           []CarState   `json:"cars"`
	Player         PlayerDetail `json:"player"`  // car-management detail for the player car only
	Events         []RaceEvent  `json:"events"`  // most-recent-last race timeline (bounded)
	Corners        []string     `json:"corners"` // per-mini-sector corner label ("T1"/""=straight); derived from the reference lap
}

// PlayerDetail is the instantaneous "Car Management" view of the player's car:
// powertrain, energy, aero, suspension, driver aids and damage. It is player-only
// (the data is heavy and only the engineered car needs it) and needs no history,
// so it is mapped straight from the current frame. Present is false until a
// player car is identified.
type PlayerDetail struct {
	Present bool `json:"present"`

	// powertrain / temps
	Rpm       float64 `json:"rpm"`
	MaxRpm    float64 `json:"max_rpm"`
	WaterTemp float64 `json:"water_temp"` // Celsius
	OilTemp   float64 `json:"oil_temp"`   // Celsius

	// hybrid / electric
	ElectricState int     `json:"electric_state"` // 0=unavail,1=inactive,2=propulsion,3=regen
	ElectricTemp  float64 `json:"electric_temp"`  // motor temp, Celsius

	// aero / suspension
	FrontDownforce  float64 `json:"front_downforce"` // Newtons
	RearDownforce   float64 `json:"rear_downforce"`  // Newtons
	Drag            float64 `json:"drag"`            // Newtons
	FrontRideHeight float64 `json:"front_ride_height"`
	RearRideHeight  float64 `json:"rear_ride_height"`
	RearBrakeBias   float64 `json:"rear_brake_bias"` // 0..1 toward the rear

	// driver aids (session-wide settings)
	TractionControl  int `json:"traction_control"`  // 0=off..3
	ABS              int `json:"abs"`               // 0=off..2
	StabilityControl int `json:"stability_control"` // 0=off..2

	PitSpeedLimit float64 `json:"pit_speed_limit"` // m/s

	// damage: per-panel dent severity (0=none,1=minor,2=major)
	DentSeverity [8]int `json:"dent_severity"`
	WorstDent    int    `json:"worst_dent"` // max of DentSeverity, for a quick at-a-glance level

	// Balance is the heuristic understeer/oversteer assessment + advisory proposal.
	Balance BalanceState `json:"balance"`
}

// BalanceState is the heuristic chassis-balance read for the player car: whether
// it tends to understeer or oversteer through corners, and an advisory proposal.
// It is a HEURISTIC from grip/slip telemetry, not a setup readout (LMU doesn't
// expose the setup), so it suggests a direction, never exact values.
type BalanceState struct {
	Samples  int     `json:"samples"`  // cornering frames observed (0 = not enough data)
	Bias     float64 `json:"bias"`     // -1 (oversteer) .. +1 (understeer)
	Verdict  string  `json:"verdict"`  // "Understeer" | "Neutral" | "Oversteer" | ""
	Proposal string  `json:"proposal"` // advisory text
}

// CarState is one car's strategy-relevant state. Every field is filled from the
// current frame; nothing here requires history. Fuel/battery are read per car —
// the sidecar reads the telemetry buffer for the whole grid, so rivals' values
// are populated too (0 when the game doesn't expose them).
type CarState struct {
	ID               int32        `json:"id"`                 // stable slot ID
	Number           string       `json:"number"`             // car number — not exposed by LMU yet; UI falls back to Place
	Driver           string       `json:"driver"`             // driver name
	CarName          string       `json:"car_name"`           // car model
	Class            string       `json:"class"`              // class name — drives the dot colour
	Place            int          `json:"place"`              // 1-based overall position
	LapDistFrac      float64      `json:"lap_dist_frac"`      // 0..1 position around the lap — drives the circle
	TotalLaps        int          `json:"total_laps"`         // laps completed
	InPits           bool         `json:"in_pits"`            // currently in the pit lane
	PitState         int          `json:"pit_state"`          // 0=none,1=request,2=entering,3=stopped,4=exiting
	NumPitstops      int          `json:"num_pitstops"`       // stops completed
	GapToLeader      float64      `json:"gap_to_leader"`      // seconds behind the leader
	GapToNext        float64      `json:"gap_to_next"`        // seconds behind the car ahead
	LapsBehindLeader int          `json:"laps_behind_leader"` // laps down to the leader
	BestLap          float64      `json:"best_lap"`           // session best lap (s, 0 if none)
	LastLap          float64      `json:"last_lap"`           // previous lap time (s, 0 if none)
	CurSector        int          `json:"cur_sector"`         // current mini-sector index (rF2: 0=S3,1=S1,2=S2)
	Fuel             float64      `json:"fuel"`               // liters remaining
	FuelCapacity     float64      `json:"fuel_capacity"`      // tank size (liters)
	Battery          float64      `json:"battery"`            // hybrid charge 0..1
	Tires            [4]TireState `json:"tires"`              // FL, FR, RL, RR
	IsPlayer         bool         `json:"is_player"`          // this is the car we're engineering for

	// MiniSectors / LapInProgress are the per-corner accumulation from the live
	// engine (engineer/lapaccum.go). MiniSectors (the last COMPLETED lap) is
	// filled for EVERY car so Driver Vs. can compare any rival; LapInProgress (the
	// lap currently being driven) is the player car only. Both are nil until the
	// relevant lap exists.
	MiniSectors   []MiniSectorState `json:"mini_sectors"`
	LapInProgress []MiniSectorState `json:"lap_in_progress"`

	// LapPath is the driven line (world X/Z) of the last completed lap, for the
	// Drive Line view. Captured for the player and the selected compare car only
	// (it's heavy); nil otherwise.
	LapPath []Vec2 `json:"lap_path"`

	// Best* hold the fastest FULL lap the engine has seen for this car — the
	// reference the Coaching and Driver Vs. views compare against. BestSectors is
	// filled for every car; BestPath only for the player + selected compare car.
	BestSectors  []MiniSectorState `json:"best_sectors"`
	BestPath     []Vec2            `json:"best_path"`
	BestMeasured float64           `json:"best_measured"` // engine-measured best lap time (s)
}

// Vec2 is a world-plane point (X east/west, Z north/south) used for the driven
// line. Y (height) is dropped — the line is drawn top-down.
type Vec2 struct {
	X float64 `json:"x"`
	Z float64 `json:"z"`
}

// RaceEvent is one entry in the race timeline / popup feed: a flag change, a
// competitor pit entry, or a notable contact. Generated by the engine from frame-
// to-frame state transitions (engineer/events.go).
type RaceEvent struct {
	AtET    float64 `json:"at_et"`   // session time the event happened (s)
	Kind    string  `json:"kind"`    // "flag" | "pit" | "contact"
	CarID   int32   `json:"car_id"`  // car involved (-1 for session-wide)
	Message string  `json:"message"` // human-readable, ready to display
}

// MiniSectorState is the resource usage accumulated across one mini-sector (one
// of numMiniSectors equal slices of the lap). It is what lets the engineer say
// "you used too much tire braking into this corner". All deltas are over the
// mini-sector only.
type MiniSectorState struct {
	Index       int        `json:"index"`        // 0..numMiniSectors-1
	TireWear    [4]float64 `json:"tire_wear"`    // wear consumed per wheel (entry-exit; see lapaccum.go)
	FuelUsed    float64    `json:"fuel_used"`    // liters burned
	BatteryUsed float64    `json:"battery_used"` // hybrid charge delta (+ depleted, - regen)
	TimeSpent   float64    `json:"time_spent"`   // seconds spent in the mini-sector
	EntrySpeed  float64    `json:"entry_speed"`  // m/s at entry
	ExitSpeed   float64    `json:"exit_speed"`   // m/s at exit
	MinSpeed    float64    `json:"min_speed"`    // slowest m/s within the mini-sector
}

// TireState is one corner's tire condition. Tire and brake temps are converted
// from LMU's Kelvin to Celsius in mapCar.
type TireState struct {
	Temp      [3]float64 `json:"temp"`       // left/center/right, Celsius
	Pressure  float64    `json:"pressure"`   // kPa
	Wear      float64    `json:"wear"`       // 0..1 (1 = fresh in rF2? treated as a fraction)
	BrakeTemp float64    `json:"brake_temp"` // Celsius
	Compound  string     `json:"compound"`   // e.g. "Soft"
}

// FlagState is the global race-control state powering the popup banner.
type FlagState struct {
	Green       bool   `json:"green"`
	Yellow      bool   `json:"yellow"`
	SafetyCar   bool   `json:"safety_car"`   // a safety car exists this session
	SCActive    bool   `json:"sc_active"`    // safety car is currently deployed
	SectorFlags [3]int `json:"sector_flags"` // per-sector yellow state
}

// WeatherState is the session weather for the Strategy Calls forecast.
type WeatherState struct {
	AmbientTemp float64    `json:"ambient_temp"` // Celsius
	TrackTemp   float64    `json:"track_temp"`   // Celsius
	Raining     float64    `json:"raining"`      // 0..1 overall
	RainGrid    [9]float64 `json:"rain_grid"`    // 3x3 rain intensity across the track
	WindMax     float64    `json:"wind_max"`     // m/s
	Cloudiness  float64    `json:"cloudiness"`   // 0..1
}

// kToC converts a Kelvin reading (rF2 tire temps) to Celsius, mapping an
// unavailable 0 reading to 0 rather than -273.15. Mirrors app.kelvinToCelsius but
// stays in float64 to keep the strategy model precise.
func kToC(k float64) float64 {
	if k <= 0 {
		return 0
	}
	return k - 273.15
}

// mapLMUFrame projects a decoded LMU wire.Frame onto the game-agnostic
// SessionState. This is the single point where LMU-specific layout meets the
// strategy model; a second game would add its own map<Game>Frame filling the same
// struct. Phase 1: instantaneous fields only.
func mapLMUFrame(f *wire.Frame) SessionState {
	si := f.ScoringInfo
	s := SessionState{
		Available:      true,
		Game:           "lmu",
		Track:          wire.GoString(si.TrackName[:]),
		TrackLength:    si.LapDist,
		SessionType:    si.Session,
		SessionTime:    si.CurrentET,
		SessionEndTime: si.EndET,
		MaxLaps:        saneMaxLaps(si.MaxLaps),
		PlayerID:       f.PlayerID,
		Flags:          mapFlags(f),
		Weather:        mapWeather(f),
		Cars:           make([]CarState, 0, len(f.Vehicles)),
	}
	for i := range f.Vehicles {
		s.Cars = append(s.Cars, mapCar(&f.Vehicles[i], si.LapDist))
	}
	s.Player = mapPlayerDetail(f)
	return s
}

// saneMaxLaps normalises rF2's lap limit: a timed session reports a huge sentinel
// (e.g. 2147483647) rather than 0 for "no lap limit", which otherwise makes
// laps-remaining / fuel-to-add explode. Treat anything non-positive or
// implausibly large as time-limited (0). No real race exceeds ~2000 laps.
func saneMaxLaps(n int32) int32 {
	if n <= 0 || n > 2000 {
		return 0
	}
	return n
}

// mapPlayerDetail builds the Car Management view for the player car from its
// telemetry plus the session-wide driving aids in the Extended buffer.
func mapPlayerDetail(f *wire.Frame) PlayerDetail {
	p, ok := f.Player()
	if !ok {
		return PlayerDetail{}
	}
	vt := &p.Telemetry
	ext := f.Extended
	d := PlayerDetail{
		Present: true,
		Rpm:     vt.EngineRPM,
		MaxRpm:  vt.EngineMaxRPM,
		// Engine water/oil and electric motor temps are already in Celsius from LMU
		// (unlike the tire and brake temps, which are Kelvin). Track/ambient also come
		// in Celsius from ScoringInfo.
		WaterTemp:        vt.EngineWaterTemp,
		OilTemp:          vt.EngineOilTemp,
		ElectricState:    int(vt.ElectricBoostMotorState),
		ElectricTemp:     vt.ElectricBoostMotorTemperature,
		FrontDownforce:   vt.FrontDownforce,
		RearDownforce:    vt.RearDownforce,
		Drag:             vt.Drag,
		FrontRideHeight:  vt.FrontRideHeight,
		RearRideHeight:   vt.RearRideHeight,
		RearBrakeBias:    vt.RearBrakeBias,
		TractionControl:  int(ext.TractionControl),
		ABS:              int(ext.AntiLockBrakes),
		StabilityControl: int(ext.StabilityControl),
		PitSpeedLimit:    float64(ext.CurrentPitSpeedLimit),
	}
	for i := range 8 {
		d.DentSeverity[i] = int(vt.DentSeverity[i])
		if d.DentSeverity[i] > d.WorstDent {
			d.WorstDent = d.DentSeverity[i]
		}
	}
	return d
}

// mapCar projects one vehicle. trackLen is the lap length used to normalise the
// car's distance-along-lap into the 0..1 fraction the circle plots.
func mapCar(v *wire.Vehicle, trackLen float64) CarState {
	vt := &v.Telemetry
	vs := &v.Scoring

	c := CarState{
		ID:           vt.ID,
		Fuel:         vt.Fuel,
		FuelCapacity: vt.FuelCapacity,
		Battery:      vt.BatteryChargeFraction,
		CarName:      wire.GoString(vt.VehicleName[:]),
	}

	// Scoring carries the race-control view (position, gaps, pits). It is absent
	// for a car with no matched scoring row, so guard on HasScoring.
	if v.HasScoring != 0 {
		c.Driver = wire.GoString(vs.DriverName[:])
		c.Class = wire.GoString(vs.VehicleClass[:])
		c.Place = int(vs.Place)
		c.TotalLaps = int(vs.TotalLaps)
		c.InPits = vs.InPits != 0
		c.PitState = int(vs.PitState)
		c.NumPitstops = int(vs.NumPitstops)
		c.GapToLeader = vs.TimeBehindLeader
		c.GapToNext = vs.TimeBehindNext
		c.LapsBehindLeader = int(vs.LapsBehindLeader)
		c.BestLap = vs.BestLapTime
		c.LastLap = vs.LastLapTime
		c.CurSector = int(vs.Sector)
		c.IsPlayer = vs.IsPlayer != 0
		if c.CarName == "" {
			c.CarName = wire.GoString(vs.VehicleName[:])
		}
		if trackLen > 0 {
			c.LapDistFrac = clamp01(vs.LapDist / trackLen)
		}
	}

	// Tires: wheels are ordered FL, FR, RL, RR. Front wheels carry the front
	// compound, rears the rear compound.
	frontCompound := wire.GoString(vt.FrontTireCompoundName[:])
	rearCompound := wire.GoString(vt.RearTireCompoundName[:])
	for i := range 4 {
		w := &vt.Wheels[i]
		compound := frontCompound
		if i >= 2 {
			compound = rearCompound
		}
		c.Tires[i] = TireState{
			Temp:      [3]float64{kToC(w.Temperature[0]), kToC(w.Temperature[1]), kToC(w.Temperature[2])},
			Pressure:  w.Pressure,
			Wear:      w.Wear,
			BrakeTemp: kToC(w.BrakeTemp), // LMU reports brake temp in Kelvin
			Compound:  compound,
		}
	}
	return c
}

// mapFlags derives the global flag state from the Rules and ScoringInfo buffers.
func mapFlags(f *wire.Frame) FlagState {
	r := f.Rules
	si := f.ScoringInfo
	yellow := r.YellowFlagState > 0 || si.YellowFlagState > 0
	scActive := r.SafetyCarActive != 0
	fl := FlagState{
		Yellow:    yellow,
		SafetyCar: r.SafetyCarExists != 0,
		SCActive:  scActive,
		Green:     !yellow && !scActive,
	}
	for i := range 3 {
		fl.SectorFlags[i] = int(si.SectorFlag[i])
	}
	return fl
}

// mapWeather pulls the session weather. Base temps/rain come from ScoringInfo; the
// dedicated Weather buffer adds the 3x3 rain grid, cloudiness and peak wind.
func mapWeather(f *wire.Frame) WeatherState {
	si := f.ScoringInfo
	w := f.Weather
	return WeatherState{
		AmbientTemp: si.AmbientTemp,
		TrackTemp:   si.TrackTemp,
		Raining:     si.Raining,
		RainGrid:    w.Raining,
		WindMax:     w.WindMaxSpeed,
		Cloudiness:  w.Cloudiness,
	}
}

// clamp01 keeps a fraction within [0,1); a car momentarily reading slightly past
// the line shouldn't wrap the circle marker to the wrong side.
func clamp01(v float64) float64 {
	if v < 0 || math.IsNaN(v) {
		return 0
	}
	if v >= 1 {
		return math.Mod(v, 1)
	}
	return v
}
