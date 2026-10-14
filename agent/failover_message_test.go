package agent

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

const anthropic400 = `bad request (400): {"type":"error","error":{"type":"invalid_request_error","message":"thinking.type: adaptive is required for this model."}}`

func fable400() *providers.FailoverError {
	return &providers.FailoverError{
		Reason: providers.FailoverFormat, Provider: "Anthropic", Model: "claude-fable-5-1",
		Status: 400, Wrapped: errors.New(anthropic400),
	}
}

func TestProviderMessage(t *testing.T) {
	long := strings.Repeat("x", 300)
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"anthropic error body", fable400(), "thinking.type: adaptive is required for this model"},
		{"openai error body", &providers.FailoverError{Status: 400, Wrapped: errors.New(
			"API request failed:\n  Status: 400\n  Body:   {\"error\":{\"message\":\"Invalid  value\\nfor tools.\",\"type\":\"invalid_request_error\"}}")},
			"Invalid value for tools"},
		{"string error", &providers.FailoverError{Wrapped: errors.New(`failed: {"error":"model not found"}`)}, "model not found"},
		{"top-level message", &providers.FailoverError{Wrapped: errors.New(`{"message":"quota"}`)}, "quota"},
		{"cut-short body", &providers.FailoverError{Wrapped: errors.New(`bad request (400): {"error":{"message":"too long","details":"abc`)}, "too long"},
		{"long message", &providers.FailoverError{Wrapped: errors.New(`{"message":"` + long + `"}`)}, strings.Repeat("x", 200) + "…"},
		{"no body", &providers.FailoverError{Status: 500, Wrapped: errors.New("internal server error")}, ""},
		{"no wrapped error", &providers.FailoverError{Status: 402}, ""},
		{"not a failover error", errors.New(anthropic400), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providerMessage(tt.err); got != tt.want {
				t.Fatalf("providerMessage = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderFailoverErrorShowsProviderMessage(t *testing.T) {
	const want = "claude-fable-5-1 error HTTP 400 (bad response format): thinking.type: adaptive is required for this model."

	if got := renderFailoverError(fable400()); got != want {
		t.Fatalf("single render = %q, want %q", got, want)
	}

	exhausted := &providers.FallbackExhaustedError{Attempts: []providers.FallbackAttempt{
		{Provider: "Anthropic", Model: "claude-fable-5-1", Reason: providers.FailoverFormat, Error: fable400()},
	}}
	if got := renderFailoverError(exhausted); got != "All models failed. "+want {
		t.Fatalf("exhausted render = %q", got)
	}

	notice := formatFallbackNotice([]providers.FallbackAttempt{
		{Provider: "Anthropic", Model: "claude-fable-5-1", Alias: "Claude Fable 5.1", Reason: providers.FailoverFormat, Error: fable400()},
	}, providers.FallbackCandidate{Model: "claude-opus-5-5", Alias: "Claude Opus 5.5"})
	if !strings.Contains(notice, "Claude Fable 5.1 error HTTP 400 (bad response format): thinking.type: adaptive is required for this model.") {
		t.Fatalf("fallback notice = %q, want the provider's message", notice)
	}
}

func TestLogFallbackAttemptsLogsProviderMessage(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	turn := &llmTurn{agent: &AgentInstance{ID: "alice"}}
	turn.logFallbackAttempts(&providers.FallbackResult{
		Provider: "Anthropic", Model: "claude-opus-5-5",
		Attempts: []providers.FallbackAttempt{
			{Provider: "Anthropic", Model: "claude-fable-5-1", Reason: providers.FailoverFormat, Error: fable400()},
		},
	})
	if !strings.Contains(buf.String(), "thinking.type: adaptive is required for this model.") {
		t.Fatalf("log does not carry the provider's message:\n%s", buf.String())
	}
}
