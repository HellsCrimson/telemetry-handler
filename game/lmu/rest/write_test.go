package rest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSetSetupValue(t *testing.T) {
	var gotPath string
	var gotBody map[string]int
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/garage/VM_BRAKE_BALANCE", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, time.Second)
	if err := c.SetSetupValue(context.Background(), "VM_BRAKE_BALANCE", 29); err != nil {
		t.Fatalf("SetSetupValue: %v", err)
	}
	if gotPath != "/rest/garage/VM_BRAKE_BALANCE" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["value"] != 29 {
		t.Errorf("body = %v, want {value:29}", gotBody)
	}
}

func TestSetSetupValueHTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/garage/VM_X", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, time.Second)
	if err := c.SetSetupValue(context.Background(), "VM_X", 1); err == nil {
		t.Errorf("expected error on 500")
	}
}

// menuFixture is a minimal pit menu with two components and an option list, so the
// round-trip can clamp and preserve fields.
const menuFixture = `[
{"PMC Value":6,"currentSetting":42,"default":100,"name":"VIRTUAL ENERGY:","settings":[{"text":"0%"},{"text":"1%"},{"text":"2%"}]},
{"PMC Value":7,"currentSetting":84,"default":49,"name":"FUEL RATIO:","settings":[{"text":"a"},{"text":"b"}]}
]`

func TestSetPitMenuValuePostsArray(t *testing.T) {
	var posted json.RawMessage
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/garage/PitMenu/receivePitMenu", writeJSON(menuFixture))
	mux.HandleFunc("/rest/garage/PitMenu/loadPitMenu", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		posted = json.RawMessage(b)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, time.Second)
	if err := c.SetPitMenuValue(context.Background(), 6, 2); err != nil {
		t.Fatalf("SetPitMenuValue: %v", err)
	}

	// CRITICAL: the body must be a JSON ARRAY — a bare object crashes the game.
	trimmed := []byte(posted)
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\n' || trimmed[0] == '\t') {
		trimmed = trimmed[1:]
	}
	if len(trimmed) == 0 || trimmed[0] != '[' {
		t.Fatalf("pit menu POST body must be a JSON array, got: %s", posted)
	}

	var items []map[string]any
	if err := json.Unmarshal(posted, &items); err != nil {
		t.Fatalf("posted body not an array of objects: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("all menu items should be preserved, got %d", len(items))
	}
	// Target changed to the requested index; other component untouched; fields kept.
	for _, it := range items {
		if it["PMC Value"].(float64) == 6 {
			if it["currentSetting"].(float64) != 2 {
				t.Errorf("VE currentSetting = %v, want 2", it["currentSetting"])
			}
			if it["name"] != "VIRTUAL ENERGY:" || it["settings"] == nil {
				t.Errorf("fields not preserved: %v", it)
			}
		}
		if it["PMC Value"].(float64) == 7 && it["currentSetting"].(float64) != 84 {
			t.Errorf("untargeted component changed: %v", it["currentSetting"])
		}
	}
}

func TestSetPitMenuValueClampsAndValidates(t *testing.T) {
	var posted json.RawMessage
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/garage/PitMenu/receivePitMenu", writeJSON(menuFixture))
	mux.HandleFunc("/rest/garage/PitMenu/loadPitMenu", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		posted = json.RawMessage(b)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, time.Second)

	// Index 99 is out of range for VE (3 options) → clamped to 2.
	if err := c.SetPitMenuValue(context.Background(), 6, 99); err != nil {
		t.Fatalf("SetPitMenuValue: %v", err)
	}
	var items []map[string]any
	_ = json.Unmarshal(posted, &items)
	for _, it := range items {
		if it["PMC Value"].(float64) == 6 && it["currentSetting"].(float64) != 2 {
			t.Errorf("index not clamped: %v", it["currentSetting"])
		}
	}

	// Unknown component → error, no post.
	if err := c.SetPitMenuValue(context.Background(), 999, 0); err == nil {
		t.Errorf("expected error for unknown PMC")
	}
}
