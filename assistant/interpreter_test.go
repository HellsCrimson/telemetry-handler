package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"telemetry-handler/engineer"
)

// sseServer replays a scripted server-sent-event stream, the way an
// OpenAI-compatible endpoint does. It also records the request body so tests can
// assert what was sent.
func sseServer(t *testing.T, events []string) (*httptest.Server, *[]byte) {
	t.Helper()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the whole body: a single Read returns only what happens to be
		// buffered, which silently truncated once the tool list grew.
		buf, _ := io.ReadAll(r.Body)
		body = buf
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &body
}

// textDelta builds one content chunk.
func textDelta(s string) string {
	return fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, s)
}

func newTestInterpreter(t *testing.T, srv *httptest.Server) *Interpreter {
	t.Helper()
	llm := NewLLM(LLMConfig{BaseURL: srv.URL, Model: "test-model", Timeout: 5 * time.Second})
	return NewInterpreter(llm, func() engineer.SessionState { return sampleState() }, t.Logf)
}

// A spoken answer must reach the speaker sentence by sentence while the model is
// still generating — that is the whole reason the reply is streamed.
func TestInterpreterSpeaksSentencesAsTheyStream(t *testing.T) {
	srv, _ := sseServer(t, []string{
		textDelta("Fuel is good for "),
		textDelta("eleven laps."),
		textDelta(" You need nine, so you're fine."),
	})
	in := newTestInterpreter(t, srv)

	var mu sync.Mutex
	var spoken []string
	reply, err := in.Interpret(context.Background(), "how's my fuel?", func(s string) {
		mu.Lock()
		spoken = append(spoken, s)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if len(spoken) != 2 {
		t.Fatalf("expected 2 sentences, got %d: %q", len(spoken), spoken)
	}
	if spoken[0] != "Fuel is good for eleven laps." {
		t.Errorf("first sentence = %q", spoken[0])
	}
	if !strings.Contains(reply.Speech, "you're fine") {
		t.Errorf("full speech = %q", reply.Speech)
	}
	if len(reply.Actions) != 0 {
		t.Errorf("a question should not stage pit actions, got %v", reply.Actions)
	}
}

// A tool call must come back as staged actions, parsed by the real grammar.
func TestInterpreterStagesPitCommandThroughTheGrammar(t *testing.T) {
	call := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"pit_command","arguments":"{\"command\":\"all tyres wet, energy to 90\"}"}}]}}]}`
	srv, _ := sseServer(t, []string{call})
	in := newTestInterpreter(t, srv)

	reply, err := in.Interpret(context.Background(), "put the wets on and fill the energy", nil)
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if len(reply.Actions) != 2 {
		t.Fatalf("expected 2 staged actions, got %d", len(reply.Actions))
	}
	if reply.Speech != "" {
		t.Errorf("a staged command should not also speak, got %q", reply.Speech)
	}
}

// Tool-call arguments arrive split across chunks; they must be reassembled.
func TestInterpreterReassemblesSplitToolArguments(t *testing.T) {
	srv, _ := sseServer(t, []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"pit_command","arguments":"{\"comm"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"more wing\"}"}}]}}]}`,
	})
	in := newTestInterpreter(t, srv)

	reply, err := in.Interpret(context.Background(), "give me more rear wing", nil)
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if len(reply.Actions) != 1 {
		t.Fatalf("expected the reassembled command to stage 1 action, got %d", len(reply.Actions))
	}
}

// A command the grammar cannot parse must fail closed — no actions staged —
// rather than applying something unintended.
func TestInterpreterRejectsUnparseableCommand(t *testing.T) {
	call := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"pit_command","arguments":"{\"command\":\"engage the flux capacitor\"}"}}]}}]}`
	srv, _ := sseServer(t, []string{call})
	in := newTestInterpreter(t, srv)

	reply, err := in.Interpret(context.Background(), "do something odd", nil)
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	if len(reply.Actions) != 0 {
		t.Errorf("an unparseable command must stage nothing, got %v", reply.Actions)
	}
}

// The live briefing has to reach the model, or it is answering from nothing.
func TestInterpreterSendsTheBriefingAndDisablesThinking(t *testing.T) {
	srv, body := sseServer(t, []string{textDelta("You are P4, about ten seconds back.")})
	in := newTestInterpreter(t, srv)
	if _, err := in.Interpret(context.Background(), "where am I?", nil); err != nil {
		t.Fatalf("Interpret: %v", err)
	}

	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		EnableThinking *bool `json:"enable_thinking"`
	}
	if err := json.Unmarshal(*body, &req); err != nil {
		t.Fatalf("request body: %v (%s)", err, *body)
	}
	last := req.Messages[len(req.Messages)-1].Content
	if !strings.Contains(last, "Sebring") || !strings.Contains(last, "where am I?") {
		t.Errorf("the briefing and question should both be sent, got:\n%s", last)
	}
	offered := map[string]bool{}
	for _, tool := range req.Tools {
		offered[tool.Function.Name] = true
	}
	for _, want := range []string{"pit_command", "get_rival", "get_forecast", "get_sector_comparison", "get_strategy"} {
		if !offered[want] {
			t.Errorf("tool %q was not offered (got %v)", want, offered)
		}
	}
	// Thinking mode would spend seconds reasoning before the first spoken word.
	if req.EnableThinking == nil || *req.EnableThinking {
		t.Error("thinking mode must be disabled")
	}
}

// Follow-ups only work if the previous exchange is replayed.
func TestInterpreterRemembersTheLastExchange(t *testing.T) {
	srv, body := sseServer(t, []string{textDelta("You are fourth on the road.")})
	in := newTestInterpreter(t, srv)
	if _, err := in.Interpret(context.Background(), "where am I?", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Interpret(context.Background(), "and the car behind?", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(*body), "where am I?") {
		t.Errorf("the earlier question should be replayed as history:\n%s", *body)
	}

	in.Reset()
	if _, err := in.Interpret(context.Background(), "fresh start", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(*body), "where am I?") {
		t.Error("Reset should clear the conversation")
	}
}

// An endpoint that errors must surface as ErrLLMUnavailable so the engine falls
// back to the grammar instead of dropping the driver's call.
func TestInterpreterReportsAnUnavailableModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"Not authenticated"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	in := newTestInterpreter(t, srv)
	_, err := in.Interpret(context.Background(), "how's my fuel?", nil)
	if err == nil {
		t.Fatal("expected an error from a failing endpoint")
	}
	if !strings.Contains(err.Error(), "Not authenticated") {
		t.Errorf("the endpoint's reason should be reported, got %v", err)
	}
}

func TestSentenceSplitter(t *testing.T) {
	var s sentenceSplitter
	var got []string
	for _, delta := range []string{"Box this ", "lap, box box. ", "Tyres are ", "gone."} {
		got = append(got, s.push(delta)...)
	}
	if tail := s.flush(); tail != "" {
		got = append(got, tail)
	}
	want := []string{"Box this lap, box box.", "Tyres are gone."}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A decimal point mid-number must not be mistaken for the end of a sentence.
func TestSentenceSplitterKeepsShortFragmentsTogether(t *testing.T) {
	var s sentenceSplitter
	var got []string
	for _, delta := range []string{"P3.", "2 seconds ahead of you now."} {
		got = append(got, s.push(delta)...)
	}
	if tail := s.flush(); tail != "" {
		got = append(got, tail)
	}
	if len(got) != 1 {
		t.Errorf("expected one sentence, got %q", got)
	}
}

func TestEndpointTolerAtesEitherBaseForm(t *testing.T) {
	for _, base := range []string{"http://box:4399", "http://box:4399/", "http://box:4399/v1"} {
		l := NewLLM(LLMConfig{BaseURL: base, Model: "m"})
		if got := l.endpoint(); got != "http://box:4399/v1/chat/completions" {
			t.Errorf("endpoint(%q) = %q", base, got)
		}
	}
}

// The switch that actually works on llama.cpp must be on the wire. It was found
// by measurement, not documentation, and the two neighbours that look
// equivalent are ignored by that server — so assert the working one explicitly.
func TestRequestDisablesThinkingTheWaySeversActuallyRead(t *testing.T) {
	srv, body := sseServer(t, []string{textDelta("Fuel is fine for eleven laps.")})
	in := newTestInterpreter(t, srv)
	if _, err := in.Interpret(context.Background(), "fuel?", nil); err != nil {
		t.Fatal(err)
	}
	var req struct {
		Thinking       *struct{ Type string } `json:"thinking"`
		EnableThinking *bool                  `json:"enable_thinking"`
		ChatTemplate   map[string]any         `json:"chat_template_kwargs"`
	}
	if err := json.Unmarshal(*body, &req); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if req.Thinking == nil || req.Thinking.Type != "disabled" {
		t.Error(`missing {"thinking":{"type":"disabled"}} — the only switch llama.cpp honours here`)
	}
	// The other two are for vLLM/sglang; harmless but must not be dropped either.
	if req.EnableThinking == nil || *req.EnableThinking {
		t.Error("enable_thinking should still be sent for other backends")
	}
	if req.ChatTemplate == nil {
		t.Error("chat_template_kwargs should still be sent for other backends")
	}
}

// A reply that is all reasoning and no answer must be an error. Returning it as
// an empty success is what made a completely non-functional engineer look like a
// quiet one for a week.
func TestReplyThatIsAllReasoningIsAnError(t *testing.T) {
	srv, _ := sseServer(t, []string{
		`{"choices":[{"delta":{"reasoning_content":"We need to respond to the driver. Let me think about the fuel."}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"length"}]}`,
	})
	in := newTestInterpreter(t, srv)

	_, err := in.Interpret(context.Background(), "how's my fuel?", nil)
	if err == nil {
		t.Fatal("an answerless reply must not look like success")
	}
	if !errors.Is(err, ErrLLMUnavailable) {
		t.Errorf("should be reported as unavailable so the grammar takes over, got %v", err)
	}
	if !strings.Contains(err.Error(), "reasoning") {
		t.Errorf("the error should name the cause, got %v", err)
	}
}

// Reasoning alongside a real answer is merely wasteful, not a failure.
func TestReasoningAlongsideAnAnswerIsFine(t *testing.T) {
	srv, _ := sseServer(t, []string{
		`{"choices":[{"delta":{"reasoning_content":"Thinking about it."}}]}`,
		textDelta("Fuel is good for eleven laps."),
	})
	in := newTestInterpreter(t, srv)

	reply, err := in.Interpret(context.Background(), "how's my fuel?", nil)
	if err != nil {
		t.Fatalf("an answer that also reasoned should succeed: %v", err)
	}
	if reply.Speech != "Fuel is good for eleven laps." {
		t.Errorf("speech = %q", reply.Speech)
	}
}

// A keep-alive must not cost a real generation, and must not fail merely because
// the model spent its four tokens thinking — liveness is the question.
func TestPingIsCheapAndToleratesReasoning(t *testing.T) {
	srv, body := sseServer(t, []string{
		`{"choices":[{"delta":{"reasoning_content":"The user wants a word."}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"length"}]}`,
	})
	llm := NewLLM(LLMConfig{BaseURL: srv.URL, Model: "test-model", MaxTokens: 220, Timeout: 5 * time.Second})
	if err := llm.Ping(context.Background()); err != nil {
		t.Fatalf("a reasoning-only reply still proves the endpoint is alive: %v", err)
	}
	var req struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(*body, &req); err != nil {
		t.Fatal(err)
	}
	if req.MaxTokens != warmTokens {
		t.Errorf("ping asked for %d tokens, want the %d-token warm budget", req.MaxTokens, warmTokens)
	}
	// The configured budget must be untouched for real answers.
	if llm.cfg.MaxTokens != 220 {
		t.Errorf("Ping mutated the shared config: max_tokens is now %d", llm.cfg.MaxTokens)
	}
}

func TestPingReportsARealOutage(t *testing.T) {
	llm := NewLLM(LLMConfig{BaseURL: "http://127.0.0.1:1", Model: "m", Timeout: 2 * time.Second})
	if err := llm.Ping(context.Background()); err == nil {
		t.Fatal("expected an error from an unreachable endpoint")
	}
}

// A conversation must not survive into a different race: answers about the last
// session's fuel and rivals are worse than no memory, and the driver cannot tell
// it is happening.
func TestInterpreterForgetsAcrossSessions(t *testing.T) {
	srv, body := sseServer(t, []string{textDelta("You are running fourth.")})
	st := sampleState()
	llm := NewLLM(LLMConfig{BaseURL: srv.URL, Model: "test-model", Timeout: 5 * time.Second})
	in := NewInterpreter(llm, func() engineer.SessionState { return st }, t.Logf)

	if _, err := in.Interpret(context.Background(), "where am I?", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Interpret(context.Background(), "and behind?", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(*body), "where am I?") {
		t.Fatal("history should be replayed within one session")
	}

	st.Track = "Monza" // new race
	if _, err := in.Interpret(context.Background(), "where am I?", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(*body), "and behind?") {
		t.Errorf("the previous session's conversation leaked into the new one:\n%s", *body)
	}
}

// Restarting the same session rewinds the clock; that is a new race too.
func TestInterpreterForgetsOnSessionRestart(t *testing.T) {
	srv, body := sseServer(t, []string{textDelta("Fourth on the road.")})
	st := sampleState()
	st.SessionTime = 1200
	llm := NewLLM(LLMConfig{BaseURL: srv.URL, Model: "test-model", Timeout: 5 * time.Second})
	in := NewInterpreter(llm, func() engineer.SessionState { return st }, t.Logf)

	if _, err := in.Interpret(context.Background(), "first question", nil); err != nil {
		t.Fatal(err)
	}
	st.SessionTime = 4 // restarted
	if _, err := in.Interpret(context.Background(), "second question", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(*body), "first question") {
		t.Errorf("a restarted session should clear the conversation:\n%s", *body)
	}
}

// A brief clock wobble between frames is not a restart.
func TestInterpreterKeepsMemoryThroughAClockWobble(t *testing.T) {
	srv, body := sseServer(t, []string{textDelta("Fourth on the road.")})
	st := sampleState()
	st.SessionTime = 1200
	llm := NewLLM(LLMConfig{BaseURL: srv.URL, Model: "test-model", Timeout: 5 * time.Second})
	in := NewInterpreter(llm, func() engineer.SessionState { return st }, t.Logf)

	if _, err := in.Interpret(context.Background(), "first question", nil); err != nil {
		t.Fatal(err)
	}
	st.SessionTime = 1199 // a frame out of order, not a new race
	if _, err := in.Interpret(context.Background(), "second question", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(*body), "first question") {
		t.Errorf("a one-second wobble must not wipe the conversation:\n%s", *body)
	}
}

// scriptStub is a stub endpoint whose reply depends on which pass it is, so a
// tool hop (call, result, answer) can be exercised end to end.
type scriptStub struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

// Body returns the request body of the nth pass (0-based).
func (s *scriptStub) Body(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n >= len(s.bodies) {
		return ""
	}
	return s.bodies[n]
}

// scriptedServer replays script(n) on the nth request.
func scriptedServer(t *testing.T, script func(n int) []string) *scriptStub {
	t.Helper()
	stub := &scriptStub{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		n := len(stub.bodies)
		stub.bodies = append(stub.bodies, string(buf))
		stub.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, e := range script(n) {
			fmt.Fprintf(w, "data: %s\n\n", e)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(stub.Close)
	return stub
}
