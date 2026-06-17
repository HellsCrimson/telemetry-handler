package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// summaryJSON is a trimmed copy of a real /rest/garage/summary response. It keeps
// the category/setting ordering, the "diffCount" bookkeeping entry that is NOT a
// setting, and an unsaved change (lastSavedStringValue != stringValue) so the
// parser is tested against the real quirks.
const summaryJSON = `{
"activeSetup":"CDA_992_SPA_Race_Safe",
"defaultSetup":"<Factory Defaults>",
"compareToSetup":"",
"currentTrackFolder":"Spa",
"fixedSetupRace":false,
"unsavedChanges":true,
"car":{"name":"Porsche 911 GT3 R","manufacturer":"Porsche","engine":"Boxer 4.2","fullPathTree":"WEC 2026, GT3, Porsche 911 GT3 R LMGT3","displayProperties":{"displayName":"Manthey #91"}},
"settingSummaries":{
  "AERODYNAMICS_FRONT":{
    "VM_FRONT_WING":{"caption":"FRONT WING","key":"VM_FRONT_WING","stringValue":"Standard","value":0,"minValue":0,"maxValue":1,"lastSavedStringValue":"Standard"},
    "diffCount":0
  },
  "SUSPENSION_FRONT":{
    "VM_BRAKE_BALANCE":{"caption":"BRAKE BALANCE","key":"VM_BRAKE_BALANCE","stringValue":"50.0:50.0","value":28,"minValue":0,"maxValue":57,"lastSavedStringValue":"57.0:43.0"}
  }
}
}`

func TestSetupParsing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/garage/summary", writeJSON(summaryJSON))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, time.Second)
	setup, err := c.Setup(context.Background())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if setup.ActiveSetup != "CDA_992_SPA_Race_Safe" || !setup.UnsavedChanges {
		t.Errorf("header not parsed: %+v", setup)
	}
	if setup.Car.DisplayName != "Manthey #91" || setup.Car.Class != "WEC 2026, GT3, Porsche 911 GT3 R LMGT3" {
		t.Errorf("car not parsed: %+v", setup.Car)
	}
	// Two categories, in source order (AERODYNAMICS_FRONT before SUSPENSION_FRONT).
	if len(setup.Groups) != 2 {
		t.Fatalf("groups = %d, want 2: %+v", len(setup.Groups), setup.Groups)
	}
	if setup.Groups[0].Category != "AERODYNAMICS_FRONT" || setup.Groups[1].Category != "SUSPENSION_FRONT" {
		t.Errorf("category order not preserved: %v, %v", setup.Groups[0].Category, setup.Groups[1].Category)
	}
	// diffCount must be skipped — only the real setting remains.
	front := setup.Groups[0].Settings
	if len(front) != 1 || front[0].Key != "VM_FRONT_WING" {
		t.Errorf("diffCount not skipped / setting missing: %+v", front)
	}
	if front[0].Changed {
		t.Errorf("front wing matches last saved, should not be marked changed")
	}
	// The brake balance has an unsaved change.
	bb := setup.Groups[1].Settings[0]
	if bb.Key != "VM_BRAKE_BALANCE" || bb.StringValue != "50.0:50.0" || bb.LastSaved != "57.0:43.0" || !bb.Changed {
		t.Errorf("brake balance not parsed/changed: %+v", bb)
	}
}

func TestSetupHTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/garage/summary", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, time.Second)
	if _, err := c.Setup(context.Background()); err == nil {
		t.Errorf("expected error on 503")
	}
}
