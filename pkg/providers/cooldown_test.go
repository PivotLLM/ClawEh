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

func TestCooldown_InitiallyAvailable(t *testing.T) {
	ct := NewCooldownTracker()
	if !ct.IsAvailable("openai", testModel) {
		t.Error("new model should be available")
	}
	if ct.ErrorCount("openai", testModel) != 0 {
		t.Error("new model should have 0 errors")
	}
}

func TestCooldown_StandardEscalation(t *testing.T) {
	now := time.Now()
	ct, current := newTestTracker(now)

	// 1st error → 1 min cooldown
	ct.MarkFailure("openai", testModel, FailoverRateLimit, 0)
	if ct.IsAvailable("openai", testModel) {
		t.Error("should be in cooldown after 1st error")
	}

	// Advance 61 seconds → available
	*current = now.Add(61 * time.Second)
	if !ct.IsAvailable("openai", testModel) {
		t.Error("should be available after 1 min cooldown")
	}

	// 2nd error → 5 min cooldown
	ct.MarkFailure("openai", testModel, FailoverRateLimit, 0)
	*current = now.Add(61*time.Second + 4*time.Minute)
	if ct.IsAvailable("openai", testModel) {
		t.Error("should be in cooldown (5 min) after 2nd error")
	}
	*current = now.Add(61*time.Second + 6*time.Minute)
	if !ct.IsAvailable("openai", testModel) {
		t.Error("should be available after 5 min cooldown")
	}
}

func TestCooldown_StandardCap(t *testing.T) {
	// Verify formula: 1m, 5m, 25m, 1h, 1h, 1h...
	expected := []time.Duration{
		1 * time.Minute,
		5 * time.Minute,
		25 * time.Minute,
		1 * time.Hour,
		1 * time.Hour,
	}

	for i, want := range expected {
		got := calculateStandardCooldown(i + 1)
		if got != want {
			t.Errorf("calculateStandardCooldown(%d) = %v, want %v", i+1, got, want)
		}
	}
}

func TestCooldown_BillingTerminalForSession(t *testing.T) {
	now := time.Now()
	ct, current := newTestTracker(now)

	// A billing error disables the model for billingInitialCooldown (5m).
	ct.MarkFailure("openai", testModel, FailoverBilling, 0)
	if ct.IsAvailable("openai", testModel) {
		t.Error("should be disabled after billing error")
	}

	// Halfway through the cooldown — still disabled.
	*current = now.Add(2 * time.Minute)
	if ct.IsAvailable("openai", testModel) {
		t.Error("should still be disabled within billingInitialCooldown")
	}

	// Past the cooldown — available again.
	*current = now.Add(billingInitialCooldown + 1*time.Second)
	if !ct.IsAvailable("openai", testModel) {
		t.Error("should be available after billingInitialCooldown elapses")
	}
}

func TestCooldown_ClearPerModel(t *testing.T) {
	ct := NewCooldownTracker()
	ct.MarkFailure("openai", testModel, FailoverBilling, 0)
	ct.MarkFailure("anthropic", "claude", FailoverRateLimit, 0)

	if ct.IsAvailable("openai", testModel) {
		t.Fatal("openai expected disabled")
	}
	ct.Clear("openai", testModel)
	if !ct.IsAvailable("openai", testModel) {
		t.Error("openai should be available after Clear")
	}
	// Anthropic still disabled.
	if ct.IsAvailable("anthropic", "claude") {
		t.Error("anthropic/claude should still be in cooldown")
	}
}

func TestCooldown_ClearAll(t *testing.T) {
	ct := NewCooldownTracker()
	ct.MarkFailure("openai", testModel, FailoverBilling, 0)
	ct.MarkFailure("anthropic", "claude", FailoverRateLimit, 0)
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

	ct.MarkFailure("openai", "gpt-4", FailoverBilling, 0)
	ct.MarkFailure("anthropic", "claude", FailoverRateLimit, 0)

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

	ct.MarkFailure("openai", testModel, FailoverRateLimit, 0)
	ct.MarkFailure("openai", testModel, FailoverBilling, 0)
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
	if ct.FailureCount("openai", testModel, FailoverBilling) != 0 {
		t.Error("billing failure count should be reset after success")
	}
}

func TestCooldown_FailureWindowReset(t *testing.T) {
	now := time.Now()
	ct, current := newTestTracker(now)

	// 4 errors → 1h cooldown
	for range 4 {
		ct.MarkFailure("openai", testModel, FailoverRateLimit, 0)
		*current = current.Add(2 * time.Second) // small advance between errors
	}
	if ct.ErrorCount("openai", testModel) != 4 {
		t.Errorf("error count = %d, want 4", ct.ErrorCount("openai", testModel))
	}

	// Advance 25 hours (past 24h failure window)
	*current = now.Add(25 * time.Hour)

	// Next error should reset counters first, then increment to 1
	ct.MarkFailure("openai", testModel, FailoverRateLimit, 0)
	if ct.ErrorCount("openai", testModel) != 1 {
		t.Errorf("error count after window reset = %d, want 1 (reset + 1)", ct.ErrorCount("openai", testModel))
	}
}

func TestCooldown_PerReasonTracking(t *testing.T) {
	ct := NewCooldownTracker()

	ct.MarkFailure("openai", testModel, FailoverRateLimit, 0)
	ct.MarkFailure("openai", testModel, FailoverRateLimit, 0)
	ct.MarkFailure("openai", testModel, FailoverBilling, 0)
	ct.MarkFailure("openai", testModel, FailoverAuth, 0)

	if ct.FailureCount("openai", testModel, FailoverRateLimit) != 2 {
		t.Errorf("rate_limit count = %d, want 2", ct.FailureCount("openai", testModel, FailoverRateLimit))
	}
	if ct.FailureCount("openai", testModel, FailoverBilling) != 1 {
		t.Errorf("billing count = %d, want 1", ct.FailureCount("openai", testModel, FailoverBilling))
	}
	if ct.FailureCount("openai", testModel, FailoverAuth) != 1 {
		t.Errorf("auth count = %d, want 1", ct.FailureCount("openai", testModel, FailoverAuth))
	}
	if ct.ErrorCount("openai", testModel) != 4 {
		t.Errorf("total error count = %d, want 4", ct.ErrorCount("openai", testModel))
	}
}

func TestCooldown_BillingTakesPrecedence(t *testing.T) {
	now := time.Now()
	ct, current := newTestTracker(now)

	// Standard rate-limit cooldown is 1 min on first failure; billing disable
	// is billingInitialCooldown (5 min). Billing should outlast it.
	ct.MarkFailure("openai", testModel, FailoverRateLimit, 0)
	ct.MarkFailure("openai", testModel, FailoverBilling, 0)

	// After 90 sec: standard cooldown expired but billing still active.
	*current = now.Add(90 * time.Second)
	if ct.IsAvailable("openai", testModel) {
		t.Error("billing disable should take precedence over standard cooldown")
	}

	// After billingInitialCooldown + 1s: both expired.
	*current = now.Add(billingInitialCooldown + 1*time.Second)
	if !ct.IsAvailable("openai", testModel) {
		t.Error("should be available after all cooldowns expire")
	}
}

func TestCooldown_CooldownRemaining(t *testing.T) {
	now := time.Now()
	ct, current := newTestTracker(now)

	// No failures → 0 remaining
	if ct.CooldownRemaining("openai", testModel) != 0 {
		t.Error("expected 0 remaining for new model")
	}

	ct.MarkFailure("openai", testModel, FailoverRateLimit, 0)

	*current = now.Add(30 * time.Second)
	remaining := ct.CooldownRemaining("openai", testModel)
	if remaining <= 0 || remaining > 1*time.Minute {
		t.Errorf("remaining = %v, expected ~30s", remaining)
	}
}

func TestCooldown_SuccessOnUnknownProvider(t *testing.T) {
	ct := NewCooldownTracker()
	// Should not panic
	ct.MarkSuccess("nonexistent", testModel)
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
			ct.MarkFailure("openai", testModel, FailoverRateLimit, 0)
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
	// If we got here without panic, concurrent access is safe
}

func TestCooldown_MultipleModels(t *testing.T) {
	ct := NewCooldownTracker()

	ct.MarkFailure("openai", "gpt-4", FailoverRateLimit, 0)
	ct.MarkFailure("anthropic", "claude-opus", FailoverBilling, 0)

	if ct.IsAvailable("openai", "gpt-4") {
		t.Error("openai/gpt-4 should be in cooldown")
	}
	if ct.IsAvailable("anthropic", "claude-opus") {
		t.Error("anthropic/claude-opus should be in cooldown")
	}
	// untouched models on the same providers stay available
	if !ct.IsAvailable("openai", "gpt-3.5") {
		t.Error("openai/gpt-3.5 should be available (per-model cooldown)")
	}
	if !ct.IsAvailable("groq", testModel) {
		t.Error("groq/m should be available")
	}
}
