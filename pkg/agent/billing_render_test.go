package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/spawnllm/common"
)

func TestRenderBillingError_Nil(t *testing.T) {
	if got := renderBillingError(nil); got != "" {
		t.Errorf("expected empty for nil, got %q", got)
	}
}

func TestRenderBillingError_NonBilling(t *testing.T) {
	err := &providers.FailoverError{
		Reason:   providers.FailoverRateLimit,
		Provider: "openai",
		Model:    "gpt-4",
	}
	if got := renderBillingError(err); got != "" {
		t.Errorf("expected empty for non-billing, got %q", got)
	}
}

func TestRenderBillingError_SingleAttempt_WithURL(t *testing.T) {
	body := `{"error":{"code":"credits_exhausted","message":"out","billing_url":"https://openrouter.ai/credits"}}`
	wrapped := &common.HTTPStatusError{StatusCode: 402, BodyPreview: body}
	err := &providers.FailoverError{
		Reason:   providers.FailoverBilling,
		Provider: "openrouter",
		Model:    "auto",
		Wrapped:  wrapped,
	}

	got := renderBillingError(err)
	if !strings.Contains(got, "Out of credits on openrouter/auto") {
		t.Errorf("missing provider line: %q", got)
	}
	if !strings.Contains(got, "https://openrouter.ai/credits") {
		t.Errorf("missing billing URL: %q", got)
	}
}

func TestRenderBillingError_SingleAttempt_NoURL(t *testing.T) {
	wrapped := &common.HTTPStatusError{
		StatusCode:  429,
		BodyPreview: `{"error":{"code":"insufficient_quota"}}`,
	}
	err := &providers.FailoverError{
		Reason:   providers.FailoverBilling,
		Provider: "openai",
		Model:    "gpt-4",
		Wrapped:  wrapped,
	}

	got := renderBillingError(err)
	if !strings.Contains(got, "Out of credits on openai/gpt-4") {
		t.Errorf("missing provider line: %q", got)
	}
	if strings.Contains(got, "Top up:") {
		t.Errorf("unexpected URL line when no billing_url present: %q", got)
	}
}

func TestRenderBillingError_FallbackExhausted_MultipleBilling(t *testing.T) {
	a1Body := `{"billing_url":"https://openrouter.ai/credits"}`
	a2Body := `{"error":{"billing_url":"https://platform.openai.com/billing"}}`

	exhausted := &providers.FallbackExhaustedError{
		Attempts: []providers.FallbackAttempt{
			{
				Provider: "openrouter",
				Model:    "auto",
				Reason:   providers.FailoverBilling,
				Error: &providers.FailoverError{
					Reason:  providers.FailoverBilling,
					Wrapped: &common.HTTPStatusError{StatusCode: 402, BodyPreview: a1Body},
				},
			},
			{
				Provider: "openai",
				Model:    "gpt-4",
				Reason:   providers.FailoverBilling,
				Error: &providers.FailoverError{
					Reason:  providers.FailoverBilling,
					Wrapped: &common.HTTPStatusError{StatusCode: 429, BodyPreview: a2Body},
				},
			},
		},
	}

	got := renderBillingError(exhausted)
	if !strings.Contains(got, "openrouter/auto") || !strings.Contains(got, "openai/gpt-4") {
		t.Errorf("missing provider lines: %q", got)
	}
	if !strings.Contains(got, "https://openrouter.ai/credits") ||
		!strings.Contains(got, "https://platform.openai.com/billing") {
		t.Errorf("missing URLs: %q", got)
	}
}

func TestRenderBillingError_FallbackExhausted_NoBillingAttempts(t *testing.T) {
	// Only rate-limit attempts — must fall through (returns "").
	exhausted := &providers.FallbackExhaustedError{
		Attempts: []providers.FallbackAttempt{
			{Provider: "a", Model: "m", Reason: providers.FailoverRateLimit},
		},
	}
	if got := renderBillingError(exhausted); got != "" {
		t.Errorf("expected empty for non-billing exhausted, got %q", got)
	}
}

func TestSniffBillingURL_Variants(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "top-level field",
			body: `{"billing_url":"https://a/b"}`,
			want: "https://a/b",
		},
		{
			name: "nested in error object",
			body: `{"error":{"billing_url":"https://nested"}}`,
			want: "https://nested",
		},
		{
			name: "truncated JSON, fallback scan",
			body: `{"error":{"code":"credits_exhausted","billing_url":"https://truncated.com/x`,
			want: "",
		},
		{
			name: "truncated JSON with closing quote on URL",
			body: `{"error":{"code":"credits_exhausted","billing_url":"https://truncated.com/x","mor`,
			want: "https://truncated.com/x",
		},
		{
			name: "no billing url",
			body: `{"error":{"code":"insufficient_quota"}}`,
			want: "",
		},
		{
			name: "empty",
			body: "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sniffBillingURL(tc.body)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderBillingError_PlainError(t *testing.T) {
	// A non-FailoverError, non-FallbackExhaustedError must fall through.
	if got := renderBillingError(errors.New("network is unreachable")); got != "" {
		t.Errorf("expected empty for plain error, got %q", got)
	}
}

// TestRenderBillingError_LabelUsesFailureRecordProvider is the regression
// test for the billing_render.go defect flagged by QA worker 721b2394: the
// rendered row must reflect the provider recorded on the failure record we
// have in hand (FailoverError.Provider / FallbackAttempt.Provider), not a
// hardcoded or shared value. Mutation evidence: change the
// `Provider: a.Provider` / `Provider: fe.Provider` assignments in
// billing_render.go to a hardcoded string (e.g. "claude-cli", representing
// the shared agent.Provider's protocol on the default config) and this test
// fails because the rendered label no longer matches the per-row record.
func TestRenderBillingError_LabelUsesFailureRecordProvider(t *testing.T) {
	// Use a deliberately distinctive provider name so a mutation to a
	// hardcoded label is obvious. The shared-provider concern QA raised would
	// have surfaced as e.g. "claude-cli" appearing in the rendered text
	// regardless of which actual provider returned the 402.
	const sentinelProvider = "openrouter-distinctive-9c4f"

	// Single-attempt FailoverError path.
	{
		wrapped := &common.HTTPStatusError{
			StatusCode:  402,
			BodyPreview: `{"error":{"code":"credits_exhausted"}}`,
		}
		err := &providers.FailoverError{
			Reason:   providers.FailoverBilling,
			Provider: sentinelProvider,
			Model:    "auto",
			Wrapped:  wrapped,
		}
		got := renderBillingError(err)
		if !strings.Contains(got, sentinelProvider) {
			t.Errorf("FailoverError path: rendered label %q does not contain failure-record provider %q",
				got, sentinelProvider)
		}
	}

	// Multi-attempt FallbackExhaustedError path: each row must carry its
	// own attempt-recorded provider, not a shared label.
	{
		attemptProviderA := sentinelProvider + "-A"
		attemptProviderB := sentinelProvider + "-B"
		exhausted := &providers.FallbackExhaustedError{
			Attempts: []providers.FallbackAttempt{
				{
					Provider: attemptProviderA,
					Model:    "model-a",
					Reason:   providers.FailoverBilling,
					Error: &providers.FailoverError{
						Reason:  providers.FailoverBilling,
						Wrapped: &common.HTTPStatusError{StatusCode: 402, BodyPreview: "{}"},
					},
				},
				{
					Provider: attemptProviderB,
					Model:    "model-b",
					Reason:   providers.FailoverBilling,
					Error: &providers.FailoverError{
						Reason:  providers.FailoverBilling,
						Wrapped: &common.HTTPStatusError{StatusCode: 402, BodyPreview: "{}"},
					},
				},
			},
		}
		got := renderBillingError(exhausted)
		if !strings.Contains(got, attemptProviderA) {
			t.Errorf("FallbackExhaustedError: missing per-attempt provider %q in %q",
				attemptProviderA, got)
		}
		if !strings.Contains(got, attemptProviderB) {
			t.Errorf("FallbackExhaustedError: missing per-attempt provider %q in %q",
				attemptProviderB, got)
		}
	}
}
