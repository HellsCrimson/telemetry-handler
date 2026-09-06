package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"telemetry-handler/engineer"
	"telemetry-handler/game/lmu/rest"
)

type fakeGarage struct {
	setup *rest.CarSetup
	err   error
}

func (f fakeGarage) Setup(context.Context) (*rest.CarSetup, error) { return f.setup, f.err }

func sampleSetup() *rest.CarSetup {
	return &rest.CarSetup{
		ActiveSetup: "Sebring Race",
		Car:         rest.SetupCar{DisplayName: "Oreca 07"},
		Groups: []rest.SetupGroup{
			{Category: "Aero", Settings: []rest.SetupSetting{
				{Key: "REARWING", Caption: "Rear Wing", Value: 8, StringValue: "8", MinValue: 1, MaxValue: 12},
				{Key: "FRONTSPLITTER", Caption: "Front Splitter", Value: 3, MinValue: 0, MaxValue: 5},
			}},
			{Category: "Brakes", Settings: []rest.SetupSetting{
				{Key: "BRAKEBIAS", Caption: "Brake Bias", Value: 55, MinValue: 40, MaxValue: 70},
			}},
		},
	}
}

// inGarage puts the player in the pits, which is the only place a setup change
// can take effect.
func inGarage() engineer.SessionState {
	st := sampleState()
	st.Cars[3].InPits = true
	return st
}

func TestGetSetupListsKeysValuesAndRanges(t *testing.T) {
	tool := GetSetupTool(fakeGarage{setup: sampleSetup()})
	res, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	// A change needs the key and a legal value; both must be here, or setting a
	// field costs a second lookup.
	for _, want := range []string{"Rear Wing", "[REARWING]", "range 1..12", "Brake Bias"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("missing %q:\n%s", want, res.Text)
		}
	}
}

func TestGetSetupFilters(t *testing.T) {
	tool := GetSetupTool(fakeGarage{setup: sampleSetup()})
	res, err := tool.Handler(context.Background(), json.RawMessage(`{"filter":"wing"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Rear Wing") {
		t.Errorf("filter dropped the match:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "Brake Bias") {
		t.Errorf("filter should exclude other fields:\n%s", res.Text)
	}
}

// The whole point of the gate: on track the game ignores setup writes, so
// confirming one would leave the driver believing in a change that never
// happened.
func TestSetSetupRefusedOnTrack(t *testing.T) {
	st := sampleState() // not in the pits
	tool := SetSetupTool(fakeGarage{setup: sampleSetup()}, func() engineer.SessionState { return st })

	res, err := tool.Handler(context.Background(), json.RawMessage(`{"key":"REARWING","value":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan != nil {
		t.Fatal("a setup change must not be staged while on track")
	}
	if !strings.Contains(res.Text, "garage") {
		t.Errorf("the refusal should say why: %q", res.Text)
	}
}

func TestSetSetupStagesInTheGarage(t *testing.T) {
	st := inGarage()
	tool := SetSetupTool(fakeGarage{setup: sampleSetup()}, func() engineer.SessionState { return st })

	res, err := tool.Handler(context.Background(), json.RawMessage(`{"key":"REARWING","value":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan == nil || len(res.Plan.Setup) != 1 {
		t.Fatalf("expected one staged setup write, got %+v", res.Plan)
	}
	w := res.Plan.Setup[0]
	if w.Key != "REARWING" || w.Value != 10 {
		t.Errorf("staged %+v", w)
	}
	// The prompt is read back to the driver, so it has to be legible.
	if !strings.Contains(res.Plan.Desc, "REAR WING") || !strings.Contains(res.Plan.Desc, "10") {
		t.Errorf("confirmation description = %q", res.Plan.Desc)
	}
}

// A hallucinated value must be caught here rather than posted to the car.
func TestSetSetupRejectsAnOutOfRangeValue(t *testing.T) {
	st := inGarage()
	tool := SetSetupTool(fakeGarage{setup: sampleSetup()}, func() engineer.SessionState { return st })

	res, err := tool.Handler(context.Background(), json.RawMessage(`{"key":"REARWING","value":40}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan != nil {
		t.Fatal("an out-of-range value must not be staged")
	}
	if !strings.Contains(res.Text, "1 to 12") {
		t.Errorf("the refusal should state the real range: %q", res.Text)
	}
}

func TestSetSetupRejectsAnUnknownKey(t *testing.T) {
	st := inGarage()
	tool := SetSetupTool(fakeGarage{setup: sampleSetup()}, func() engineer.SessionState { return st })

	res, err := tool.Handler(context.Background(), json.RawMessage(`{"key":"FLUXCAPACITOR","value":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan != nil {
		t.Fatal("an invented key must not be staged")
	}
	if !strings.Contains(res.Text, "get_setup") {
		t.Errorf("the refusal should point at the way to find real keys: %q", res.Text)
	}
}

// The model may echo the human label instead of the key.
func TestSetSetupAcceptsTheCaptionAsAKey(t *testing.T) {
	st := inGarage()
	tool := SetSetupTool(fakeGarage{setup: sampleSetup()}, func() engineer.SessionState { return st })

	res, err := tool.Handler(context.Background(), json.RawMessage(`{"key":"Brake Bias","value":57}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan == nil || res.Plan.Setup[0].Key != "BRAKEBIAS" {
		t.Fatalf("a caption should resolve to its key, got %+v", res.Plan)
	}
}

func TestSetSetupReportsAGarageReadFailure(t *testing.T) {
	st := inGarage()
	tool := SetSetupTool(fakeGarage{err: fmt.Errorf("connection refused")}, func() engineer.SessionState { return st })

	res, err := tool.Handler(context.Background(), json.RawMessage(`{"key":"REARWING","value":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan != nil {
		t.Fatal("nothing should be staged when the setup cannot be read")
	}
	if !strings.Contains(res.Text, "connection refused") {
		t.Errorf("the reason should reach the driver: %q", res.Text)
	}
}

func TestGetSetupFlagsAFixedSetupRace(t *testing.T) {
	setup := sampleSetup()
	setup.FixedSetupRace = true
	tool := GetSetupTool(fakeGarage{setup: setup})
	res, _ := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if !strings.Contains(res.Text, "FIXED SETUP") {
		t.Errorf("a fixed-setup race should be stated up front:\n%s", res.Text)
	}
}
