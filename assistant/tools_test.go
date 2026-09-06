package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"telemetry-handler/engineer"
)

func toolCallChunk(index int, name, args string) string {
	return fmt.Sprintf(
		`{"choices":[{"delta":{"tool_calls":[{"index":%d,"id":"c%d","type":"function","function":{"name":%q,"arguments":%q}}]}}]}`,
		index, index, name, args)
}

func testRegistry(state StateSource) *Registry {
	return NewRegistry(
		PitCommandTool(),
		GetRivalTool(state),
		GetForecastTool(state),
		GetSectorComparisonTool(state),
		GetStrategyTool(state),
	)
}

// A lookup must reach the model as a tool result and be answered on the next
// pass — that is the whole point of the hop.
func TestLookupResultIsFedBackAndAnswered(t *testing.T) {
	calls := 0
	scripted := scriptedServer(t, func(n int) []string {
		calls = n
		if n == 0 {
			return []string{toolCallChunk(0, "get_rival", `{\"place\":7}`)}
		}
		return []string{textDelta("Backmarker is a lap down and no threat.")}
	})

	st := strategyState()
	llm := NewLLM(LLMConfig{BaseURL: scripted.URL, Model: "m", Timeout: 5 * time.Second})
	in := NewInterpreterWithTools(llm, func() engineer.SessionState { return st },
		testRegistry(func() engineer.SessionState { return st }), t.Logf)

	reply, err := in.Interpret(context.Background(), "how's the car in seventh doing?", nil)
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if calls < 1 {
		t.Fatal("expected a second pass after the lookup")
	}
	if !strings.Contains(reply.Speech, "Backmarker") {
		t.Errorf("expected the answer from the second pass, got %q", reply.Speech)
	}
	sent := scripted.Body(1)
	if !strings.Contains(sent, "P7 Backmarker") {
		t.Errorf("the tool result should be fed back to the model:\n%s", sent)
	}
	if !strings.Contains(sent, `"role":"tool"`) {
		t.Errorf("the result should be sent as a tool message:\n%s", sent)
	}
}

// One hop, then answer. A chain of lookups puts the reply past the corner it was
// about.
func TestToolHopsAreCapped(t *testing.T) {
	passes := 0
	scripted := scriptedServer(t, func(n int) []string {
		passes = n + 1
		// Always ask for another lookup; the cap is what has to stop this.
		return []string{toolCallChunk(0, "get_rival", `{\"place\":1\}`)}
	})

	st := strategyState()
	llm := NewLLM(LLMConfig{BaseURL: scripted.URL, Model: "m", Timeout: 5 * time.Second})
	in := NewInterpreterWithTools(llm, func() engineer.SessionState { return st },
		testRegistry(func() engineer.SessionState { return st }), t.Logf)

	if _, err := in.Interpret(context.Background(), "tell me about the leader", nil); err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if passes != maxToolHops+1 {
		t.Errorf("made %d passes, want %d (one hop then answer)", passes, maxToolHops+1)
	}
}

// A tool that fails must not abort the turn: the model should be told, so it can
// say it could not look the thing up.
func TestToolFailureIsReportedToTheModel(t *testing.T) {
	scripted := scriptedServer(t, func(n int) []string {
		if n == 0 {
			return []string{toolCallChunk(0, "no_such_tool", `{}`)}
		}
		return []string{textDelta("I could not look that up.")}
	})

	st := strategyState()
	llm := NewLLM(LLMConfig{BaseURL: scripted.URL, Model: "m", Timeout: 5 * time.Second})
	in := NewInterpreterWithTools(llm, func() engineer.SessionState { return st },
		testRegistry(func() engineer.SessionState { return st }), t.Logf)

	reply, err := in.Interpret(context.Background(), "something odd", nil)
	if err != nil {
		t.Fatalf("a failing tool must not fail the turn: %v", err)
	}
	if !strings.Contains(scripted.Body(1), "no such tool") {
		t.Errorf("the failure should be reported to the model:\n%s", scripted.Body(1))
	}
	if reply.Speech == "" {
		t.Error("the model should still have answered")
	}
}

func TestGetRivalFindsByPlaceAndByName(t *testing.T) {
	st := strategyState()
	tool := GetRivalTool(func() engineer.SessionState { return st })

	res, err := tool.Handler(context.Background(), json.RawMessage(`{"place":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Leader") {
		t.Errorf("lookup by place: %q", res.Text)
	}
	// The gap to the player is the thing actually being asked about.
	if !strings.Contains(res.Text, "ahead of you") {
		t.Errorf("expected the gap relative to the player: %q", res.Text)
	}

	res, err = tool.Handler(context.Background(), json.RawMessage(`{"driver":"backmark"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Backmarker") {
		t.Errorf("lookup by partial name: %q", res.Text)
	}
}

func TestGetRivalReportsNoMatch(t *testing.T) {
	st := strategyState()
	tool := GetRivalTool(func() engineer.SessionState { return st })
	res, err := tool.Handler(context.Background(), json.RawMessage(`{"place":99}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "No car") {
		t.Errorf("expected a clear miss, got %q", res.Text)
	}
}

// The full forecast is exactly what the briefing truncates, so the tool must not
// truncate it too.
func TestGetForecastReturnsEveryNode(t *testing.T) {
	st := strategyState()
	tool := GetForecastTool(func() engineer.SessionState { return st })
	res, err := tool.Handler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "finish") {
		t.Errorf("the tool should carry nodes the briefing drops:\n%s", res.Text)
	}
}

func TestGetSectorComparisonCoversTheWholeLap(t *testing.T) {
	st := strategyState()
	tool := GetSectorComparisonTool(func() engineer.SessionState { return st })
	res, err := tool.Handler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"T1 +0.50s", "T2 +0.10s", "total +0.60s"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("missing %q:\n%s", want, res.Text)
		}
	}
}

func TestGetStrategyBreaksDownTheStop(t *testing.T) {
	st := strategyState()
	st.Strategy.PitEstimate = engineer.PitEstimate{Total: 32, Fuel: 20, Tires: 12}
	tool := GetStrategyTool(func() engineer.SessionState { return st })
	res, err := tool.Handler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "fuel 20s") || !strings.Contains(res.Text, "tyres 12s") {
		t.Errorf("expected the stop breakdown:\n%s", res.Text)
	}
}

func TestToolsWithoutASessionSaySo(t *testing.T) {
	none := func() engineer.SessionState { return engineer.SessionState{} }
	for _, tool := range []Tool{GetRivalTool(none), GetForecastTool(none), GetSectorComparisonTool(none), GetStrategyTool(none)} {
		res, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
		if err != nil {
			t.Errorf("%s: %v", tool.Name, err)
			continue
		}
		if res.Text == "" {
			t.Errorf("%s returned nothing rather than saying it has no data", tool.Name)
		}
	}
}

func TestRegistryRejectsUnknownTools(t *testing.T) {
	r := NewRegistry(PitCommandTool())
	if _, err := r.Call(context.Background(), "invented", nil); err == nil {
		t.Error("an invented tool name should be an error the model can read")
	}
}

func TestRegistryReplacesByName(t *testing.T) {
	r := NewRegistry(PitCommandTool())
	r.Add(Tool{Name: "pit_command", Description: "replaced",
		Handler: func(context.Context, json.RawMessage) (Result, error) { return Result{}, nil }})
	if got := len(r.Tools()); got != 1 {
		t.Errorf("re-registering should replace, got %d tools", got)
	}
	if r.Tools()[0].Description != "replaced" {
		t.Error("the later registration should win")
	}
}
