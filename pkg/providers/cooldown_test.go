package providers

import (
	"sync"
	"testing"
	"time"
)

const testModel = "m"

func newTestTracker(now time.Time) (*CooldownTracker, *time.Time) {
	current := now
	ct := NewCooldownTracker()
	ct.nowFunc = func() time.Time { return current }
	return ct, &current
}

// statusFor maps a reason to a representative HTTP status for tests.
func statusFor(r FailoverReason) int {
	switch r {
	case FailoverBilling:
		return 402
	case FailoverAuth:
		return 401
	case FailoverRateLimit:
		return 429
	case FailoverContextLimit:
		return 413
	default:
		return 500
	}
}

// mark records a failure with the representative status and no Retry-After.
func mark(ct *CooldownTracker, provider, model string, reason FailoverReason) {
	ct.MarkFailure(provider, model, reason, statusFor(reason), 0)
}

func TestCooldown_InitiallyAvailable(t *testing.T) {
	ct := NewCooldownTracker()
	if !ct.IsAvailable("openai", testModel) {
		t.Error("new model should be available")
	}
	if ct.ErrorCount("openai", testModel) != 0 {
		t.Error("new model should have 0 errors")
	}
}

// TestCooldown_Escalation verifies the 1/3/5-minute escalation on the first
// three consecutive failures, then the per-category cooldown (rate-limit = 10m).
func TestCooldown_Escalation(t *testing.T) {
	now := time.Now()
	ct, current := newTestTracker(now)

	steps := []time.Duration{1 * time.Minute, 3 * time.Minute, 5 * time.Minute, 10 * time.Minute}
	for i, want := range steps {
		mark(ct, "openai", testModel, FailoverRateLimit)
		if got := ct.CooldownRemaining("openai", testModel); got != want {
			t.Fatalf("failure %d: cooldown = %v, want %v", i+1, got, want)
		}
		// Advance past this cooldown so the model is available for the next mark
		// (ErrorCount persists until success or the failure window elapses).
		*current = current.Add(want + time.Second)
	}
}

// TestCooldown_BillingReachesCategory verifies billing escalates 1/3/5 then
// settles at the long billing/auth cooldown (default 60m).
func TestCooldown_BillingReachesCategory(t *testing.T) {
	now := time.Now()
	ct, current := newTestTracker(now)
	for i := 0; i < 3; i++ {
		mark(ct, "openai", testModel, FailoverBilling)
		*current = current.Add(10 * time.Minute)
	}
	mark(ct, "openai", testModel, FailoverBilling) // 4th → category
	if got := ct.CooldownRemaining("openai", testModel); got != 60*time.Minute {
		t.Fatalf("billing settled cooldown = %v, want 60m", got)
	}
}

// TestCooldown_ContextLimitNeverCools verifies a 413 neither cools the model nor
// counts toward escalation (it is fixed by compaction, not by waiting).
func TestCooldown_ContextLimitNeverCools(t *testing.T) {
	ct := NewCooldownTracker()
	ct.MarkFailure("openai", testModel, FailoverContextLimit, 413, 0)
	if !ct.IsAvailable("openai", testModel) {
		t.Error("413 should not put the model in cooldown")
	}
	if ct.ErrorCount("openai", testModel) != 0 {
		t.Errorf("413 should not count toward escalation; count = %d", ct.ErrorCount("openai", testModel))
	}
}

// TestCooldown_NoStatusNeverCools verifies a network/timeout error with no HTTP
// status does not cool the model.
func TestCooldown_NoStatusNeverCools(t *testing.T) {
	ct := NewCooldownTracker()
	ct.MarkFailure("openai", testModel, FailoverTimeout, 0, 0)
	if !ct.IsAvailable("openai", testModel) {
		t.Error("a status-less error should not cool the model")
	}
	if ct.ErrorCount("openai", testModel) != 0 {
		t.Error("a status-less error should not count toward escalation")
	}
}

// TestCooldown_RetryAfterHonored verifies a server Retry-After sets the cooldown
// window (capped at maxRetryAfterCooldown), overriding the escalation step.
func TestCooldown_RetryAfterHonored(t *testing.T) {
	ct, _ := newTestTracker(time.Now())
	// First failure would be 1m by escalation; the 2m Retry-After is used instead.
	ct.MarkFailure("openai", testModel, FailoverRateLimit, 429, 2*time.Minute)
	if got := ct.CooldownRemaining("openai", testModel); got != 2*time.Minute {
		t.Fatalf("cooldown with Retry-After = %v, want 2m", got)
	}
}

func TestCooldown_ClearPerModel(t *testing.T) {
	ct := NewCooldownTracker()
	mark(ct, "openai", testModel, FailoverBilling)
	mark(ct, "anthropic", "claude", FailoverRateLimit)

	if ct.IsAvailable("openai", testModel) {
		t.Fatal("openai expected in cooldown")
	}
	ct.Clear("openai", testModel)
	if !ct.IsAvailable("openai", testModel) {
		t.Error("openai should be available after Clear")
	}
	if ct.IsAvailable("anthropic", "claude") {
		t.Error("anthropic/claude should still be in cooldown")
	}
}

func TestCooldown_ClearAll(t *testing.T) {
	ct := NewCooldownTracker()
	mark(ct, "openai", testModel, FailoverBilling)
	mark(ct, "anthropic", "claude", FailoverRateLimit)
	ct.ClearAll()
	if !ct.IsAvailable("openai", testModel) || !ct.IsAvailable("anthropic", "claude") {
		t.Error("all models should be available after ClearAll")
	}
}

func TestCooldown_Snapshot(t *testing.T) {
	now := time.Now()
	ct, _ := newTestTracker(now)

	if got := ct.Snapshot(); len(got) != 0 {
		t.Fatalf("empty tracker: got %d entries, want 0", len(got))
	}

	mark(ct, "openai", "gpt-4", FailoverBilling)
	mark(ct, "anthropic", "claude", FailoverRateLimit)

	snap := ct.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	// Stable sort by provider/model key — anthropic/claude < openai/gpt-4.
	if snap[0].Provider != "anthropic" || snap[0].Reason != FailoverRateLimit {
		t.Errorf("snap[0] = %+v", snap[0])
	}
	if snap[1].Provider != "openai" || snap[1].Reason != FailoverBilling {
		t.Errorf("snap[1] = %+v", snap[1])
	}
}

func TestCooldown_SuccessReset(t *testing.T) {
	ct := NewCooldownTracker()

	mark(ct, "openai", testModel, FailoverRateLimit)
	mark(ct, "openai", testModel, FailoverBilling)
	if ct.ErrorCount("openai", testModel) != 2 {
		t.Errorf("error count = %d, want 2", ct.ErrorCount("openai", testModel))
	}

	ct.MarkSuccess("openai", testModel)
	if ct.ErrorCount("openai", testModel) != 0 {
		t.Errorf("error count after success = %d, want 0", ct.ErrorCount("openai", testModel))
	}
	if !ct.IsAvailable("openai", testModel) {
		t.Error("should be available after success")
	}
	if ct.FailureCount("openai", testModel, FailoverRateLimit) != 0 {
		t.Error("failure counts should be reset after success")
	}
}

func TestCooldown_FailureWindowReset(t *testing.T) {
	now := time.Now()
	ct, current := newTestTracker(now)

	for range 4 {
		mark(ct, "openai", testModel, FailoverRateLimit)
		*current = current.Add(2 * time.Second)
	}
	if ct.ErrorCount("openai", testModel) != 4 {
		t.Errorf("error count = %d, want 4", ct.ErrorCount("openai", testModel))
	}

	// Advance past the 24h failure window; the next failure resets then counts 1.
	*current = now.Add(25 * time.Hour)
	mark(ct, "openai", testModel, FailoverRateLimit)
	if ct.ErrorCount("openai", testModel) != 1 {
		t.Errorf("error count after window reset = %d, want 1", ct.ErrorCount("openai", testModel))
	}
}

func TestCooldown_PerReasonTracking(t *testing.T) {
	ct := NewCooldownTracker()

	mark(ct, "openai", testModel, FailoverRateLimit)
	mark(ct, "openai", testModel, FailoverRateLimit)
	mark(ct, "openai", testModel, FailoverBilling)
	mark(ct, "openai", testModel, FailoverAuth)

	if ct.FailureCount("openai", testModel, FailoverRateLimit) != 2 {
		t.Errorf("rate_limit count = %d, want 2", ct.FailureCount("openai", testModel, FailoverRateLimit))
	}
	if ct.FailureCount("openai", testModel, FailoverBilling) != 1 {
		t.Errorf("billing count = %d, want 1", ct.FailureCount("openai", testModel, FailoverBilling))
	}
	if ct.ErrorCount("openai", testModel) != 4 {
		t.Errorf("total error count = %d, want 4", ct.ErrorCount("openai", testModel))
	}
}

func TestCooldown_CooldownRemaining(t *testing.T) {
	now := time.Now()
	ct, current := newTestTracker(now)

	if ct.CooldownRemaining("openai", testModel) != 0 {
		t.Error("expected 0 remaining for new model")
	}

	mark(ct, "openai", testModel, FailoverRateLimit) // 1st failure → 1m
	*current = now.Add(30 * time.Second)
	remaining := ct.CooldownRemaining("openai", testModel)
	if remaining <= 0 || remaining > 1*time.Minute {
		t.Errorf("remaining = %v, expected ~30s", remaining)
	}
}

func TestCooldown_SuccessOnUnknownProvider(t *testing.T) {
	ct := NewCooldownTracker()
	ct.MarkSuccess("nonexistent", testModel) // must not panic
	if !ct.IsAvailable("nonexistent", testModel) {
		t.Error("nonexistent model should be available")
	}
}

func TestCooldown_ConcurrentAccess(t *testing.T) {
	ct := NewCooldownTracker()
	var wg sync.WaitGroup

	for range 100 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			mark(ct, "openai", testModel, FailoverRateLimit)
		}()
		go func() {
			defer wg.Done()
			ct.IsAvailable("openai", testModel)
		}()
		go func() {
			defer wg.Done()
			ct.MarkSuccess("openai", testModel)
		}()
	}
	wg.Wait()
}

func TestCooldown_MultipleModels(t *testing.T) {
	ct := NewCooldownTracker()

	mark(ct, "openai", "gpt-4", FailoverRateLimit)
	mark(ct, "anthropic", "claude-opus", FailoverBilling)

	if ct.IsAvailable("openai", "gpt-4") {
		t.Error("openai/gpt-4 should be in cooldown")
	}
	if ct.IsAvailable("anthropic", "claude-opus") {
		t.Error("anthropic/claude-opus should be in cooldown")
	}
	if !ct.IsAvailable("openai", "gpt-3.5") {
		t.Error("openai/gpt-3.5 should be available (per-model cooldown)")
	}
	if !ct.IsAvailable("groq", testModel) {
		t.Error("groq/m should be available")
	}
}

// TestCooldownPolicy_Categories spot-checks the status→duration mapping.
func TestCooldownPolicy_Categories(t *testing.T) {
	p := DefaultCooldownPolicy()
	cases := map[int]time.Duration{
		401: 60 * time.Minute,
		402: 60 * time.Minute,
		403: 60 * time.Minute,
		429: 10 * time.Minute,
		400: 1 * time.Minute,
		404: 10 * time.Minute,
		500: 10 * time.Minute,
		503: 10 * time.Minute,
		413: 0,
		0:   0,
	}
	for status, want := range cases {
		if got := p.CategoryCooldown(status); got != want {
			t.Errorf("CategoryCooldown(%d) = %v, want %v", status, got, want)
		}
	}
}
