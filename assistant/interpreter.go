package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"telemetry-handler/engineer"
	"telemetry-handler/voice"
)

// historyTurns is how many past messages are replayed to the model, so
// follow-ups like "and the one behind?" work without the prompt growing without
// bound.
const historyTurns = 6

// maxToolHops caps how many times the model may go and fetch something before it
// has to answer. Each hop is another full generation (~2-3s measured), so a
// chain of lookups puts the answer past the corner it was about. One hop, then
// answer with what you have.
const maxToolHops = 1

// StateSource supplies the current session snapshot at question time.
type StateSource func() engineer.SessionState

// Interpreter is the LLM-backed voice.Interpreter: it answers questions from the
// live session and turns free-form pit requests into grammar commands.
//
// Pit changes are deliberately routed back through the existing grammar rather
// than emitted as structured actions. Asking an 8B model to fill in a fifteen-
// field action struct is far less reliable than asking it for a phrase the
// deterministic parser already understands, and it means an unparseable
// suggestion fails closed ("NOT UNDERSTOOD") instead of applying something
// unintended.
type Interpreter struct {
	llm   *LLM
	state StateSource
	tools *Registry
	logf  func(string, ...any)

	mu     sync.Mutex
	memory []chatMessage
	// session identifies the race the memory belongs to, so a conversation cannot
	// leak across sessions.
	session string
	// lastET is the session clock at the previous exchange; going backwards means
	// the session restarted.
	lastET float64
}

// NewInterpreter builds the engineer with the default tool set: the pit command
// plus the read tools covering what the briefing leaves out.
func NewInterpreter(llm *LLM, state StateSource, logf func(string, ...any)) *Interpreter {
	in := NewInterpreterWithTools(llm, state, nil, logf)
	in.tools = NewRegistry(
		PitCommandTool(),
		GetRivalTool(state),
		GetForecastTool(state),
		GetSectorComparisonTool(state),
		GetStrategyTool(state),
	)
	return in
}

// NewInterpreterWithTools builds one with an explicit registry, for callers that
// want to add or restrict capabilities (the garage tools, or a test).
func NewInterpreterWithTools(llm *LLM, state StateSource, tools *Registry, logf func(string, ...any)) *Interpreter {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Interpreter{llm: llm, state: state, tools: tools, logf: logf}
}

// Tools exposes the registry so the app can register extra capabilities.
func (in *Interpreter) Tools() *Registry { return in.tools }

// systemPrompt defines the persona and, crucially, the command vocabulary the
// pit_command tool must produce — the model does not have to invent a syntax,
// only translate into one that already exists.
const systemPrompt = `You are a race engineer on the pit wall of an endurance team, talking to your driver over the radio during a race in Le Mans Ultimate.

HOW TO SPEAK
- One or two SHORT sentences. Radio brevity. The driver is at 250 km/h.
- Plain spoken English. No lists, no markdown, no numbers-as-digits gimmicks.
- Answer only from the BRIEFING below. If it is not there, say you do not have it.
- Never invent lap times, gaps, fuel figures or positions.

CHANGING THE CAR
- Any request to change the car for the next pit stop MUST use the pit_command tool.
- When you call the tool, say nothing else — the driver is asked to confirm separately.
- The command must use this exact vocabulary:
  tyres:     "change all tyres" | "all tyres wet" | "all tyres medium" | "front tyres" | "rear tyres wet" | "don't change tyres"
  pressure:  "front pressure 140" | "rear pressure 150" | "all pressures 145" | "more front pressure" | "rear pressure plus 2"
  energy:    "energy to 80" | "full energy" | "no energy"
  fuel:      "fuel to 30" | "fill it up" | "no fuel"
  ducts:     "open front ducts" | "close rear duct" | "front brake duct 40 percent" | "all ducts closed"
  wing:      "wing to 11" | "more wing" | "less wing" | "wing plus 2"
  brakes:    "replace brakes" | "keep brakes"
  damage:    "repair damage" | "no repair"
  grille:    "grille 3" | "more grille"
- Several can be chained with commas: "all tyres wet, energy to 90, more wing".
- Work out the numbers yourself from the briefing when the driver is vague. "Enough
  fuel to the end" means computing it from the fuel figures you were given.

LOOKING THINGS UP
- The briefing already covers your own car, the cars immediately around you, the
  staged pit menu, the next forecast and where the last lap lost time. Answer from
  it directly.
- Only call a lookup tool for something the briefing genuinely does not contain.
  Each lookup costs the driver two more seconds of silence, and you only get one.

CHANGING THE SETUP
- The car setup is separate from the pit menu and can only be changed in the garage.
- Call get_setup first to find the exact field key and the range it accepts, then
  set_setup. Never guess a key or a value.`

// Interpret answers the driver. speak is called with each finished sentence as
// the reply streams, so the engineer starts talking before the model is done.
func (in *Interpreter) Interpret(ctx context.Context, transcript string, speak func(string)) (voice.Reply, error) {
	var st engineer.SessionState
	brief := "No live session."
	if in.state != nil {
		st = in.state()
		brief = Brief(st)
	}
	// Drop the conversation when the race changes underneath us. Carrying answers
	// about the last session's fuel and rivals into a new one is worse than
	// having no memory at all, and the driver has no way to tell it is happening.
	in.forgetOnSessionChange(st)

	msgs := []chatMessage{{Role: "system", Content: systemPrompt}}
	msgs = append(msgs, in.recall()...)
	msgs = append(msgs, chatMessage{
		Role:    "user",
		Content: "BRIEFING\n" + brief + "\nDRIVER: " + transcript,
	})

	// Speak whole sentences as they complete; a token-by-token feed would
	// synthesize gibberish.
	var split sentenceSplitter
	onText := func(delta string) {
		if speak == nil {
			return
		}
		for _, sentence := range split.push(delta) {
			speak(sentence)
		}
	}

	started := time.Now()
	for hop := 0; ; hop++ {
		out, err := in.llm.stream(ctx, msgs, in.tools.specs(), onText)
		if err != nil {
			return voice.Reply{}, err
		}
		in.logf("voice: engineer hop %d in %v (%d chars, %d tool calls)",
			hop, time.Since(started).Round(time.Millisecond), len(out.Text), len(out.Calls))

		// A pit command ends the turn: parse it with the grammar and stage it.
		if cmd := pitCommandOf(out.Calls); cmd != "" {
			in.flush(speak, &split)
			in.logf("voice: engineer pit command %q", cmd)
			u := voice.Parse(cmd)
			if len(u.Actions) > 0 {
				in.remember(transcript, "(staged: "+cmd+")")
				return voice.Reply{Actions: u.Actions}, nil
			}
			in.logf("voice: engineer command %q did not parse", cmd)
		}

		// Any other tool call is a lookup or a staged change.
		if lookups := otherCalls(out.Calls); len(lookups) > 0 && hop < maxToolHops {
			staged, results := in.runTools(ctx, lookups)
			if staged != nil {
				in.flush(speak, &split)
				in.remember(transcript, "(staged: "+staged.Desc+")")
				return voice.Reply{Plan: staged}, nil
			}
			msgs = append(msgs, chatMessage{Role: "assistant", ToolCalls: lookups})
			msgs = append(msgs, results...)
			continue
		}

		in.flush(speak, &split)
		text := strings.TrimSpace(out.Text)
		in.remember(transcript, text)
		return voice.Reply{Speech: text}, nil
	}
}

// flush speaks whatever sentence fragment is left at the end of a stream.
func (in *Interpreter) flush(speak func(string), split *sentenceSplitter) {
	if speak == nil {
		return
	}
	if tail := split.flush(); tail != "" {
		speak(tail)
	}
}

// runTools executes lookups and renders their output as tool messages. A tool
// that stages a change ends the turn instead, and its plan is returned.
func (in *Interpreter) runTools(ctx context.Context, calls []toolCall) (*voice.Plan, []chatMessage) {
	out := make([]chatMessage, 0, len(calls))
	for _, c := range calls {
		res, err := in.tools.Call(ctx, c.Function.Name, json.RawMessage(c.Function.Arguments))
		if err != nil {
			// Report the failure to the model rather than aborting: it can say it
			// could not look the thing up, which is better than silence.
			in.logf("voice: engineer tool %s: %v", c.Function.Name, err)
			out = append(out, chatMessage{Role: "tool", ToolCallID: c.ID, Content: "error: " + err.Error()})
			continue
		}
		if res.stages() {
			if res.Plan != nil {
				return res.Plan, nil
			}
		}
		in.logf("voice: engineer tool %s -> %d chars", c.Function.Name, len(res.Text))
		out = append(out, chatMessage{Role: "tool", ToolCallID: c.ID, Content: res.Text})
	}
	return nil, out
}

// otherCalls returns every call that is not the pit command, which is handled
// separately because the grammar — not a tool handler — has to validate it.
func otherCalls(calls []toolCall) []toolCall {
	var out []toolCall
	for _, c := range calls {
		if c.Function.Name != "pit_command" {
			out = append(out, c)
		}
	}
	return out
}

// pitCommandOf pulls the command string out of the first pit_command call.
func pitCommandOf(calls []toolCall) string {
	for _, c := range calls {
		if c.Function.Name != "pit_command" {
			continue
		}
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(c.Function.Arguments), &args); err != nil {
			continue
		}
		if cmd := strings.TrimSpace(args.Command); cmd != "" {
			return cmd
		}
	}
	return ""
}

func (in *Interpreter) recall() []chatMessage {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]chatMessage(nil), in.memory...)
}

// remember keeps the last few turns so follow-up questions have context. Only
// the driver's words and our reply are kept — replaying old briefings would
// feed the model stale telemetry.
func (in *Interpreter) remember(driver, reply string) {
	if reply == "" {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.memory = append(in.memory,
		chatMessage{Role: "user", Content: driver},
		chatMessage{Role: "assistant", Content: reply},
	)
	if len(in.memory) > historyTurns {
		in.memory = in.memory[len(in.memory)-historyTurns:]
	}
}

// Reset clears the conversation, for a new session.
func (in *Interpreter) Reset() {
	in.mu.Lock()
	in.memory = nil
	in.mu.Unlock()
}

// forgetOnSessionChange clears the memory when the track or session type changes,
// or when the session clock jumps backwards (a restart).
func (in *Interpreter) forgetOnSessionChange(st engineer.SessionState) {
	if !st.Available {
		return
	}
	key := fmt.Sprintf("%s|%d", st.Track, st.SessionType)

	in.mu.Lock()
	changed := in.session != "" && in.session != key
	// A clock that went backwards by more than a rounding wobble is a restart.
	restarted := st.SessionTime < in.lastET-5
	in.session, in.lastET = key, st.SessionTime
	if changed || restarted {
		in.memory = nil
	}
	in.mu.Unlock()

	if changed || restarted {
		in.logf("voice: engineer: new session, clearing the conversation")
	}
}

// sentenceSplitter accumulates streamed text and hands back complete sentences.
type sentenceSplitter struct{ buf strings.Builder }

// push adds a delta and returns any sentences that are now complete.
func (s *sentenceSplitter) push(delta string) []string {
	var out []string
	for _, r := range delta {
		s.buf.WriteRune(r)
		if r != '.' && r != '!' && r != '?' && r != '\n' {
			continue
		}
		// Require a few words, so "P3." or a decimal point does not get spoken on
		// its own.
		if sentence := strings.TrimSpace(s.buf.String()); len(sentence) >= 12 {
			out = append(out, sentence)
			s.buf.Reset()
		}
	}
	return out
}

// flush returns whatever is left when the stream ends.
func (s *sentenceSplitter) flush() string {
	tail := strings.TrimSpace(s.buf.String())
	s.buf.Reset()
	return tail
}
