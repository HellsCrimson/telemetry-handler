package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrLLMUnavailable marks a failure to reach or use the model, so callers can
// fall back to the deterministic grammar instead of leaving the driver with
// nothing.
var ErrLLMUnavailable = errors.New("engineer model unavailable")

// defaultMaxTokens is headroom, not a brevity control — the system prompt is
// what keeps answers to a sentence or two (a good one measures ~25 tokens). The
// cap only exists to stop a runaway, so it is set well clear of any real reply.
const defaultMaxTokens = 220

// warmTokens is the budget for a keep-alive completion: enough for a word.
const warmTokens = 4

// LLMConfig points at an OpenAI-compatible chat endpoint (Unsloth Studio, vLLM,
// llama.cpp, …).
type LLMConfig struct {
	// BaseURL is the API root, with or without the /v1 suffix.
	BaseURL string
	APIKey  string
	Model   string
	// Timeout bounds a whole completion. Kept short: an engineer that answers
	// after the corner is over is worse than one that says nothing.
	Timeout time.Duration
	// MaxTokens caps the reply length, which is also what keeps answers brief.
	MaxTokens   int
	Temperature float64
}

// chatMessage is one OpenAI-format message.
type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// LLM is a minimal streaming client for the chat-completions API. It is
// deliberately hand-rolled: the app needs exactly one endpoint, streaming, and
// tool calls, and a vendor SDK would be a much larger dependency for that.
type LLM struct {
	cfg  LLMConfig
	http *http.Client
}

func NewLLM(cfg LLMConfig) *LLM {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	return &LLM{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

// completion is the streamed result: prose plus any tool calls the model made.
// Reasoning and FinishReason are kept only to tell "the model had nothing to
// say" apart from "the model never got to the saying part".
type completion struct {
	Text         string
	Calls        []toolCall
	Reasoning    string
	FinishReason string
}

// chatRequest is the wire body.
//
// Reasoning models spend their token budget thinking before they answer, which
// here is fatal rather than merely slow: a 160-token cap was consumed entirely
// by reasoning and the reply came back with empty content and
// finish_reason=length, so the engineer never said anything at all.
//
// Which switch turns it off is a property of the serving stack, not the model,
// and they are not interchangeable. Measured against the live endpoint
// (llama.cpp's llama-server behind Unsloth Studio) on 2026-09-06:
//
//	thinking: {type: disabled}   WORKS — 3 tokens vs 27 on a trivial prompt
//	enable_thinking: false       ignored
//	chat_template_kwargs         ignored
//	"/no_think" in the prompt    ignored (in either position)
//
// All three are still sent because the ignored ones are what vLLM and sglang
// read, and an unknown field costs nothing. Do not "tidy up" the redundancy
// without re-measuring against the endpoint you are actually pointing at —
// dropping the wrong one silently returns the engineer to saying nothing.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature"`
	Tools       []toolSpec    `json:"tools,omitempty"`

	Thinking       *thinkingSpec  `json:"thinking,omitempty"`
	EnableThinking *bool          `json:"enable_thinking,omitempty"`
	ChatTemplate   map[string]any `json:"chat_template_kwargs,omitempty"`
}

// thinkingSpec is the switch llama.cpp honours.
type thinkingSpec struct {
	Type string `json:"type"`
}

type toolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

// streamChunk is one server-sent event from the completions stream.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Reasoning arrives on its own field when thinking is left on.
			Reasoning string `json:"reasoning_content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// stream runs a completion, calling onText with each content delta as it
// arrives. It returns the assembled prose and tool calls.
func (l *LLM) stream(ctx context.Context, msgs []chatMessage, tools []toolSpec, onText func(string)) (completion, error) {
	var out completion
	if strings.TrimSpace(l.cfg.BaseURL) == "" || strings.TrimSpace(l.cfg.Model) == "" {
		return out, fmt.Errorf("%w: base_url and model are required", ErrLLMUnavailable)
	}
	no := false
	body, err := json.Marshal(chatRequest{
		Model:          l.cfg.Model,
		Messages:       msgs,
		Stream:         true,
		MaxTokens:      l.cfg.MaxTokens,
		Temperature:    l.cfg.Temperature,
		Tools:          tools,
		Thinking:       &thinkingSpec{Type: "disabled"},
		EnableThinking: &no,
		ChatTemplate:   map[string]any{"enable_thinking": false},
	})
	if err != nil {
		return out, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.endpoint(), bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if l.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+l.cfg.APIKey)
	}

	resp, err := l.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrLLMUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := bufio.NewReader(resp.Body).Peek(300)
		return out, fmt.Errorf("%w: %s: %s", ErrLLMUnavailable, resp.Status, strings.TrimSpace(string(snippet)))
	}

	calls := map[int]*toolCall{}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // a malformed keepalive should not kill the stream
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				out.FinishReason = choice.FinishReason
			}
			out.Reasoning += choice.Delta.Reasoning
			if text := choice.Delta.Content; text != "" {
				out.Text += text
				if onText != nil {
					onText(text)
				}
			}
			// Tool-call arguments arrive in fragments keyed by index.
			for _, tc := range choice.Delta.ToolCalls {
				c, ok := calls[tc.Index]
				if !ok {
					c = &toolCall{}
					calls[tc.Index] = c
				}
				if tc.ID != "" {
					c.ID = tc.ID
				}
				if tc.Type != "" {
					c.Type = tc.Type
				}
				if tc.Function.Name != "" {
					c.Function.Name = tc.Function.Name
				}
				c.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("%w: reading stream: %v", ErrLLMUnavailable, err)
	}
	for i := range len(calls) {
		if c, ok := calls[i]; ok {
			out.Calls = append(out.Calls, *c)
		}
	}
	// A reply that is all reasoning and no answer is a configuration failure, not
	// a model with nothing to say. Returning it as an empty success is what let
	// this go unnoticed: voice fell through to the grammar and the engineer just
	// seemed quiet. Report it so the caller falls back audibly and the log names
	// the cause.
	if out.Text == "" && len(out.Calls) == 0 && out.Reasoning != "" {
		return out, fmt.Errorf("%w: the model spent its whole %d-token budget reasoning and never answered — "+
			"thinking is still enabled on this endpoint (see chatRequest)", ErrLLMUnavailable, l.cfg.MaxTokens)
	}
	return out, nil
}

// endpoint builds the chat-completions URL, tolerating a base with or without
// the /v1 suffix.
func (l *LLM) endpoint() string {
	base := strings.TrimSuffix(strings.TrimSpace(l.cfg.BaseURL), "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/chat/completions"
}

// Ping checks the endpoint answers and the credentials work, and — because the
// request loads the weights — doubles as the warm-up. A cold llama-server takes
// tens of seconds to map a 27B GGUF, so whoever calls this must allow for that;
// the point is to pay it here rather than on the driver's first question.
//
// It uses its own tiny token budget so a keep-alive never costs a real
// generation, and tolerates an empty answer: liveness is the question, not
// eloquence.
func (l *LLM) Ping(ctx context.Context) error {
	warm := *l
	warm.cfg.MaxTokens = warmTokens
	_, err := warm.stream(ctx, []chatMessage{
		{Role: "user", Content: "Reply with the single word: ready"},
	}, nil, nil)
	if errors.Is(err, ErrLLMUnavailable) && strings.Contains(err.Error(), "reasoning") {
		return nil // it answered, just spent the four tokens thinking
	}
	return err
}
