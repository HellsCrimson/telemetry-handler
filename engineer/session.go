// Package engineer turns the raw, game-specific telemetry frames into a single,
// game-agnostic "session model" that the Strategy Planner frontend consumes.
//
// Why this layer exists
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
// Phase 1 is "instantaneous" only: every field here comes straight from the
// current frame. The stateful, per-corner accumulation (tire wear per mini-sector,
// fuel per straight) is added later in lapaccum.go / minisectors.go.
package engineer

import (
	"math"

	"telemetry-handler/lmu/wire"
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
}

// TireState is one corner's tire condition. Temps are Celsius (the wire format is
// Kelvin); BrakeTemp is already Celsius.
type TireState struct {
	Temp     [3]float64 `json:"temp"`     // left/center/right, Celsius
	Pressure float64    `json:"pressure"` // kPa
	Wear     float64    `json:"wear"`     // 0..1 (1 = fresh in rF2? treated as a fraction)
	BrakeTemp float64   `json:"brake_temp"` // Celsius
	Compound string     `json:"compound"` // e.g. "Soft"
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
		MaxLaps:        si.MaxLaps,
		PlayerID:       f.PlayerID,
		Flags:          mapFlags(f),
		Weather:        mapWeather(f),
		Cars:           make([]CarState, 0, len(f.Vehicles)),
	}
	for i := range f.Vehicles {
		s.Cars = append(s.Cars, mapCar(&f.Vehicles[i], si.LapDist))
	}
	return s
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
			BrakeTemp: w.BrakeTemp,
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
