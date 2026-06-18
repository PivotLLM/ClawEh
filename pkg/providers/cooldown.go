package providers

import (
	"sort"
	"sync"
	"time"
)

const (
	defaultFailureWindow = 24 * time.Hour

	// maxRetryAfterCooldown caps the duration we will honour from a server-
	// supplied Retry-After header. A misconfigured upstream could otherwise pin
	// a model for hours.
	maxRetryAfterCooldown = 5 * time.Minute
)

// Escalation steps applied to the first consecutive failures before the
// per-category cooldown takes over: failure 1 → 1m, 2 → 3m, 3 → 5m, 4+ →
// CooldownPolicy.CategoryCooldown(status). A transient blip costs only a short
// retry; a persistently-failing model escalates into the full category lockout.
var cooldownEscalation = []time.Duration{
	1 * time.Minute,
	3 * time.Minute,
	5 * time.Minute,
}

// CooldownPolicy maps an HTTP status to the "settled" cooldown duration reached
// after the escalation steps. A zero duration for a category means "never cool"
// (the model is not taken out of rotation for that status).
type CooldownPolicy struct {
	BillingAuth time.Duration // 401, 402, 403
	RateLimit   time.Duration // 429
	BadRequest  time.Duration // 400
	ClientError time.Duration // other 4xx (404, 408, …)
	ServerError time.Duration // 5xx
}

// DefaultCooldownPolicy is used when no config-derived policy is supplied.
func DefaultCooldownPolicy() CooldownPolicy {
	return CooldownPolicy{
		BillingAuth: 60 * time.Minute,
		RateLimit:   10 * time.Minute,
		BadRequest:  1 * time.Minute,
		ClientError: 10 * time.Minute,
		ServerError: 10 * time.Minute,
	}
}

// CategoryCooldown returns the settled cooldown for an HTTP status, or 0 when
// the status must never cool: 413 (context-too-large, fixed by compaction) and
// any non-error/unknown status (e.g. a network error with no HTTP code).
func (p CooldownPolicy) CategoryCooldown(status int) time.Duration {
	switch {
	case status == 413:
		return 0
	case status == 401 || status == 402 || status == 403:
		return p.BillingAuth
	case status == 429:
		return p.RateLimit
	case status == 400:
		return p.BadRequest
	case status >= 400 && status < 500:
		return p.ClientError
	case status >= 500 && status < 600:
		return p.ServerError
	default:
		return 0
	}
}

// cooldownForFailure returns the cooldown for the n-th consecutive failure of a
// model that failed with the given status: the 1/3/5-minute escalation for the
// first three, then the category cooldown. Returns 0 when the status never cools.
func (p CooldownPolicy) cooldownForFailure(errorCount, status int) time.Duration {
	cat := p.CategoryCooldown(status)
	if cat == 0 {
		return 0
	}
	// Billing/auth (401/402/403) won't recover in minutes — skip the escalation
	// and park the model for the full category cooldown on the FIRST failure, so
	// an out-of-credits model isn't retried every 1–5 minutes.
	if status == 401 || status == 402 || status == 403 {
		return cat
	}
	if errorCount >= 1 && errorCount <= len(cooldownEscalation) {
		return cooldownEscalation[errorCount-1]
	}
	return cat
}

// CooldownTracker manages per-model cooldown state for the fallback chain.
// Entries are keyed by ModelKey(provider, model) so that a rate-limit on one
// model does not block sibling models hosted by the same provider — notably
// important for OpenRouter, where each model has its own upstream quota.
// Thread-safe via sync.RWMutex. In-memory only (resets on restart).
type CooldownTracker struct {
	mu            sync.RWMutex
	entries       map[string]*cooldownEntry
	failureWindow time.Duration
	policy        CooldownPolicy
	nowFunc       func() time.Time // for testing
}

type cooldownEntry struct {
	ErrorCount    int
	FailureCounts map[FailoverReason]int
	CooldownEnd   time.Time      // current cooldown expiry
	LastReason    FailoverReason // most recent failure reason (for display)
	LastFailure   time.Time
}

// NewCooldownTracker creates a tracker with the default policy and 24h window.
func NewCooldownTracker() *CooldownTracker {
	return NewCooldownTrackerWithPolicy(DefaultCooldownPolicy())
}

// NewCooldownTrackerWithPolicy creates a tracker driven by the given policy.
func NewCooldownTrackerWithPolicy(policy CooldownPolicy) *CooldownTracker {
	return &CooldownTracker{
		entries:       make(map[string]*cooldownEntry),
		failureWindow: defaultFailureWindow,
		policy:        policy,
		nowFunc:       time.Now,
	}
}

// MarkFailure records a failure for a model and sets the appropriate cooldown
// from the policy: the 1/3/5-minute escalation for the first three consecutive
// failures, then the per-category cooldown for the HTTP status. A status the
// policy never cools (413, or no HTTP status) is ignored entirely — it neither
// cools the model nor counts toward escalation. When retryAfter > 0 (server
// Retry-After) it is honoured as a floor, capped at maxRetryAfterCooldown.
func (ct *CooldownTracker) MarkFailure(provider, model string, reason FailoverReason, status int, retryAfter time.Duration) {
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

	// Statuses the policy never cools (413, network/no-status) do not count.
	if ct.policy.CategoryCooldown(status) == 0 {
		return
	}

	entry.ErrorCount++
	entry.FailureCounts[reason]++
	entry.LastReason = reason
	entry.LastFailure = now

	cooldown := ct.policy.cooldownForFailure(entry.ErrorCount, status)
	// A server-supplied Retry-After is authoritative for when to retry, so it
	// REPLACES the computed cooldown (capped so a misbehaving upstream cannot pin
	// a model for hours).
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
	entry.LastReason = ""
}

// IsAvailable returns true if the model is not currently in cooldown.
func (ct *CooldownTracker) IsAvailable(provider, model string) bool {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	entry := ct.entries[ModelKey(provider, model)]
	if entry == nil {
		return true
	}
	now := ct.nowFunc()
	return entry.CooldownEnd.IsZero() || !now.Before(entry.CooldownEnd)
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
	if !entry.CooldownEnd.IsZero() && now.Before(entry.CooldownEnd) {
		return entry.CooldownEnd.Sub(now)
	}
	return 0
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
// or otherwise resolves the upstream condition. Returns true when an entry
// existed and was removed so the caller can report "no cooldown found"
// informationally.
func (ct *CooldownTracker) Clear(provider, model string) bool {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	key := ModelKey(provider, model)
	if _, ok := ct.entries[key]; !ok {
		return false
	}
	delete(ct.entries, key)
	return true
}

// ClearAll wipes every cooldown entry. Equivalent to Reset; kept as a named
// alias because the command surface ("/cooldowns clear") reads more clearly.
func (ct *CooldownTracker) ClearAll() {
	ct.Reset()
}

// CooldownStatus is a snapshot of a single model's cooldown state. Returned
// by Snapshot in stable (provider/model) order so callers can render it.
type CooldownStatus struct {
	Provider string
	Model    string
	Reason   FailoverReason
	Since    time.Time
	Until    time.Time
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
		if !entry.CooldownEnd.IsZero() && now.Before(entry.CooldownEnd) {
			until = entry.CooldownEnd
			reason = entry.LastReason
			if reason == "" {
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

