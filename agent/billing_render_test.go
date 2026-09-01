package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/providers"
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

// realOpenRouter402Body is the verbatim shape OpenRouter returned during the
// 2026-08-31 credit exhaustion (key id and user id scrubbed). It carries NO
// billing_url — the actionable link lives in error.metadata.remedy_hint, which
// is why the rendered notice used to have no "Top up:" line at all.
const realOpenRouter402Body = `{"error":{"message":"This request requires more credits, or fewer max_tokens. You requested up to 32768 tokens, but can only afford 5142. To increase, visit https://openrouter.ai/workspaces/default/keys/KEYID and adjust the key's daily limit","code":402,"metadata":{"limit_source":"openrouter_credits","remedy_hint":"Add credits at https://openrouter.ai/settings/credits, or lower max_tokens / prompt size to fit your remaining balance.","provider_name":null,"previous_errors":[]}},"user_id":"user_X"}`

func TestSniffBillingURL_OpenRouterRemedyHint(t *testing.T) {
	got := sniffBillingURL(realOpenRouter402Body)
	want := "https://openrouter.ai/settings/credits"
	if got != want {
		t.Fatalf("got %q, want %q (trailing comma must be trimmed)", got, want)
	}
}

// The same body truncated mid-document (as common.HandleErrorResponse delivers
// it) must still yield the hint via the raw scan — this is the shape the
// incident actually produced, where the cut landed well past remedy_hint.
func TestSniffBillingURL_OpenRouterTruncated(t *testing.T) {
	cut := strings.Index(realOpenRouter402Body, `"provider_name"`) + 10
	truncated := realOpenRouter402Body[:cut]
	if json.Valid([]byte(truncated)) {
		t.Fatal("fixture is meant to be invalid JSON")
	}
	got := sniffBillingURL(truncated)
	want := "https://openrouter.ai/settings/credits"
	if got != want {
		t.Fatalf("truncated body: got %q, want %q", got, want)
	}
}

// A cut landing INSIDE the hint never yields half a URL: the hint is discarded
// (the billing_url scan's long-standing contract) and the sniff degrades to the
// complete URL in error.message, which sits earlier in the body.
func TestSniffBillingURL_TruncatedInsideRemedyHint(t *testing.T) {
	cut := strings.Index(realOpenRouter402Body, "settings/credits") + 8
	truncated := realOpenRouter402Body[:cut]
	got := sniffBillingURL(truncated)
	want := "https://openrouter.ai/workspaces/default/keys/KEYID"
	if got != want {
		t.Fatalf("got %q, want the message URL %q", got, want)
	}
}

// Both candidate URLs cut off → no link at all, rather than a broken one.
func TestSniffBillingURL_TruncatedBeforeAnyURL(t *testing.T) {
	cut := strings.Index(realOpenRouter402Body, "https://") + 20
	if got := sniffBillingURL(realOpenRouter402Body[:cut]); got != "" {
		t.Fatalf("got %q, want empty when every URL is cut mid-value", got)
	}
}

// With no remedy hint, fall back to the URL in error.message.
func TestSniffBillingURL_MessageURLFallback(t *testing.T) {
	body := `{"error":{"message":"Out of credits. To increase, visit https://openrouter.ai/workspaces/default/keys/KEYID and adjust the key's daily limit","code":402}}`
	want := "https://openrouter.ai/workspaces/default/keys/KEYID"
	if got := sniffBillingURL(body); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// An explicit billing_url still wins over the hint and the message.
func TestSniffBillingURL_BillingURLWinsOverHint(t *testing.T) {
	body := `{"error":{"billing_url":"https://explicit/top-up","message":"see https://in-message","metadata":{"remedy_hint":"Add credits at https://in-hint, or lower max_tokens."}}}`
	if got := sniffBillingURL(body); got != "https://explicit/top-up" {
		t.Fatalf("got %q, want the explicit billing_url", got)
	}
}

// A message with no URL at all yields nothing (no "Top up:" line).
func TestSniffBillingURL_MessageWithoutURL(t *testing.T) {
	body := `{"error":{"message":"insufficient quota","code":402}}`
	if got := sniffBillingURL(body); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// End-to-end: the incident's error now renders with an actionable link.
func TestRenderBillingError_OpenRouterEndToEnd(t *testing.T) {
	err := &providers.FailoverError{
		Reason:   providers.FailoverBilling,
		Provider: "OpenRouter Chat DS",
		Model:    "deepseek/deepseek-v4-flash",
		Wrapped:  &common.HTTPStatusError{StatusCode: 402, BodyPreview: realOpenRouter402Body},
	}
	got := renderBillingError(err)
	want := "Out of credits on OpenRouter Chat DS/deepseek/deepseek-v4-flash. " +
		"Top up: https://openrouter.ai/settings/credits"
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestFirstURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"no url here", ""},
		{"Add credits at https://a/b, or lower max_tokens.", "https://a/b"},
		{"visit https://a/b and adjust", "https://a/b"},
		{"see (http://a/b).", "http://a/b"},
		{`{"u":"https://a/b"}`, "https://a/b"},
		{"trailing period https://a/b.", "https://a/b"},
	}
	for _, c := range cases {
		if got := firstURL(c.in); got != c.want {
			t.Errorf("firstURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValueAfterKey(t *testing.T) {
	cases := []struct{ body, key, want string }{
		{`{"a":"x"}`, "a", "x"},
		{`{"a" : "x"}`, "a", "x"},       // whitespace around the colon
		{`{"a":1}`, "a", ""},            // non-string value
		{`{"b":"x"}`, "a", ""},          // absent
		{`{"a":"trunc`, "a", ""},        // truncated mid-value
		{`{"a":"x","a":"y"}`, "a", "x"}, // first occurrence wins
	}
	for _, c := range cases {
		if got := valueAfterKey(c.body, c.key); got != c.want {
			t.Errorf("valueAfterKey(%q,%q) = %q, want %q", c.body, c.key, got, c.want)
		}
	}
}
