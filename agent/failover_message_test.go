package agent

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ctxengine"

	"github.com/PivotLLM/ClawEh/internal/clock"
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
	"abcd1234efgh5678ijkl":                         `{"error":{"message":"Incorrect API key provided: abcd1234efgh5678ijkl. You can find your API key at https://platform.openai.com/account/api-keys."}}`,
	"AKIAABCDEFGHIJKLMNOP":                         `{"message":"The security token AKIAABCDEFGHIJKLMNOP is invalid"}`,
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

// Each case's text carries the secret in one of the forms providers and
// proxies use; the secret must not survive and the rest of the text must.
func TestRedactSecretsForms(t *testing.T) {
	for _, tc := range []struct{ text, secret, keep string }{
		{"Incorrect API key provided: abcd1234efgh5678ijkl", "abcd1234efgh5678ijkl", "Incorrect API key provided: "},
		{"header x-api-key: abcd1234efgh5678 rejected", "abcd1234efgh5678", "header x-api-key: "},
		{`{"api_key": "abcd1234efgh", "model": "gpt-4o"}`, "abcd1234efgh", `"model": "gpt-4o"`},
		{`{"apikey":"abcd1234efgh"}`, "abcd1234efgh", `{"apikey":"`},
		{"GET /v1/models?key=AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q", "AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q", "GET /v1/models?key="},
		{"client_secret=s3cr3tValue99 rejected", "s3cr3tValue99", "client_secret="},
		{"auth_token: tok3nValue42", "tok3nValue42", "auth_token: "},
		{"password=Passw0rdAlice", "Passw0rdAlice", "password="},
		{"access key AKIAABCDEFGHIJKLMNOP denied", "AKIAABCDEFGHIJKLMNOP", "access key "},
		{"Authorization: Bearer abcdefghijklmnop1234", "abcdefghijklmnop1234", "Authorization: Bearer "},
		{"Authorization: Basic YWxpY2U6c2VjcmV0cGFzcw==", "YWxpY2U6c2VjcmV0cGFzcw==", "Authorization: Basic "},
		{"key sk-ant-api03-Zx9Yw8Vu7Ts6Rq5Po4 revoked", "sk-ant-api03-Zx9Yw8Vu7Ts6Rq5Po4", " revoked"},
		{"session 0123456789abcdef0123456789abcdef expired", "0123456789abcdef0123456789abcdef", "session "},
	} {
		got := redactSecrets(tc.text)
		if strings.Contains(got, tc.secret) || !strings.Contains(got, secretRedaction) || !strings.Contains(got, tc.keep) {
			t.Errorf("redactSecrets(%q) = %q, want %q redacted and %q kept", tc.text, got, tc.secret, tc.keep)
		}
	}
}

func TestRedactSecretsLeavesOrdinaryText(t *testing.T) {
	for _, s := range []string{
		"model anthropic/claude-3-5-sonnet-20241022 is overloaded",
		"qwen/qwen3-235b-a22b-thinking-2507 does not support tools",
		"deepseek/deepseek-r1-distill-llama-70b: context length exceeded",
		"request 3f2b8c1e-9a4d-4e7f-b6c2-1d0e5a7f9b3c failed",
		"request_id req_0123456789abcdef0123456789abcdef",
		"duplicate value violates primary key=user_id constraint",
		"Invalid token: expired",
		"password: required",
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

// providerErrorWithKey is a provider error whose text carries an API key, as
// an unclassified error or one a provider echoes back.
const leakedKey = "sk-proj-Ab12Cd34Ef56Gh78Ij90"

func providerErrorWithKey() error {
	return errors.New(`API request failed: Status: 401 Body: {"error":{"message":"Incorrect API key provided: ` + leakedKey + `"}}`)
}

// Every log line that carries a provider's error redacts its secrets: the
// dispatch that failed for good, the timeout retry and the compression chain.
func TestProviderErrorLogsRedactSecrets(t *testing.T) {
	assertRedacted := func(t *testing.T, buf *bytes.Buffer) {
		t.Helper()
		if strings.Contains(buf.String(), leakedKey) || !strings.Contains(buf.String(), secretRedaction) {
			t.Fatalf("log carries the key or no redaction:\n%s", buf.String())
		}
	}

	t.Run("LLM call failed", func(t *testing.T) {
		var buf bytes.Buffer
		restore := logger.RedirectForTest(&buf)
		defer restore()
		turn := &llmTurn{agent: &AgentInstance{ID: "alice"}, model: "gpt-4o"}
		if err := turn.dispatchFailed(context.Background(), providerErrorWithKey()); err == nil {
			t.Fatal("dispatchFailed returned nil")
		}
		assertRedacted(t, &buf)
	})

	t.Run("timeout retry", func(t *testing.T) {
		var buf bytes.Buffer
		restore := logger.RedirectForTest(&buf)
		defer restore()
		clk := clock.NewFake(time.Unix(0, 0))
		done := make(chan error, 1)
		go func() { done <- waitToRetry(context.Background(), clk, providerErrorWithKey(), 0) }()
		if err := clk.BlockUntil(context.Background(), 1); err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Hour)
		if err := <-done; err != nil {
			t.Fatalf("waitToRetry: %v", err)
		}
		assertRedacted(t, &buf)
	})

	t.Run("compression model", func(t *testing.T) {
		var buf bytes.Buffer
		restore := logger.RedirectForTest(&buf)
		defer restore()
		c := &compressModelCaller{agentID: "alice", clients: []*providerLLMClient{
			compressClient(&scriptedProvider{err: providerErrorWithKey()}, "p1", "model-a"),
		}}
		if _, err := c.Complete(context.Background(), ctxengine.ModelRequest{System: "s", User: "u"}); err == nil {
			t.Fatal("Complete succeeded with a failing model")
		}
		assertRedacted(t, &buf)
	})
}

// An error with no failover classification is shown as is, minus its secrets.
func TestRenderTurnErrorRedactsUnclassifiedError(t *testing.T) {
	got := renderTurnError(context.Background(), time.Minute, providerErrorWithKey())
	if strings.Contains(got, leakedKey) || !strings.HasPrefix(got, "Error processing message: API request failed") {
		t.Fatalf("reply = %q, want the error without the key", got)
	}
}
