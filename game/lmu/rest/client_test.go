package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fixtures are trimmed copies of real responses captured from a live LMU
// session, so the decoders are tested against the actual wire shapes.
const (
	gameStateActive  = `{"MultiStintState":"DRIVING","PitEntryDist":6583.12,"PitState":"EXITING","gamePhase":"GPHASE_GREEN","inControlOfVehicle":true,"inMonitor":false,"isReplayActive":false,"playerVehicleLoaded":true,"raceFinished":false,"teamVehicleState":"IN CONTROL","timeOfDay":41448.22}`
	gameStateUnavail = `{"status": "unavailable", "reason": "Game is not in an active session", "gameState": "Setup"}`
	pitEstimateJSON  = `{"brakeDucts":0.0,"brakes":0.0,"damage":0.0,"driverSwap":0.0,"fuel":2.0074,"penalties":0.0,"tires":0.0,"total":2.0074,"ve":0.0}`
	usageJSON        = `{"Antares Au":[{"lap":0,"pit":false,"stint":1,"ve":1.0}],"Matthias Rollins":[{"fuel":0.2252,"lap":0,"pit":false,"stint":1,"tyres":[100.0,100.0,100.0,100.0],"ve":0.31}]}`
	conditionJSON    = `{"brakeCondition":[1.0,1.0,1.0,1.0],"fuel":26.32,"fuelCapacity":117.0,"suspensionDamage":[0.0,0.0,0.0,0.0],"tireCondition":[1.0,1.0,0.9,1.0],"vehicleDamage":0.0}`
	weatherJSON      = `{"PRACTICE":{"FINISH":{"WNV_RAIN_CHANCE":{"currentValue":0,"stringValue":"0%"},"WNV_TEMPERATURE":{"currentValue":16,"stringValue":"16 °"},"WNV_SKY":{"currentValue":1,"stringValue":"Light Clouds"}},"NODE_25":{"WNV_RAIN_CHANCE":{"currentValue":5,"stringValue":"5%"}}}}`
	standingsJSON    = `[{"slotID":3,"driverName":"Antares Au","carNumber":"10","carClass":"GT3","fullTeamName":"Garage 59","vehicleName":"AMR","position":1,"bestLapTime":-1.0,"lastLapTime":0.0,"estimatedLapTime":139.38,"timeBehindLeader":0.0,"timeBehindNext":0.0,"lapsCompleted":0,"fuelFraction":0.70,"veFraction":1.0,"pitstops":0,"penalties":0,"pitState":"NONE","pitting":false,"inGarageStall":true,"flag":"GREEN"}]`
	pitMenuJSON      = `[{"PMC Value":6,"currentSetting":31,"default":100,"name":"VIRTUAL ENERGY:","settings":[{"text":"0% 0 laps"},{"text":"31% 6 laps"}]},{"PMC Value":5,"currentSetting":0,"default":0,"name":"DRIVER:","settings":[{"text":"M Rollins"}]}]`
)

func newTestServer(t *testing.T, gameState string, gameStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/sessions/GetGameState", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(gameStatus)
		_, _ = w.Write([]byte(gameState))
	})
	mux.HandleFunc("/rest/strategy/pitstop-estimate", writeJSON(pitEstimateJSON))
	mux.HandleFunc("/rest/strategy/usage", writeJSON(usageJSON))
	mux.HandleFunc("/rest/garage/getVehicleCondition", writeJSON(conditionJSON))
	mux.HandleFunc("/rest/sessions/weather", writeJSON(weatherJSON))
	mux.HandleFunc("/rest/watch/standings", writeJSON(standingsJSON))
	mux.HandleFunc("/rest/garage/PitMenu/receivePitMenu", writeJSON(pitMenuJSON))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestFetchActiveSession(t *testing.T) {
	srv := newTestServer(t, gameStateActive, http.StatusOK)
	c := NewClient(srv.URL, time.Second)
	now := time.Unix(1700000000, 0)
	snap := c.Fetch(context.Background(), now)

	if !snap.Available {
		t.Fatalf("expected available snapshot, reason=%q", snap.Reason)
	}
	if !snap.FetchedAt.Equal(now) {
		t.Errorf("FetchedAt = %v, want %v", snap.FetchedAt, now)
	}
	if snap.GameState == nil || snap.GameState.PitState != "EXITING" {
		t.Errorf("game state not decoded: %+v", snap.GameState)
	}
	if snap.PitEstimate == nil || snap.PitEstimate.Total != 2.0074 {
		t.Errorf("pit estimate not decoded: %+v", snap.PitEstimate)
	}
	if snap.Condition == nil || snap.Condition.FuelCapacity != 117.0 || snap.Condition.TireCondition[2] != 0.9 {
		t.Errorf("condition not decoded: %+v", snap.Condition)
	}
	player := snap.Usage["Matthias Rollins"]
	if len(player) != 1 || player[0].Fuel != 0.2252 || player[0].Tyres[0] != 100.0 || player[0].VE != 0.31 {
		t.Errorf("usage not decoded: %+v", snap.Usage)
	}
	if len(snap.Standings) != 1 || snap.Standings[0].DriverName != "Antares Au" || snap.Standings[0].EstimatedLapTime != 139.38 {
		t.Errorf("standings not decoded: %+v", snap.Standings)
	}
	if len(snap.Forecast) != 2 {
		t.Fatalf("forecast nodes = %d, want 2: %+v", len(snap.Forecast), snap.Forecast)
	}
	// Find the PRACTICE/FINISH node and check a value flattened correctly.
	var found bool
	for _, n := range snap.Forecast {
		if n.Session == "PRACTICE" && n.Node == "FINISH" {
			found = true
			if n.Values["WNV_SKY"].StringValue != "Light Clouds" {
				t.Errorf("forecast value: %+v", n.Values)
			}
		}
	}
	if !found {
		t.Errorf("PRACTICE/FINISH forecast node missing: %+v", snap.Forecast)
	}
	// Pit menu settings flattened from {text:...} objects to strings.
	if len(snap.PitMenu) != 2 || snap.PitMenu[0].Name != "VIRTUAL ENERGY:" ||
		len(snap.PitMenu[0].Settings) != 2 || snap.PitMenu[0].Settings[1] != "31% 6 laps" {
		t.Errorf("pit menu not decoded: %+v", snap.PitMenu)
	}
}

func TestFetchUnavailable(t *testing.T) {
	srv := newTestServer(t, gameStateUnavail, http.StatusServiceUnavailable)
	c := NewClient(srv.URL, time.Second)
	snap := c.Fetch(context.Background(), time.Unix(1700000000, 0))

	if snap.Available {
		t.Fatalf("expected unavailable snapshot")
	}
	if snap.Reason != "Game is not in an active session" {
		t.Errorf("reason = %q", snap.Reason)
	}
	if snap.GameState != nil || snap.PitEstimate != nil || snap.Standings != nil {
		t.Errorf("data sections should be nil when unavailable")
	}
}

func TestFetchPartialFailure(t *testing.T) {
	// game state OK, but a data endpoint returns null/500 — the snapshot stays
	// available with that one section nil.
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/sessions/GetGameState", writeJSON(gameStateActive))
	mux.HandleFunc("/rest/strategy/pitstop-estimate", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("null")) // endpoint returns null when not ready
	})
	mux.HandleFunc("/rest/garage/getVehicleCondition", writeJSON(conditionJSON))
	// other endpoints intentionally unregistered → 404
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, time.Second)
	snap := c.Fetch(context.Background(), time.Unix(1700000000, 0))
	if !snap.Available {
		t.Fatalf("expected available snapshot")
	}
	if snap.PitEstimate != nil {
		t.Errorf("null pit estimate should leave field nil, got %+v", snap.PitEstimate)
	}
	if snap.Condition == nil || snap.Condition.FuelCapacity != 117.0 {
		t.Errorf("condition should still decode: %+v", snap.Condition)
	}
	if snap.Standings != nil {
		t.Errorf("404 standings should leave field nil")
	}
}
