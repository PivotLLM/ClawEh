package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/providers"
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
		{
			Provider: "x", Model: "grok-2", Reason: providers.FailoverBilling,
			Error: &providers.FailoverError{Reason: providers.FailoverBilling, Status: 402},
		},
		{
			Provider: "y", Model: "gpt-4o", Reason: providers.FailoverOverloaded,
			Error: &providers.FailoverError{Reason: providers.FailoverOverloaded, Status: 529},
		},
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
	got := formatFallbackNotice([]providers.FallbackAttempt{failed}, next)
	// Alias preferred for the next model; HTTP code surfaced for the failed one;
	// period + newline before "Trying".
	if !strings.Contains(got, "HTTP 402") || !strings.Contains(got, ").\nTrying smart…") {
		t.Fatalf("notice unexpected: %q", got)
	}
}

// A single notifier de-duplicates identical notices across a turn: a primary that
// fails over the same way on every tool iteration posts its heads-up once.
func TestFallbackNotifier_DedupsAcrossTurn(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	notifier := al.fallbackNotifier(processOptions{Channel: "test", ChatID: "chat"})
	if notifier == nil {
		t.Fatal("expected a notifier")
	}

	collected := make(chan bus.OutboundMessage, 16)
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	go func() {
		for {
			msg, ok := msgBus.SubscribeOutbound(subCtx)
			if !ok {
				return
			}
			collected <- msg
		}
	}()

	failA := providers.FallbackAttempt{
		Model: "deepseek-v4-pro", Alias: "DeepSeek 4 Pro",
		Reason: providers.FailoverFormat,
		Error:  &providers.FailoverError{Reason: providers.FailoverFormat, Status: 400},
	}
	nextW := providers.FallbackCandidate{Model: "deepseek-v4-pro", Alias: "DeepSeek V4 Pro Writing"}
	failB := providers.FallbackAttempt{
		Model: "grok", Alias: "Grok",
		Reason: providers.FailoverRateLimit,
		Error:  &providers.FailoverError{Reason: providers.FailoverRateLimit, Status: 429},
	}

	batchA := []providers.FallbackAttempt{failA}
	batchB := []providers.FallbackAttempt{failB}
	notifier(batchA, nextW) // published
	notifier(batchA, nextW) // duplicate → suppressed
	notifier(batchA, nextW) // duplicate → suppressed
	notifier(batchB, nextW) // distinct → published

	var seen []bus.OutboundMessage
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case m := <-collected:
			seen = append(seen, m)
		case <-deadline:
			t.Fatalf("timed out; got %d notices, want 2", len(seen))
		}
	}
	// No third notice — the duplicates must have been suppressed.
	select {
	case m := <-collected:
		t.Fatalf("dedup failed; got an extra notice: %q", m.Content)
	case <-time.After(200 * time.Millisecond):
	}
}

// A single cooldown-skipped candidate names the CAUSE and the retry time (using
// its alias), so the skip is neither silent nor uninformative.
func TestFormatFallbackNotice_Skip(t *testing.T) {
	skipped := providers.FallbackAttempt{
		Model:     "deepseek-v4-pro",
		Alias:     "DeepSeek V4 Pro Writing",
		Skipped:   true,
		Reason:    providers.FailoverRateLimit,
		Remaining: 9 * time.Minute,
	}
	next := providers.FallbackCandidate{Model: "abliterated-model", Alias: "Abliteration"}
	got := formatFallbackNotice([]providers.FallbackAttempt{skipped}, next)
	want := "⚠️ DeepSeek V4 Pro Writing unavailable — rate limited (retry in 9m). Using Abliteration."
	if got != want {
		t.Fatalf("skip notice:\n got %q\nwant %q", got, want)
	}
}

// A run of skips collapses to ONE line that names the cause — the fix for a
// wall of "skipped (in cooldown)" on every turn while OpenRouter is out of
// credits.
func TestFormatFallbackNotice_SkipBatchCoalesced(t *testing.T) {
	skips := []providers.FallbackAttempt{
		{
			Model: "deepseek-v4-flash", Alias: "OR Chat DeepSeek V4 Flash", Skipped: true,
			Reason: providers.FailoverBilling, Remaining: 41 * time.Minute,
		},
		{
			Model: "deepseek-v4-pro", Alias: "OR Chat DeepSeek V4 Pro", Skipped: true,
			Reason: providers.FailoverBilling, Remaining: 58 * time.Minute,
		},
		{
			Model: "glm-5.2", Alias: "OR GLM 5.2", Skipped: true,
			Reason: providers.FailoverBilling, Remaining: 12 * time.Minute,
		},
	}
	next := providers.FallbackCandidate{Model: "grok-medium", Alias: "Grok Medium"}
	got := formatFallbackNotice(skips, next)
	want := "⚠️ 3 models unavailable — out of credits (retry in up to 58m). Using Grok Medium."
	if got != want {
		t.Fatalf("batch skip notice:\n got %q\nwant %q", got, want)
	}
}

// Mixed causes across a batch are listed once each, in first-seen order.
func TestFormatFallbackNotice_SkipBatchMixedReasons(t *testing.T) {
	skips := []providers.FallbackAttempt{
		{Model: "a", Skipped: true, Reason: providers.FailoverBilling, Remaining: time.Minute},
		{Model: "b", Skipped: true, Reason: providers.FailoverRateLimit, Remaining: 2 * time.Minute},
		{Model: "c", Skipped: true, Reason: providers.FailoverBilling, Remaining: time.Minute},
	}
	got := formatFallbackNotice(skips, providers.FallbackCandidate{Model: "grok"})
	want := "⚠️ 3 models unavailable — out of credits, rate limited (retry in up to 2m). Using grok."
	if got != want {
		t.Fatalf("mixed-reason notice:\n got %q\nwant %q", got, want)
	}
}

// With no recorded reason (and no remaining), the notice degrades to the plain
// "in cooldown" wording rather than inventing a cause.
func TestFormatFallbackNotice_SkipUnknownReason(t *testing.T) {
	skips := []providers.FallbackAttempt{{Model: "a", Skipped: true}}
	got := formatFallbackNotice(skips, providers.FallbackCandidate{Model: "grok"})
	want := "⚠️ a unavailable — in cooldown. Using grok."
	if got != want {
		t.Fatalf("unknown-reason notice:\n got %q\nwant %q", got, want)
	}
}

func TestFormatFallbackNotice_Empty(t *testing.T) {
	if got := formatFallbackNotice(nil, providers.FallbackCandidate{Model: "grok"}); got != "" {
		t.Fatalf("empty batch = %q, want empty", got)
	}
}

func TestFormatRemaining(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, ""},
		{-time.Minute, ""},
		{30 * time.Second, "30s"},
		{90 * time.Second, "2m"},
		{42 * time.Minute, "42m"},
		{time.Hour, "60m"},
	}
	for _, c := range cases {
		if got := formatRemaining(c.in); got != c.want {
			t.Errorf("formatRemaining(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
