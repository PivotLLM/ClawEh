package agent

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

const anthropic400 = `bad request (400): {"type":"error","error":{"type":"invalid_request_error","message":"thinking.type: adaptive is required for this model."}}`

const openai400 = "API request failed:\n  Status: 400\n  Body:   " +
	`{"error":{"message":"Invalid  value\nfor tools.","type":"invalid_request_error"}}`

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
		{"openai error body", &providers.FailoverError{Status: 400, Wrapped: errors.New(openai400)}, "Invalid value for tools"},
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

// secretBodies are provider error bodies whose message carries a credential;
// the key is the secret that must never reach a chat or the log.
var secretBodies = map[string]string{
	"sk-proj-Ab12Cd34Ef56Gh78Ij90":                 `{"error":{"message":"Incorrect API key provided: sk-proj-Ab12Cd34Ef56Gh78Ij90."}}`,
	"sk-ant-api03-Zx9Yw8Vu7Ts6Rq5Po4":              `{"type":"error","error":{"message":"invalid x-api-key sk-ant-api03-Zx9Yw8Vu7Ts6Rq5Po4"}}`,
	"xai-Qw3Er4Ty5Ui6Op7As8":                       `{"error":"bad key xai-Qw3Er4Ty5Ui6Op7As8"}`,
	"gsk_Lk2Jh3Gf4Ds5Ap6Mn7":                       `{"message":"key gsk_Lk2Jh3Gf4Ds5Ap6Mn7 revoked"}`,
	"AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q":      `{"error":{"message":"API key not valid: AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q"}}`,
	"eyJhbGciOiJIUzI1NiJ9.payloadPart":             `{"error":{"message":"rejected Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payloadPart"}}`,
	"YWxpY2U6c2VjcmV0cGFzc3dvcmQ=":                 `{"error":{"message":"rejected Basic YWxpY2U6c2VjcmV0cGFzc3dvcmQ="}}`,
	"hunter2secret":                                `{"error":{"message":"GET /v1?api_key=hunter2secret failed"}}`,
	"tok-short-9":                                  `{"error":{"message":"bad token=tok-short-9 in request"}}`,
	"plainkey":                                     `{"error":{"message":"bad key=plainkey"}}`,
	"0123456789abcdef0123456789abcdef":             `{"error":{"message":"session 0123456789abcdef0123456789abcdef expired"}}`,
	"QWxpY2VBbmRCb2JTaGFyZWRTZWNyZXQxMjM0NTY3OA==": `{"error":{"message":"signature QWxpY2VBbmRCb2JTaGFyZWRTZWNyZXQxMjM0NTY3OA== mismatch"}}`,
}

func TestProviderMessageRedactsSecrets(t *testing.T) {
	for secret, body := range secretBodies {
		t.Run(secret, func(t *testing.T) {
			fe := &providers.FailoverError{
				Reason: providers.FailoverAuth, Provider: "Anthropic", Model: "claude-opus-5-5",
				Status: 401, Wrapped: errors.New("unauthorized (401): " + body),
			}
			msg := providerMessage(fe)
			if msg == "" || strings.Contains(msg, secret) || !strings.Contains(msg, secretRedaction) {
				t.Fatalf("providerMessage = %q, want the message with %q redacted", msg, secret)
			}
			if reply := renderTurnError(context.Background(), time.Minute, fe); strings.Contains(reply, secret) {
				t.Fatalf("reply carries the secret: %q", reply)
			}
			exhausted := &providers.FallbackExhaustedError{Attempts: []providers.FallbackAttempt{
				{Provider: "Anthropic", Model: "claude-opus-5-5", Reason: providers.FailoverAuth, Error: fe},
			}}
			if reply := renderFailoverError(exhausted); strings.Contains(reply, secret) {
				t.Fatalf("exhausted reply carries the secret: %q", reply)
			}
			notice := formatFallbackNotice([]providers.FallbackAttempt{
				{Provider: "Anthropic", Model: "claude-opus-5-5", Reason: providers.FailoverAuth, Error: fe},
			}, providers.FallbackCandidate{Model: "gpt-4o"})
			if notice == "" || strings.Contains(notice, secret) {
				t.Fatalf("fallback notice = %q, must not carry the secret", notice)
			}

			var buf bytes.Buffer
			restore := logger.RedirectForTest(&buf)
			defer restore()
			turn := &llmTurn{agent: &AgentInstance{ID: "alice"}}
			turn.logFallbackAttempts(&providers.FallbackResult{
				Provider: "OpenAI", Model: "gpt-4o",
				Attempts: []providers.FallbackAttempt{
					{Provider: "Anthropic", Model: "claude-opus-5-5", Reason: providers.FailoverAuth, Error: fe},
					{Provider: "Anthropic", Model: "claude-fable-5-1", Reason: providers.FailoverAuth, Skipped: true, Error: fe},
				},
			})
			if strings.Contains(buf.String(), secret) || !strings.Contains(buf.String(), secretRedaction) {
				t.Fatalf("log carries the secret or no redaction:\n%s", buf.String())
			}
		})
	}
}

func TestRedactSecretsLeavesOrdinaryText(t *testing.T) {
	for _, s := range []string{
		"thinking.type: adaptive is required for this model.",
		"Invalid value for tools.",
		"basic validation failed for field messages",
		"The model `claude-opus-5-5` does not exist or you do not have access to it",
		"Please check your key and try again",
		"task-manager reached its limit",
		strings.Repeat("x", 300),
		"",
	} {
		if got := redactSecrets(s); got != s {
			t.Errorf("redactSecrets(%q) = %q, want it unchanged", s, got)
		}
	}
}
