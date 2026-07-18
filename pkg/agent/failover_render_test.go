package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

func TestAttemptDescriptionIncludesHTTPStatus(t *testing.T) {
	// With a status code, it must be surfaced verbatim (technical users).
	got := attemptDescription("grok-2", 402, providers.FailoverBilling)
	if got != "grok-2 error HTTP 402 (out of credits)" {
		t.Fatalf("got %q", got)
	}
	// Without a status (timeout/network), fall back to the reason — no "HTTP 0".
	got = attemptDescription("gpt-4o", 0, providers.FailoverTimeout)
	if got != "gpt-4o error: timeout" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderFailoverErrorExhausted(t *testing.T) {
	err := &providers.FallbackExhaustedError{Attempts: []providers.FallbackAttempt{
		{Provider: "x", Model: "grok-2", Reason: providers.FailoverBilling,
			Error: &providers.FailoverError{Reason: providers.FailoverBilling, Status: 402}},
		{Provider: "y", Model: "gpt-4o", Reason: providers.FailoverOverloaded,
			Error: &providers.FailoverError{Reason: providers.FailoverOverloaded, Status: 529}},
	}}
	out := renderFailoverError(err)
	if !strings.Contains(out, "HTTP 402") || !strings.Contains(out, "grok-2") {
		t.Fatalf("exhausted render missing http code/model: %q", out)
	}
}

func TestRenderFailoverErrorSkipsCooldownOnly(t *testing.T) {
	// All attempts skipped (cooldown) → nothing to render; caller falls through.
	err := &providers.FallbackExhaustedError{Attempts: []providers.FallbackAttempt{
		{Provider: "x", Model: "grok-2", Skipped: true},
	}}
	if out := renderFailoverError(err); out != "" {
		t.Fatalf("expected empty render for all-skipped, got %q", out)
	}
}

func TestRenderTurnErrorTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done() // ensure deadline elapsed
	out := renderTurnError(ctx, 15*time.Minute, errors.New("context deadline exceeded"))
	if !strings.Contains(out, "time limit") || !strings.Contains(out, "15m") {
		t.Fatalf("turn-timeout message unexpected: %q", out)
	}
}

func TestFormatFallbackNotice(t *testing.T) {
	failed := providers.FallbackAttempt{
		Model:  "grok-2",
		Reason: providers.FailoverBilling,
		Error:  &providers.FailoverError{Reason: providers.FailoverBilling, Status: 402},
	}
	next := providers.FallbackCandidate{Model: "gpt-4o", Alias: "smart"}
	got := formatFallbackNotice(failed, next)
	// Alias preferred for the next model; HTTP code surfaced for the failed one;
	// period + newline before "Trying".
	if !strings.Contains(got, "HTTP 402") || !strings.Contains(got, ").\nTrying smart…") {
		t.Fatalf("notice unexpected: %q", got)
	}
}

// A cooldown-skipped candidate renders a "skipped (in cooldown)" heads-up (using
// its alias) rather than an HTTP-error line, so the skip is never silent.
func TestFormatFallbackNotice_Skip(t *testing.T) {
	skipped := providers.FallbackAttempt{
		Model:   "deepseek-v4-pro",
		Alias:   "DeepSeek V4 Pro Writing",
		Skipped: true,
		Reason:  providers.FailoverRateLimit,
	}
	next := providers.FallbackCandidate{Model: "abliterated-model", Alias: "Abliteration"}
	got := formatFallbackNotice(skipped, next)
	if !strings.Contains(got, "DeepSeek V4 Pro Writing skipped (in cooldown)") ||
		!strings.Contains(got, "Trying Abliteration…") {
		t.Fatalf("skip notice unexpected: %q", got)
	}
}
