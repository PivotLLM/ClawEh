package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/providers/common"
	"github.com/PivotLLM/ClawEh/pkg/providers/openai_compat"
)

// TestFallback_SameProviderFallbackNotCooldownSkipped is the regression test
// for the OpenRouter 429 cascade: when the primary model returns 429 and the
// configured fallback uses the SAME provider (openrouter/openrouter/auto),
// the fallback must still be tried — provider-level cooldown previously
// blocked it. Per-model cooldown lets the sibling model through.
func TestFallback_SameProviderFallbackNotCooldownSkipped(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{
		makeCandidate("openrouter", "meta-llama/llama-4-scout"),
		makeCandidate("openrouter", "openrouter/auto"),
	}

	calls := []string{}
	run := func(ctx context.Context, c FallbackCandidate) (*LLMResponse, error) {
		calls = append(calls, c.Model)
		if c.Model == "meta-llama/llama-4-scout" {
			return nil, &common.HTTPStatusError{
				StatusCode:  429,
				BodyPreview: `{"error":"rate_limited"}`,
				RetryAfter:  2 * time.Second,
			}
		}
		return &LLMResponse{Content: "fallback ok", FinishReason: "stop"}, nil
	}

	start := time.Now()
	result, err := fc.Execute(context.Background(), candidates, run)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected fallback to succeed, got %v", err)
	}
	if result.Model != "openrouter/auto" {
		t.Fatalf("expected fallback model openrouter/auto, got %q", result.Model)
	}
	// Sanity: same-model retry consumed the 2s Retry-After once, then fallback
	// took over. Combined wall time should be at least the Retry-After hint
	// (the fallback should NOT have been skipped due to cooldown).
	if elapsed < time.Second {
		t.Logf("note: elapsed=%v — same-model retry may not have waited", elapsed)
	}
	if len(calls) < 2 {
		t.Fatalf("expected at least 2 calls (retry + fallback), got %d: %v", len(calls), calls)
	}
	skipped := 0
	for _, a := range result.Attempts {
		if a.Skipped {
			skipped++
		}
	}
	if skipped != 0 {
		t.Errorf("fallback candidate should not be skipped due to cooldown; got %d skipped attempts", skipped)
	}
}

// TestFallback_RetryAfterRespectedInCooldown verifies that the cooldown
// tracker uses the Retry-After hint to size the cooldown window instead of
// applying the default 1-minute exponential backoff.
func TestFallback_RetryAfterRespectedInCooldown(t *testing.T) {
	now := time.Now()
	ct, current := newTestTracker(now)

	ct.MarkFailure("openrouter", "scout", FailoverRateLimit, 3*time.Second)
	if ct.IsAvailable("openrouter", "scout") {
		t.Fatal("expected scout to be in cooldown immediately after failure")
	}

	*current = now.Add(2 * time.Second)
	if ct.IsAvailable("openrouter", "scout") {
		t.Fatal("expected scout still in cooldown 2s after a 3s Retry-After")
	}

	*current = now.Add(4 * time.Second)
	if !ct.IsAvailable("openrouter", "scout") {
		t.Fatal("expected scout available 4s after a 3s Retry-After (default 1m backoff would still block)")
	}
}

// TestFallback_RetryAfterCappedAtFiveMinutes guards against a misbehaving
// upstream pinning a model for hours with a giant Retry-After.
func TestFallback_RetryAfterCappedAtFiveMinutes(t *testing.T) {
	now := time.Now()
	ct, current := newTestTracker(now)

	ct.MarkFailure("openrouter", "scout", FailoverRateLimit, 24*time.Hour)
	*current = now.Add(5*time.Minute + time.Second)
	if !ct.IsAvailable("openrouter", "scout") {
		t.Fatal("expected scout available 5m1s after a 24h Retry-After hint (cap is 5m)")
	}
}

// TestClassifyError_TransientStatusCodes ensures the OpenRouter-common
// transient statuses (504, 520, 525-528) classify as retriable timeouts.
func TestClassifyError_TransientStatusCodes(t *testing.T) {
	codes := []int{504, 520, 525, 526, 527, 528}
	for _, status := range codes {
		err := &common.HTTPStatusError{StatusCode: status, BodyPreview: "x"}
		fe := ClassifyError(err, "openrouter", "scout")
		if fe == nil {
			t.Errorf("status %d: expected classified error, got nil", status)
			continue
		}
		if fe.Reason != FailoverTimeout {
			t.Errorf("status %d: reason = %q, want %q", status, fe.Reason, FailoverTimeout)
		}
		if !fe.IsRetriable() {
			t.Errorf("status %d: should be retriable", status)
		}
	}
}

// TestClassifyError_ParseErrorTriggersFallback proves that a malformed
// upstream response classifies as a retriable error so the chain moves to
// the next configured candidate instead of stopping.
func TestClassifyError_ParseErrorTriggersFallback(t *testing.T) {
	parseErrors := []string{
		"failed to parse JSON response: unexpected EOF",
		"failed to decode response: invalid character '<' looking for beginning of value",
		"failed to inspect response: read: broken pipe",
	}
	for _, msg := range parseErrors {
		fe := ClassifyError(errors.New(msg), "openrouter", "scout")
		if fe == nil {
			t.Errorf("parse error %q: expected classified, got nil", msg)
			continue
		}
		if !fe.IsRetriable() {
			t.Errorf("parse error %q: should be retriable", msg)
		}
	}
}

// TestFallback_ParseErrorTriggersFallback exercises the chain end-to-end:
// a parse error on the primary must hand off to the next candidate.
func TestFallback_ParseErrorTriggersFallback(t *testing.T) {
	ct := NewCooldownTracker()
	fc := NewFallbackChain(ct)

	candidates := []FallbackCandidate{
		makeCandidate("openrouter", "scout"),
		makeCandidate("openrouter", "auto"),
	}

	run := func(ctx context.Context, c FallbackCandidate) (*LLMResponse, error) {
		if c.Model == "scout" {
			return nil, errors.New("failed to parse JSON response: unexpected EOF")
		}
		return &LLMResponse{Content: "ok", FinishReason: "stop"}, nil
	}

	result, err := fc.Execute(context.Background(), candidates, run)
	if err != nil {
		t.Fatalf("expected fallback success, got %v", err)
	}
	if result.Model != "auto" {
		t.Errorf("expected fallback to auto, got %q", result.Model)
	}
}

// TestCommon_ParseRetryAfter covers the header parser used by openai_compat.
func TestCommon_ParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if got := common.ParseRetryAfter("", now); got != 0 {
		t.Errorf("empty: got %v, want 0", got)
	}
	if got := common.ParseRetryAfter("2", now); got != 2*time.Second {
		t.Errorf("delta-seconds: got %v, want 2s", got)
	}
	if got := common.ParseRetryAfter("0.5", now); got != 500*time.Millisecond {
		t.Errorf("fractional: got %v, want 500ms", got)
	}
	// HTTP-date 5s in the future.
	future := now.Add(5 * time.Second).UTC().Format(http.TimeFormat)
	if got := common.ParseRetryAfter(future, now); got <= 0 || got > 6*time.Second {
		t.Errorf("http-date: got %v, want ~5s", got)
	}
	// HTTP-date in the past → 0
	past := now.Add(-1 * time.Hour).UTC().Format(http.TimeFormat)
	if got := common.ParseRetryAfter(past, now); got != 0 {
		t.Errorf("past date: got %v, want 0", got)
	}
	if got := common.ParseRetryAfter("not-a-date", now); got != 0 {
		t.Errorf("garbage: got %v, want 0", got)
	}
}

// TestOpenAICompat_RetryAfterPropagated ensures the openai_compat provider
// surfaces the server's Retry-After header through HTTPStatusError so the
// fallback chain can size its cooldown to the upstream's hint.
func TestOpenAICompat_RetryAfterPropagated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintln(w, `{"error":"rate_limited"}`)
	}))
	defer server.Close()

	p := openai_compat.NewProvider("key", server.URL, "")
	_, err := p.Chat(context.Background(), nil, nil, "scout", nil)
	if err == nil {
		t.Fatal("expected error from 429")
	}
	var status *common.HTTPStatusError
	if !errors.As(err, &status) {
		t.Fatalf("expected *common.HTTPStatusError, got %T: %v", err, err)
	}
	if status.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", status.StatusCode)
	}
	if status.RetryAfter <= 0 || status.RetryAfter > 3*time.Second {
		t.Errorf("retry-after = %v, want ~2s", status.RetryAfter)
	}
	if !strings.Contains(status.Error(), "429") {
		t.Errorf("error string should mention 429: %s", status.Error())
	}
}
