package app

import (
	"telemetry-handler/engineer"
	"telemetry-handler/game/lmu/rest"
)

// restToStrategy maps an LMU REST snapshot onto the game-agnostic strategy
// overlay the engine merges into its SessionState. This is the bridge that keeps
// the engineer package free of the rest package (mirroring how the app maps the
// LMU wire.Frame into the engine via Observe). A nil or unavailable snapshot maps
// to a zero (Present=false) overlay, which clears any prior one.
func restToStrategy(snap *rest.Snapshot) engineer.StrategyState {
	if snap == nil || !snap.Available {
		return engineer.StrategyState{}
	}
	s := engineer.StrategyState{Present: true}

	if gs := snap.GameState; gs != nil {
		s.GamePhase = gs.GamePhase
		s.PitState = gs.PitState
	}
	if pe := snap.PitEstimate; pe != nil {
		s.PitEstimate = engineer.PitEstimate{
			Total:      pe.Total,
			Fuel:       pe.Fuel,
			Tires:      pe.Tires,
			VE:         pe.VE,
			Damage:     pe.Damage,
			DriverSwap: pe.DriverSwap,
			Penalties:  pe.Penalties,
		}
	}
	if c := snap.Condition; c != nil {
		s.FuelCapacity = c.FuelCapacity
	}
	s.Forecast = forecastPoints(snap.Forecast)
	s.Drivers = driverUsage(snap.Usage)
	s.PitMenu = pitMenuEntries(snap.PitMenu)
	return s
}

// forecastPoints flattens the REST forecast nodes into the engine's
// ForecastPoint, pulling the WNV_* fields the strategy view needs.
func forecastPoints(nodes []rest.ForecastNode) []engineer.ForecastPoint {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]engineer.ForecastPoint, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, engineer.ForecastPoint{
			Session:     n.Session,
			Node:        n.Node,
			RainChance:  n.Values["WNV_RAIN_CHANCE"].CurrentValue,
			Temperature: n.Values["WNV_TEMPERATURE"].CurrentValue,
			Sky:         n.Values["WNV_SKY"].StringValue,
			WindSpeed:   n.Values["WNV_WINDSPEED"].CurrentValue,
		})
	}
	return out
}

// driverUsage flattens the per-driver usage map (keyed by name → list of stints)
// into a flat list, taking the latest (last) stint entry for each driver.
func driverUsage(usage map[string][]rest.UsageEntry) []engineer.DriverUsage {
	if len(usage) == 0 {
		return nil
	}
	out := make([]engineer.DriverUsage, 0, len(usage))
	for driver, entries := range usage {
		if len(entries) == 0 {
			continue
		}
		e := entries[len(entries)-1]
		out = append(out, engineer.DriverUsage{
			Driver: driver,
			Stint:  e.Stint,
			Lap:    e.Lap,
			VE:     e.VE,
			Fuel:   e.Fuel,
		})
	}
	return out
}

// pitMenuEntries resolves each pit-menu row's currently-selected option text.
func pitMenuEntries(items []rest.PitMenuItem) []engineer.PitMenuEntry {
	if len(items) == 0 {
		return nil
	}
	out := make([]engineer.PitMenuEntry, 0, len(items))
	for _, it := range items {
		current := ""
		if it.CurrentSetting >= 0 && it.CurrentSetting < len(it.Settings) {
			current = it.Settings[it.CurrentSetting]
		}
		out = append(out, engineer.PitMenuEntry{Name: it.Name, Current: current})
	}
	return out
}
