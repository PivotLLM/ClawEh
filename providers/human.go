// ClawEh
// License: MIT

package providers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// HumanRelay reaches the person a human agent represents: it posts request
// to the person's chat and returns their next message from it. timeout (0 =
// none) runs from when the request is posted, not from when it queued behind
// another; when it passes the relay returns context.DeadlineExceeded. It
// returns ctx's error when ctx ends first.
type HumanRelay func(ctx context.Context, request string, timeout time.Duration) (answer string, err error)

type humanRelayKey struct{}

// WithHumanRelay attaches the relay a human provider answers through. The
// agent loop sets it for a human agent's turn; without it the provider
// refuses, so a human model can never be called from anywhere else.
func WithHumanRelay(ctx context.Context, relay HumanRelay) context.Context {
	return context.WithValue(ctx, humanRelayKey{}, relay)
}

func humanRelayFrom(ctx context.Context) HumanRelay {
	if r, ok := ctx.Value(humanRelayKey{}).(HumanRelay); ok {
		return r
	}
	return nil
}

// ErrHumanUnreachable is returned by a human provider called without a relay:
// outside a human agent's own turn there is no person to ask.
var ErrHumanUnreachable = errors.New("this model represents a person and is answered only in its own agent's turns")

// HumanProvider is the provider of the human protocol. It runs no model: the
// turn's latest user message (the request, with any sender header it carries)
// is posted to the person through the relay, and the person's answer is the
// reply. When timeout passes first the reply is empty.
type HumanProvider struct {
	label   string
	timeout time.Duration
}

// NewHumanProvider builds the provider for the human model label; timeout is
// how long to wait for the person (0 waits as long as the turn does).
func NewHumanProvider(label string, timeout time.Duration) *HumanProvider {
	return &HumanProvider{label: label, timeout: timeout}
}

// GetDefaultModel implements LLMProvider.
func (p *HumanProvider) GetDefaultModel() string { return p.label }

// Timeout is how long the provider waits for the person; 0 means no limit of
// its own.
func (p *HumanProvider) Timeout() time.Duration { return p.timeout }

// Chat implements LLMProvider. Tools, the model id and the options are
// ignored: the person sees only the request.
func (p *HumanProvider) Chat(ctx context.Context, messages []Message, _ []ToolDefinition, _ string, _ map[string]any) (*LLMResponse, error) {
	relay := humanRelayFrom(ctx)
	if relay == nil {
		return nil, fmt.Errorf("model %q: %w", p.label, ErrHumanUnreachable)
	}
	answer, err := relay(ctx, latestUserMessage(messages), p.timeout)
	if err != nil {
		// The person did not answer in time: an empty reply, not a failure.
		// The turn's own end (cancel, shutdown, turn budget) stays an error.
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return &LLMResponse{FinishReason: "stop"}, nil
		}
		return nil, err
	}
	return &LLMResponse{Content: answer, FinishReason: "stop"}, nil
}

// latestUserMessage is the content of the last user message, or "" when there
// is none. The system prompt and the history are never shown to the person.
func latestUserMessage(messages []Message) string {
	for _, m := range slices.Backward(messages) {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}
