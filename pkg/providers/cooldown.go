package providers

import (
	"math"
	"sort"
	"sync"
	"time"
)

const (
	defaultFailureWindow = 24 * time.Hour

	// maxRetryAfterCooldown caps the duration we will honour from a server-
	// supplied Retry-After header before falling back to default exponential
	// backoff. A misconfigured upstream could otherwise pin a model for hours.
	maxRetryAfterCooldown = 5 * time.Minute

	// billingInitialCooldown is the duration a billing-marked provider is
	// skipped after its first credits-exhausted signal. Picked short (5 min)
	// rather than the 5-24h schedule used by the original OpenClaw billing
	// backoff: ClawEh is a single-user dev tool and the operator typically
	// tops up within minutes, so a multi-hour cooldown is more disruptive
	// than the repeated 402s it prevents. Use /cooldowns clear (or /retry)
	// for an immediate escape.
	billingInitialCooldown = 5 * time.Minute
)

// CooldownTracker manages per-model cooldown state for the fallback chain.
// Entries are keyed by ModelKey(provider, model) so that a rate-limit on one
// model does not block sibling models hosted by the same provider — notably
// important for OpenRouter, where each model has its own upstream quota.
// Thread-safe via sync.RWMutex. In-memory only (resets on restart).
type CooldownTracker struct {
	mu            sync.RWMutex
	entries       map[string]*cooldownEntry
	failureWindow time.Duration
	nowFunc       func() time.Time // for testing
}

type cooldownEntry struct {
	ErrorCount     int
	FailureCounts  map[FailoverReason]int
	CooldownEnd    time.Time      // standard cooldown expiry
	DisabledUntil  time.Time      // billing-specific disable expiry
	DisabledReason FailoverReason // reason for disable (billing)
	LastFailure    time.Time
}

// NewCooldownTracker creates a tracker with default 24h failure window.
func NewCooldownTracker() *CooldownTracker {
	return &CooldownTracker{
		entries:       make(map[string]*cooldownEntry),
		failureWindow: defaultFailureWindow,
		nowFunc:       time.Now,
	}
}

// MarkFailure records a failure for a model and sets the appropriate cooldown.
// When retryAfter > 0 (i.e. the server returned a Retry-After header) the
// caller's hint is used as the cooldown duration, capped at maxRetryAfterCooldown.
// Otherwise the standard exponential backoff applies.
func (ct *CooldownTracker) MarkFailure(provider, model string, reason FailoverReason, retryAfter time.Duration) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	now := ct.nowFunc()
	key := ModelKey(provider, model)
	entry := ct.getOrCreate(key)

	// 24h failure window reset: if no failure in failureWindow, reset counters.
	if !entry.LastFailure.IsZero() && now.Sub(entry.LastFailure) > ct.failureWindow {
		entry.ErrorCount = 0
		entry.FailureCounts = make(map[FailoverReason]int)
	}

	entry.ErrorCount++
	entry.FailureCounts[reason]++
	entry.LastFailure = now

	if reason == FailoverBilling {
		entry.DisabledUntil = now.Add(billingInitialCooldown)
		entry.DisabledReason = FailoverBilling
		return
	}

	cooldown := calculateStandardCooldown(entry.ErrorCount)
	if retryAfter > 0 {
		if retryAfter > maxRetryAfterCooldown {
			retryAfter = maxRetryAfterCooldown
		}
		cooldown = retryAfter
	}
	entry.CooldownEnd = now.Add(cooldown)
}

// MarkSuccess resets all counters and cooldowns for a model.
func (ct *CooldownTracker) MarkSuccess(provider, model string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	entry := ct.entries[ModelKey(provider, model)]
	if entry == nil {
		return
	}

	entry.ErrorCount = 0
	entry.FailureCounts = make(map[FailoverReason]int)
	entry.CooldownEnd = time.Time{}
	entry.DisabledUntil = time.Time{}
	entry.DisabledReason = ""
}

// IsAvailable returns true if the model is not in cooldown or disabled.
func (ct *CooldownTracker) IsAvailable(provider, model string) bool {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	entry := ct.entries[ModelKey(provider, model)]
	if entry == nil {
		return true
	}

	now := ct.nowFunc()

	// Billing disable takes precedence (longer cooldown).
	if !entry.DisabledUntil.IsZero() && now.Before(entry.DisabledUntil) {
		return false
	}

	// Standard cooldown.
	if !entry.CooldownEnd.IsZero() && now.Before(entry.CooldownEnd) {
		return false
	}

	return true
}

// CooldownRemaining returns how long until the model becomes available.
// Returns 0 if already available.
func (ct *CooldownTracker) CooldownRemaining(provider, model string) time.Duration {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	entry := ct.entries[ModelKey(provider, model)]
	if entry == nil {
		return 0
	}

	now := ct.nowFunc()
	var remaining time.Duration

	if !entry.DisabledUntil.IsZero() && now.Before(entry.DisabledUntil) {
		d := entry.DisabledUntil.Sub(now)
		if d > remaining {
			remaining = d
		}
	}

	if !entry.CooldownEnd.IsZero() && now.Before(entry.CooldownEnd) {
		d := entry.CooldownEnd.Sub(now)
		if d > remaining {
			remaining = d
		}
	}

	return remaining
}

// ErrorCount returns the current error count for a model.
func (ct *CooldownTracker) ErrorCount(provider, model string) int {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	entry := ct.entries[ModelKey(provider, model)]
	if entry == nil {
		return 0
	}
	return entry.ErrorCount
}

// FailureCount returns the failure count for a specific reason on a model.
func (ct *CooldownTracker) FailureCount(provider, model string, reason FailoverReason) int {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	entry := ct.entries[ModelKey(provider, model)]
	if entry == nil {
		return 0
	}
	return entry.FailureCounts[reason]
}

func (ct *CooldownTracker) Reset() {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.entries = make(map[string]*cooldownEntry)
}

// Clear removes any cooldown/disabled state for a single provider+model.
// Used as the per-model escape hatch after the operator tops up billing
// or otherwise resolves the upstream condition.
func (ct *CooldownTracker) Clear(provider, model string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	delete(ct.entries, ModelKey(provider, model))
}

// ClearAll wipes every cooldown entry. Equivalent to Reset; kept as a named
// alias because the command surface ("/cooldowns clear") reads more clearly.
func (ct *CooldownTracker) ClearAll() {
	ct.Reset()
}

// CooldownStatus is a snapshot of a single model's cooldown state. Returned
// by Snapshot in stable (provider/model) order so callers can render it.
type CooldownStatus struct {
	Provider    string
	Model       string
	Reason      FailoverReason
	Since       time.Time
	Until       time.Time
}

// Snapshot returns the list of models that are currently NOT available
// (either in standard cooldown or billing-disabled). Stable order by
// provider/model key. The tracker is process-global by design — callers
// should label the output accordingly.
func (ct *CooldownTracker) Snapshot() []CooldownStatus {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	now := ct.nowFunc()
	keys := make([]string, 0, len(ct.entries))
	for k := range ct.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]CooldownStatus, 0, len(keys))
	for _, k := range keys {
		entry := ct.entries[k]
		if entry == nil {
			continue
		}
		var until time.Time
		var reason FailoverReason
		if !entry.DisabledUntil.IsZero() && now.Before(entry.DisabledUntil) {
			until = entry.DisabledUntil
			reason = entry.DisabledReason
		}
		if !entry.CooldownEnd.IsZero() && now.Before(entry.CooldownEnd) {
			if entry.CooldownEnd.After(until) {
				until = entry.CooldownEnd
			}
			if reason == "" {
				// Best-effort reason: the most common failure recorded.
				reason = dominantReason(entry.FailureCounts)
			}
		}
		if until.IsZero() {
			continue
		}
		provider, model := splitModelKey(k)
		out = append(out, CooldownStatus{
			Provider: provider,
			Model:    model,
			Reason:   reason,
			Since:    entry.LastFailure,
			Until:    until,
		})
	}
	return out
}

func dominantReason(counts map[FailoverReason]int) FailoverReason {
	var top FailoverReason
	max := 0
	for r, n := range counts {
		if n > max {
			max = n
			top = r
		}
	}
	return top
}

func (ct *CooldownTracker) getOrCreate(key string) *cooldownEntry {
	entry := ct.entries[key]
	if entry == nil {
		entry = &cooldownEntry{
			FailureCounts: make(map[FailoverReason]int),
		}
		ct.entries[key] = entry
	}
	return entry
}

// calculateStandardCooldown computes standard exponential backoff.
// Formula from OpenClaw: min(1h, 1min * 5^min(n-1, 3))
//
//	1 error  → 1 min
//	2 errors → 5 min
//	3 errors → 25 min
//	4+ errors → 1 hour (cap)
func calculateStandardCooldown(errorCount int) time.Duration {
	n := max(1, errorCount)
	exp := min(n-1, 3)
	ms := 60_000 * int(math.Pow(5, float64(exp)))
	ms = min(3_600_000, ms) // cap at 1 hour
	return time.Duration(ms) * time.Millisecond
}

