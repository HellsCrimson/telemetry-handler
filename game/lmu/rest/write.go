package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// SetSetupValue sets one setup setting to a raw step index. key is the setting's
// VM_*/WM_* key (e.g. "VM_BRAKE_BALANCE", "WM_CAMBER-W_FL"); value is the integer
// step (the same units as SetupSetting.Value). The game clamps to the setting's
// min/max. POST /rest/garage/{key} with a GarageVal body {"value": N}.
//
// This only takes effect in the garage — it is the write counterpart to Setup().
func (c *Client) SetSetupValue(ctx context.Context, key string, value int) error {
	if key == "" {
		return fmt.Errorf("setup key required")
	}
	body, _ := json.Marshal(struct {
		Value int `json:"value"`
	}{value})
	status, err := c.post(ctx, "/rest/garage/"+key, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("set %s: http %d", key, status)
	}
	return nil
}

// SetPitMenuValue selects option index currentSetting on the pit-menu component
// identified by pmc (the "PMC Value" from PitMenu()). It round-trips: it reads the
// current menu, changes the target item's currentSetting (clamped to its option
// range), and posts the WHOLE menu back as a JSON array.
//
// IMPORTANT: /rest/garage/PitMenu/loadPitMenu requires a JSON ARRAY body — sending
// a bare object crashes the game. Marshalling the []item slice guarantees an array.
func (c *Client) SetPitMenuValue(ctx context.Context, pmc, currentSetting int) error {
	body, status, err := c.get(ctx, "/rest/garage/PitMenu/receivePitMenu")
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("read pit menu: http %d", status)
	}
	// Decode into ordered maps so every field (PMC Value, name, settings, default)
	// is preserved exactly when posting back — only currentSetting is changed.
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return fmt.Errorf("decode pit menu: %w", err)
	}
	found := false
	for _, it := range items {
		var pv int
		if raw, ok := it["PMC Value"]; ok {
			_ = json.Unmarshal(raw, &pv)
		}
		if pv != pmc {
			continue
		}
		// Clamp the requested index to the component's option range.
		idx := max(currentSetting, 0)
		if raw, ok := it["settings"]; ok {
			var opts []json.RawMessage
			if json.Unmarshal(raw, &opts) == nil && len(opts) > 0 && idx > len(opts)-1 {
				idx = len(opts) - 1
			}
		}
		it["currentSetting"], _ = json.Marshal(idx)
		found = true
		break
	}
	if !found {
		return fmt.Errorf("pit menu component %d not found", pmc)
	}
	// items is a slice → marshals to a JSON array (never a bare object).
	out, err := json.Marshal(items)
	if err != nil {
		return err
	}
	status, err = c.post(ctx, "/rest/garage/PitMenu/loadPitMenu", out)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("load pit menu: http %d", status)
	}
	return nil
}

// post sends a POST with a JSON body and returns the status code.
func (c *Client) post(ctx context.Context, path string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
