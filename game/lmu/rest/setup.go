package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// CarSetup is the player car's active setup, read from /rest/garage/summary. This
// is data the rF2 shared memory does not expose at all — the engine's balance
// advisory is a slip-telemetry heuristic precisely because the setup was
// previously unavailable. Groups preserve the in-game category and setting order
// so it reads like the garage setup sheet.
type CarSetup struct {
	ActiveSetup    string       `json:"active_setup"`
	DefaultSetup   string       `json:"default_setup"`
	CompareToSetup string       `json:"compare_to_setup"`
	TrackFolder    string       `json:"track_folder"`
	FixedSetupRace bool         `json:"fixed_setup_race"`
	UnsavedChanges bool         `json:"unsaved_changes"`
	Car            SetupCar     `json:"car"`
	Groups         []SetupGroup `json:"groups"`
}

// SetupCar identifies the car the setup belongs to.
type SetupCar struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	Manufacturer string `json:"manufacturer"`
	Engine       string `json:"engine"`
	Class        string `json:"class"` // the model tree path (e.g. "WEC 2026, GT3, Porsche 911 GT3 R LMGT3")
}

// SetupGroup is one category of settings (e.g. "SUSPENSION_FRONT").
type SetupGroup struct {
	Category string         `json:"category"`
	Settings []SetupSetting `json:"settings"`
}

// SetupSetting is one adjustable setting. StringValue is the in-game display
// (e.g. "50.0:50.0", "9 (Understeer)"); Value is the raw step index. LastSaved
// differs from StringValue when the current value has unsaved changes.
type SetupSetting struct {
	Key         string  `json:"key"`
	Caption     string  `json:"caption"`
	StringValue string  `json:"string_value"`
	Value       float64 `json:"value"`
	MinValue    float64 `json:"min_value"`
	MaxValue    float64 `json:"max_value"`
	LastSaved   string  `json:"last_saved"`
	Changed     bool    `json:"changed"` // current value differs from the last saved one
}

// SetupFile is one saved setup from /rest/garage/setup (the list of saveable
// setups for the current car). Names ending in "\" are track folders.
type SetupFile struct {
	Name             string `json:"name"`
	Created          string `json:"created"`
	Modified         string `json:"modified"`
	NumDiffUpgrades  int    `json:"numDiffUpgrades"`
	SameVehicleClass bool   `json:"sameVehicleClass"`
}

// Setup reads the active car setup from /rest/garage/summary and flattens its
// nested settingSummaries (category → key → setting) into ordered groups, skipping
// the non-setting bookkeeping entries (e.g. "diffCount") the game mixes in.
func (c *Client) Setup(ctx context.Context) (*CarSetup, error) {
	body, status, err := c.get(ctx, "/rest/garage/summary")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("garage summary: http %d", status)
	}
	var raw struct {
		ActiveSetup    string `json:"activeSetup"`
		DefaultSetup   string `json:"defaultSetup"`
		CompareToSetup string `json:"compareToSetup"`
		TrackFolder    string `json:"currentTrackFolder"`
		FixedSetupRace bool   `json:"fixedSetupRace"`
		UnsavedChanges bool   `json:"unsavedChanges"`
		Car            struct {
			Name              string `json:"name"`
			Manufacturer      string `json:"manufacturer"`
			Engine            string `json:"engine"`
			FullPathTree      string `json:"fullPathTree"`
			DisplayProperties struct {
				DisplayName string `json:"displayName"`
			} `json:"displayProperties"`
		} `json:"car"`
		SettingSummaries json.RawMessage `json:"settingSummaries"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("garage summary decode: %w", err)
	}

	out := &CarSetup{
		ActiveSetup:    raw.ActiveSetup,
		DefaultSetup:   raw.DefaultSetup,
		CompareToSetup: raw.CompareToSetup,
		TrackFolder:    raw.TrackFolder,
		FixedSetupRace: raw.FixedSetupRace,
		UnsavedChanges: raw.UnsavedChanges,
		Car: SetupCar{
			Name:         raw.Car.Name,
			DisplayName:  raw.Car.DisplayProperties.DisplayName,
			Manufacturer: raw.Car.Manufacturer,
			Engine:       raw.Car.Engine,
			Class:        raw.Car.FullPathTree,
		},
	}

	groups, err := parseSettingGroups(raw.SettingSummaries)
	if err != nil {
		return nil, err
	}
	out.Groups = groups
	return out, nil
}

// SetupList reads the saved setups available for the current car
// (/rest/garage/setup).
func (c *Client) SetupList(ctx context.Context) ([]SetupFile, error) {
	var out []SetupFile
	if err := c.getJSON(ctx, "/rest/garage/setup", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// parseSettingGroups walks the settingSummaries object preserving the in-game
// order of both categories and the settings within each. Entries that are not
// setting objects (a scalar "diffCount", say) are skipped.
func parseSettingGroups(raw json.RawMessage) ([]SetupGroup, error) {
	cats, err := orderedEntries(raw)
	if err != nil {
		return nil, fmt.Errorf("settingSummaries: %w", err)
	}
	groups := make([]SetupGroup, 0, len(cats))
	for _, cat := range cats {
		settings, err := orderedEntries(cat.Val)
		if err != nil {
			// A non-object category value is bookkeeping, not a group — skip it.
			continue
		}
		group := SetupGroup{Category: cat.Key}
		for _, s := range settings {
			var rs struct {
				Key         string  `json:"key"`
				Caption     string  `json:"caption"`
				StringValue string  `json:"stringValue"`
				Value       float64 `json:"value"`
				MinValue    float64 `json:"minValue"`
				MaxValue    float64 `json:"maxValue"`
				LastSaved   string  `json:"lastSavedStringValue"`
			}
			if err := json.Unmarshal(s.Val, &rs); err != nil || rs.Key == "" {
				// "diffCount" and friends are scalars / lack a key — not settings.
				continue
			}
			group.Settings = append(group.Settings, SetupSetting{
				Key:         rs.Key,
				Caption:     rs.Caption,
				StringValue: rs.StringValue,
				Value:       rs.Value,
				MinValue:    rs.MinValue,
				MaxValue:    rs.MaxValue,
				LastSaved:   rs.LastSaved,
				Changed:     rs.LastSaved != "" && rs.LastSaved != rs.StringValue,
			})
		}
		if len(group.Settings) > 0 {
			groups = append(groups, group)
		}
	}
	return groups, nil
}

// entry is one key/value pair of a JSON object, retaining source order.
type entry struct {
	Key string
	Val json.RawMessage
}

// orderedEntries decodes a JSON object into its key/value pairs in source order
// (encoding/json into a map loses order, which would scramble a setup sheet). It
// errors if raw is not a JSON object.
func orderedEntries(raw json.RawMessage) ([]entry, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not an object")
	}
	var out []entry
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("non-string key")
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		out = append(out, entry{Key: key, Val: val})
	}
	return out, nil
}
