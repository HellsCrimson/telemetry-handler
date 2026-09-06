package assistant

import (
	"context"
	"encoding/json"
	"fmt"

	"telemetry-handler/voice"
)

// Tool is one capability the engineer can invoke.
//
// Handlers take and return plain data — JSON arguments in, text out — with no
// reference to the chat API. That is what lets the same set be exposed over MCP
// to an external client later without touching any of them: only the two
// adapters (toolSpec here, MCP schema there) differ.
type Tool struct {
	Name        string
	Description string
	// Params is the JSON schema for the arguments object.
	Params map[string]any
	// Handler runs the tool. Read tools fill Result.Text; action tools fill the
	// staging fields instead.
	Handler func(ctx context.Context, args json.RawMessage) (Result, error)
}

// Result is what a tool produced.
//
// Read tools return Text, which is fed back to the model for one more pass.
// Action tools return something to stage for the driver's confirmation and end
// the turn — nothing they produce reaches the car without a spoken "yes".
type Result struct {
	// Text is fed back to the model (read tools).
	Text string
	// Actions are pit-menu changes to resolve through the grammar and stage.
	Actions []voice.Action
	// Plan is an already-resolved change to stage (setup writes, which need no
	// grammar resolution).
	Plan *voice.Plan
}

// stages reports whether this result ends the turn with something awaiting
// confirmation, rather than feeding the model more context.
func (r Result) stages() bool { return len(r.Actions) > 0 || r.Plan != nil }

// Registry is the set of tools available to the engineer.
type Registry struct {
	order  []string
	byName map[string]Tool
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{byName: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		r.Add(t)
	}
	return r
}

// Add registers a tool, replacing any existing one of the same name.
func (r *Registry) Add(t Tool) {
	if _, exists := r.byName[t.Name]; !exists {
		r.order = append(r.order, t.Name)
	}
	r.byName[t.Name] = t
}

// Tools returns the registered tools in registration order.
func (r *Registry) Tools() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.byName[name])
	}
	return out
}

// specs renders the registry as chat-completions tool definitions.
func (r *Registry) specs() []toolSpec {
	if r == nil || len(r.order) == 0 {
		return nil
	}
	out := make([]toolSpec, 0, len(r.order))
	for _, name := range r.order {
		t := r.byName[name]
		var spec toolSpec
		spec.Type = "function"
		spec.Function.Name = t.Name
		spec.Function.Description = t.Description
		spec.Function.Parameters = t.Params
		out = append(out, spec)
	}
	return out
}

// Call runs a tool by name. An unknown tool is an error the model can read and
// recover from, not a crash — models do invent tool names.
func (r *Registry) Call(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	t, ok := r.byName[name]
	if !ok {
		return Result{}, fmt.Errorf("no such tool %q", name)
	}
	return t.Handler(ctx, args)
}

// objectSchema is the shape every tool's parameters take.
func objectSchema(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func stringParam(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intParam(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
