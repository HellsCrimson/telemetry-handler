package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"telemetry-handler/engineer"
)

// Read tools cover the long tail: what a driver asks occasionally and what would
// therefore be dead weight in every prompt. Each costs a whole extra generation
// (~2-3s), so anything asked routinely belongs in Brief instead.

// pitCommandDescription is shared by the tool spec and the system prompt so the
// vocabulary can never drift between what the model is told and what it is given.
const pitCommandDescription = "Stage one or more changes to the pit-stop menu. The driver must confirm before anything is applied."

// PitCommandTool stages pit-menu changes expressed in the voice grammar.
func PitCommandTool() Tool {
	return Tool{
		Name:        "pit_command",
		Description: pitCommandDescription,
		Params: objectSchema(map[string]any{
			"command": stringParam(`The change(s) in the documented command vocabulary, comma-separated, e.g. "all tyres wet, energy to 90".`),
		}, "command"),
		// The handler is a no-op: the interpreter parses the command with the
		// grammar itself, because an unparseable one has to fail closed there
		// rather than become a staged action here.
		Handler: func(context.Context, json.RawMessage) (Result, error) {
			return Result{}, nil
		},
	}
}

// GetRivalTool describes any car on the grid, not just the few either side of
// the player that Brief carries.
func GetRivalTool(state StateSource) Tool {
	return Tool{
		Name:        "get_rival",
		Description: "Look up one car on the grid by position or driver name. Use for cars outside the few reported in the briefing.",
		Params: objectSchema(map[string]any{
			"place":  intParam("Overall position (1 = leader)."),
			"driver": stringParam("Driver name, or part of it."),
		}),
		Handler: func(_ context.Context, args json.RawMessage) (Result, error) {
			var in struct {
				Place  int    `json:"place"`
				Driver string `json:"driver"`
			}
			_ = json.Unmarshal(args, &in)
			st := stateOf(state)
			if !st.Available {
				return Result{Text: "No live session."}, nil
			}
			car, ok := findCar(st, in.Place, in.Driver)
			if !ok {
				return Result{Text: "No car matches that."}, nil
			}
			return Result{Text: describeCar(st, car)}, nil
		},
	}
}

// GetForecastTool gives the full weather forecast, where Brief carries only the
// next few nodes.
func GetForecastTool(state StateSource) Tool {
	return Tool{
		Name:        "get_forecast",
		Description: "The full weather forecast for the rest of the session, node by node.",
		Params:      objectSchema(map[string]any{}),
		Handler: func(context.Context, json.RawMessage) (Result, error) {
			st := stateOf(state)
			if len(st.Strategy.Forecast) == 0 {
				return Result{Text: "No forecast available."}, nil
			}
			var b strings.Builder
			for _, f := range st.Strategy.Forecast {
				fmt.Fprintf(&b, "%s %s: %.0f%% rain, %.0fC, %s, wind %.0f\n",
					f.Session, shortNode(f.Node), f.RainChance, f.Temperature, f.Sky, f.WindSpeed)
			}
			return Result{Text: b.String()}, nil
		},
	}
}

// GetSectorComparisonTool gives the whole lap broken down, where Brief carries
// only the three worst losses.
func GetSectorComparisonTool(state StateSource) Tool {
	return Tool{
		Name:        "get_sector_comparison",
		Description: "The last lap compared with the driver's best, mini-sector by mini-sector, including where time was gained.",
		Params:      objectSchema(map[string]any{}),
		Handler: func(context.Context, json.RawMessage) (Result, error) {
			st := stateOf(state)
			p := playerCar(st)
			if p == nil || len(p.MiniSectors) == 0 || len(p.BestSectors) == 0 {
				return Result{Text: "No completed lap to compare yet."}, nil
			}
			var b strings.Builder
			var total float64
			for i := range p.MiniSectors {
				if i >= len(p.BestSectors) {
					break
				}
				last, best := p.MiniSectors[i].TimeSpent, p.BestSectors[i].TimeSpent
				if last <= 0 || best <= 0 {
					continue
				}
				d := last - best
				total += d
				fmt.Fprintf(&b, "%s %+.2fs (%.2f vs %.2f)\n", cornerName(st, i), d, last, best)
			}
			fmt.Fprintf(&b, "total %+.2fs", total)
			return Result{Text: b.String()}, nil
		},
	}
}

// GetStrategyTool exposes the REST-sourced strategy extras: the pit-stop time
// breakdown and the per-driver resource projections.
func GetStrategyTool(state StateSource) Tool {
	return Tool{
		Name:        "get_strategy",
		Description: "Pit-stop time breakdown and per-driver fuel and energy projections for the current stint.",
		Params:      objectSchema(map[string]any{}),
		Handler: func(context.Context, json.RawMessage) (Result, error) {
			st := stateOf(state)
			if !st.Strategy.Present {
				return Result{Text: "No strategy data (the REST API has not been polled)."}, nil
			}
			var b strings.Builder
			e := st.Strategy.PitEstimate
			fmt.Fprintf(&b, "Pit stop about %.0fs total: fuel %.0fs, tyres %.0fs, energy %.0fs, damage %.0fs, driver swap %.0fs, penalties %.0fs\n",
				e.Total, e.Fuel, e.Tires, e.VE, e.Damage, e.DriverSwap, e.Penalties)
			if st.Strategy.PitState != "" {
				fmt.Fprintf(&b, "Pit state: %s. Phase: %s\n", st.Strategy.PitState, st.Strategy.GamePhase)
			}
			for _, d := range st.Strategy.Drivers {
				fmt.Fprintf(&b, "%s: stint %d, lap %d, fuel %.1f, energy %.1f\n", d.Driver, d.Stint, d.Lap, d.Fuel, d.VE)
			}
			return Result{Text: b.String()}, nil
		},
	}
}

// --- helpers ---------------------------------------------------------------

func stateOf(state StateSource) engineer.SessionState {
	if state == nil {
		return engineer.SessionState{}
	}
	return state()
}

// findCar resolves a car by position, else by a case-insensitive driver-name
// substring — the model will pass whatever the driver said aloud.
func findCar(st engineer.SessionState, place int, driver string) (engineer.CarState, bool) {
	if place > 0 {
		for _, c := range st.Cars {
			if c.Place == place {
				return c, true
			}
		}
	}
	if name := strings.ToLower(strings.TrimSpace(driver)); name != "" {
		for _, c := range st.Cars {
			if strings.Contains(strings.ToLower(c.Driver), name) {
				return c, true
			}
		}
	}
	return engineer.CarState{}, false
}

// describeCar renders one car in the same shape as the briefing's rival lines,
// plus the gap to the player, which is the thing actually being asked about.
func describeCar(st engineer.SessionState, c engineer.CarState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "P%d %s", c.Place, orUnknown(c.Driver))
	if c.CarName != "" {
		fmt.Fprintf(&b, " (%s, %s)", c.CarName, c.Class)
	}
	fmt.Fprintf(&b, ", %d laps, %s", c.TotalLaps, plural(c.NumPitstops, "stop"))
	if c.InPits {
		b.WriteString(", in the pits")
	}
	if c.LastLap > 0 {
		fmt.Fprintf(&b, ", last %s, best %s", lap(c.LastLap), lap(c.BestLap))
	}
	if p := playerCar(st); p != nil && p.ID != c.ID {
		gap := gapBetween(*p, c)
		if gap > 0 {
			fmt.Fprintf(&b, ", %.1fs behind you", gap)
		} else {
			fmt.Fprintf(&b, ", %.1fs ahead of you", -gap)
		}
	}
	if wear := worstWear(c); wear > 0 {
		fmt.Fprintf(&b, ", tyres %.0f%% worn", wear*100)
	}
	return b.String()
}

func worstWear(c engineer.CarState) float64 {
	worst := 0.0
	for _, t := range c.Tires {
		if worn := 1 - t.Wear; worn > worst {
			worst = worn
		}
	}
	return worst
}

// sortedByPlace is used by tests and any caller wanting a stable grid order.
func sortedByPlace(cars []engineer.CarState) []engineer.CarState {
	out := append([]engineer.CarState(nil), cars...)
	sort.Slice(out, func(i, j int) bool { return out[i].Place < out[j].Place })
	return out
}
