package voice

import "context"

// Reply is what an Interpreter makes of one utterance: pit-menu intent, a
// confirmation, and/or something to say back.
type Reply struct {
	// Actions are pit-menu changes to stage for confirmation.
	Actions []Action
	// Plan is an already-resolved change to stage, for intents that need no
	// grammar resolution — currently car-setup writes, whose key and value come
	// straight from the game's own setup listing.
	Plan *Plan
	// Affirm/Cancel resolve a pending confirmation.
	Affirm bool
	Cancel bool
	// Speech is the full spoken answer, for the log and the banner. It has
	// normally already been said sentence by sentence through the Speak callback
	// as it streamed, so the engine must not speak it again.
	Speech string
}

// Interpreter turns a transcript into a Reply. The grammar implementation is
// deterministic and instant; the LLM one (assistant package) also answers
// questions, and calls speak with each sentence as it streams so the assistant
// starts talking before the whole answer exists.
//
// This interface is why voice does not import the assistant: the app injects the
// implementation, so the LLM can depend on the grammar's vocabulary without a
// cycle.
type Interpreter interface {
	Interpret(ctx context.Context, transcript string, speak func(sentence string)) (Reply, error)
}

// GrammarInterpreter is the deterministic parser: phrases straight to actions,
// no model involved. It is the default, and the fallback whenever the LLM is
// unreachable or too slow.
type GrammarInterpreter struct{}

func (GrammarInterpreter) Interpret(_ context.Context, transcript string, _ func(string)) (Reply, error) {
	u := Parse(transcript)
	return Reply{Actions: u.Actions, Affirm: u.Affirm, Cancel: u.Cancel}, nil
}
