package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"telemetry-handler/engineer"
	"telemetry-handler/game/lmu/rest"
	"telemetry-handler/voice"
)

// Garage reads the car's setup. It is an interface so the assistant does not
// depend on the REST client's construction, and so tests can supply a fixture.
type Garage interface {
	Setup(ctx context.Context) (*rest.CarSetup, error)
}

// GetSetupTool lists the car's setup so the engineer knows the field keys, their
// current values and the range each accepts before proposing a change.
func GetSetupTool(garage Garage) Tool {
	return Tool{
		Name:        "get_setup",
		Description: "The car's current garage setup: every field with its key, current value and allowed range. Read this before proposing a setup change.",
		Params: objectSchema(map[string]any{
			"filter": stringParam(`Optional: only fields whose name or category contains this, e.g. "wing", "brake", "spring".`),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			var in struct {
				Filter string `json:"filter"`
			}
			_ = json.Unmarshal(args, &in)
			setup, err := garage.Setup(ctx)
			if err != nil {
				return Result{Text: "Cannot read the setup: " + err.Error()}, nil
			}
			return Result{Text: describeSetup(setup, in.Filter)}, nil
		},
	}
}

// SetSetupTool stages a garage setup change for confirmation.
//
// Two gates stand in front of it, and both matter. The value is validated
// against the field's own range from the game, so a hallucinated number is
// rejected here rather than posted to the car. And it is refused unless the car
// is in the pits: LMU only applies most setup fields in the garage, so without
// the gate the engineer would confidently confirm a change the game then
// silently ignores — which is worse than refusing, because the driver believes
// it took effect.
func SetSetupTool(garage Garage, state StateSource) Tool {
	return Tool{
		Name:        "set_setup",
		Description: "Stage a change to one car setup field. Only possible in the garage. The driver must confirm before it is applied. Use get_setup first to find the key and its allowed range.",
		Params: objectSchema(map[string]any{
			"key":   stringParam("The setup field key, exactly as get_setup reported it."),
			"value": intParam("The new value, within the range get_setup reported."),
		}, "key", "value"),
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			var in struct {
				Key   string `json:"key"`
				Value int    `json:"value"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return Result{Text: "Bad arguments: " + err.Error()}, nil
			}
			in.Key = strings.TrimSpace(in.Key)
			if in.Key == "" {
				return Result{Text: "No setup key given."}, nil
			}

			if why, ok := garageOpen(stateOf(state)); !ok {
				return Result{Text: why}, nil
			}

			setup, err := garage.Setup(ctx)
			if err != nil {
				return Result{Text: "Cannot read the setup: " + err.Error()}, nil
			}
			field, ok := findSetting(setup, in.Key)
			if !ok {
				return Result{Text: fmt.Sprintf("No setup field %q. Use get_setup to list them.", in.Key)}, nil
			}
			if v := float64(in.Value); v < field.MinValue || v > field.MaxValue {
				return Result{Text: fmt.Sprintf("%s accepts %.0f to %.0f, not %d.",
					captionOf(field), field.MinValue, field.MaxValue, in.Value)}, nil
			}

			name := captionOf(field)
			return Result{Plan: &voice.Plan{
				Setup: []voice.SetupWrite{{
					Key:   field.Key,
					Value: in.Value,
					Name:  name,
					Label: fmt.Sprintf("%d", in.Value),
				}},
				Desc: fmt.Sprintf("%s TO %d", strings.ToUpper(name), in.Value),
			}}, nil
		},
	}
}

// garageOpen reports whether a setup change can actually take effect, and if not,
// a sentence the engineer can say.
func garageOpen(st engineer.SessionState) (string, bool) {
	if !st.Available {
		return "No live session, so I cannot change the setup.", false
	}
	p := playerCar(st)
	if p == nil {
		return "I cannot see your car, so I cannot change the setup.", false
	}
	if !p.InPits {
		return "I can only change the setup in the garage — the game will not take it on track.", false
	}
	return "", true
}

// describeSetup renders the setup for the model, optionally filtered. Values,
// ranges and keys are all included because a change needs the key and a legal
// value, and a second lookup would cost another hop.
func describeSetup(setup *rest.CarSetup, filter string) string {
	if setup == nil || len(setup.Groups) == 0 {
		return "No setup available."
	}
	filter = strings.ToLower(strings.TrimSpace(filter))
	var b strings.Builder
	if setup.Car.DisplayName != "" {
		fmt.Fprintf(&b, "%s, setup %q\n", setup.Car.DisplayName, setup.ActiveSetup)
	}
	if setup.FixedSetupRace {
		b.WriteString("FIXED SETUP RACE: changes are not allowed.\n")
	}
	matched := 0
	for _, g := range setup.Groups {
		var lines []string
		for _, s := range g.Settings {
			if filter != "" &&
				!strings.Contains(strings.ToLower(s.Caption), filter) &&
				!strings.Contains(strings.ToLower(s.Key), filter) &&
				!strings.Contains(strings.ToLower(g.Category), filter) {
				continue
			}
			value := s.StringValue
			if value == "" {
				value = fmt.Sprintf("%.0f", s.Value)
			}
			lines = append(lines, fmt.Sprintf("  %s [%s] = %s (range %.0f..%.0f)",
				captionOf(s), s.Key, value, s.MinValue, s.MaxValue))
			matched++
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "%s:\n%s\n", g.Category, strings.Join(lines, "\n"))
		}
	}
	if matched == 0 {
		return fmt.Sprintf("No setup fields match %q.", filter)
	}
	return b.String()
}

// findSetting resolves a key case-insensitively, then falls back to a caption
// match — the model may echo the human label rather than the key.
func findSetting(setup *rest.CarSetup, key string) (rest.SetupSetting, bool) {
	if setup == nil {
		return rest.SetupSetting{}, false
	}
	want := strings.ToLower(key)
	for _, g := range setup.Groups {
		for _, s := range g.Settings {
			if strings.ToLower(s.Key) == want {
				return s, true
			}
		}
	}
	for _, g := range setup.Groups {
		for _, s := range g.Settings {
			if strings.EqualFold(strings.TrimSpace(s.Caption), strings.TrimSpace(key)) {
				return s, true
			}
		}
	}
	return rest.SetupSetting{}, false
}

func captionOf(s rest.SetupSetting) string {
	if c := strings.TrimSpace(s.Caption); c != "" {
		return c
	}
	return s.Key
}
