package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/providers/common"
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
