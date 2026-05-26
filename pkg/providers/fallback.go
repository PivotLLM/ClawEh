package providers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// maxSameModelRetryWait bounds how long a same-model retry is allowed to
// sleep on a server-supplied Retry-After hint. Longer hints fall through to
// the next candidate immediately (the cooldown still records the hint so the
// model is skipped on later attempts until it elapses).
const maxSameModelRetryWait = 3 * time.Second

// FallbackChain orchestrates model fallback across multiple candidates.
type FallbackChain struct {
	cooldown *CooldownTracker
}

// FallbackCandidate represents one model/provider to try.
type FallbackCandidate struct {
	Provider string
	Model    string
}

// FallbackResult contains the successful response and metadata about all attempts.
type FallbackResult struct {
	Response *LLMResponse
	Provider string
	Model    string
	Attempts []FallbackAttempt
}

// FallbackAttempt records one attempt in the fallback chain.
type FallbackAttempt struct {
	Provider string
	Model    string
	Error    error
	Reason   FailoverReason
	Duration time.Duration
	Skipped  bool // true if skipped due to cooldown
}

// NewFallbackChain creates a new fallback chain with the given cooldown tracker.
func NewFallbackChain(cooldown *CooldownTracker) *FallbackChain {
	return &FallbackChain{cooldown: cooldown}
}

func (fc *FallbackChain) Reset() {
	if fc.cooldown != nil {
		fc.cooldown.Reset()
	}
}

// Clear removes the cooldown entry for a single provider/model. Returns true
// when an entry actually existed. The per-model counterpart to Reset.
func (fc *FallbackChain) Clear(provider, model string) bool {
	if fc.cooldown == nil {
		return false
	}
	return fc.cooldown.Clear(provider, model)
}

// CooldownSnapshot returns the cooldown tracker's current view of blocked
// models. Surfaced via /cooldowns and /status; the slice is empty when no
// model is in cooldown or when the chain has no tracker.
func (fc *FallbackChain) CooldownSnapshot() []CooldownStatus {
	if fc.cooldown == nil {
		return nil
	}
	return fc.cooldown.Snapshot()
}

// ResolveCandidates parses model config into a deduplicated candidate list.
func ResolveCandidates(cfg ModelConfig, defaultProvider string) []FallbackCandidate {
	return ResolveCandidatesWithLookup(cfg, defaultProvider, nil)
}

func ResolveCandidatesWithLookup(
	cfg ModelConfig,
	defaultProvider string,
	lookup func(raw string) (resolved string, ok bool),
) []FallbackCandidate {
	seen := make(map[string]bool)
	var candidates []FallbackCandidate

	addCandidate := func(raw string) {
		original := strings.TrimSpace(raw)
		if original == "" {
			return
		}
		candidateRaw := original
		resolvedByLookup := false
		if lookup != nil {
			if resolved, ok := lookup(candidateRaw); ok {
				candidateRaw = resolved
				resolvedByLookup = true
			}
		}

		ref := ParseModelRef(candidateRaw, defaultProvider)
		if ref == nil {
			// Drop the candidate silently in the return value but log it: a
			// non-empty alias that fails to resolve usually means it isn't
			// enabled in model_list, and the operator otherwise sees a
			// shortened fallback chain with no explanation.
			logger.WarnCF("providers", "fallback alias dropped (not enabled in model_list)",
				map[string]any{
					"alias":              original,
					"resolved_by_lookup": resolvedByLookup,
					"resolved":           candidateRaw,
				})
			return
		}
		key := ModelKey(ref.Provider, ref.Model)
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, FallbackCandidate{
			Provider: ref.Provider,
			Model:    ref.Model,
		})
	}

	// Primary first.
	addCandidate(cfg.Primary)

	// Then fallbacks.
	for _, fb := range cfg.Fallbacks {
		addCandidate(fb)
	}

	return candidates
}

// Execute runs the fallback chain for text/chat requests.
// It tries each candidate in order, respecting cooldowns and error classification.
//
// Behavior:
//   - Candidates in cooldown are skipped (logged as skipped attempt).
//   - context.Canceled aborts immediately (user abort, no fallback).
//   - Non-retriable errors (format) abort immediately.
//   - Retriable errors trigger fallback to next candidate.
//   - Success marks provider as good (resets cooldown).
//   - If all fail, returns aggregate error with all attempts.
func (fc *FallbackChain) Execute(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(ctx context.Context, provider, model string) (*LLMResponse, error),
) (*FallbackResult, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("fallback: no candidates configured")
	}

	result := &FallbackResult{
		Attempts: make([]FallbackAttempt, 0, len(candidates)),
	}

	for i, candidate := range candidates {
		// Check context before each attempt.
		if ctx.Err() == context.Canceled {
			return nil, context.Canceled
		}

		// Check cooldown (per provider+model).
		if !fc.cooldown.IsAvailable(candidate.Provider, candidate.Model) {
			remaining := fc.cooldown.CooldownRemaining(candidate.Provider, candidate.Model)
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Skipped:  true,
				Reason:   FailoverRateLimit,
				Error: fmt.Errorf(
					"%s/%s in cooldown (%s remaining)",
					candidate.Provider,
					candidate.Model,
					remaining.Round(time.Second),
				),
			})
			continue
		}

		// Execute the run function with one bounded same-model retry on a
		// short Retry-After hint.
		var (
			resp     *LLMResponse
			err      error
			failErr  *FailoverError
			elapsed  time.Duration
			attempts int
		)
		for {
			attempts++
			start := time.Now()
			resp, err = run(ctx, candidate.Provider, candidate.Model)
			elapsed = time.Since(start)

			if err == nil {
				fc.cooldown.MarkSuccess(candidate.Provider, candidate.Model)
				result.Response = resp
				result.Provider = candidate.Provider
				result.Model = candidate.Model
				return result, nil
			}

			if ctx.Err() == context.Canceled {
				result.Attempts = append(result.Attempts, FallbackAttempt{
					Provider: candidate.Provider,
					Model:    candidate.Model,
					Error:    err,
					Duration: elapsed,
				})
				return nil, context.Canceled
			}

			failErr = ClassifyError(err, candidate.Provider, candidate.Model)

			if failErr == nil {
				// Unclassifiable error: do not fallback, return immediately.
				result.Attempts = append(result.Attempts, FallbackAttempt{
					Provider: candidate.Provider,
					Model:    candidate.Model,
					Error:    err,
					Duration: elapsed,
				})
				return nil, fmt.Errorf("fallback: unclassified error from %s/%s: %w",
					candidate.Provider, candidate.Model, err)
			}

			if !failErr.IsRetriable() {
				result.Attempts = append(result.Attempts, FallbackAttempt{
					Provider: candidate.Provider,
					Model:    candidate.Model,
					Error:    failErr,
					Reason:   failErr.Reason,
					Duration: elapsed,
				})
				return nil, failErr
			}

			// Same-model retry: only once, and only when the server explicitly
			// told us how long to wait via Retry-After. The wait is capped at
			// maxSameModelRetryWait so a misbehaving upstream cannot stall the
			// agent. Longer hints fall through to the next candidate (and the
			// cooldown still records the hint for subsequent calls).
			if attempts == 1 && failErr.RetryAfter > 0 && failErr.RetryAfter <= maxSameModelRetryWait {
				select {
				case <-time.After(failErr.RetryAfter):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}
			break
		}

		// Retriable error: mark failure and continue to next candidate.
		// Context-limit and auth are user-fixable without a restart and skip
		// cooldown so the model is retried immediately once the issue is
		// resolved. Billing DOES get a (short) cooldown so a credits-exhausted
		// model is skipped within the same session — see billingInitialCooldown
		// in cooldown.go. Use /cooldowns clear or /retry to override.
		noCooldown := failErr.Reason == FailoverContextLimit ||
			failErr.Reason == FailoverAuth
		if !noCooldown {
			fc.cooldown.MarkFailure(candidate.Provider, candidate.Model, failErr.Reason, failErr.RetryAfter)
		}
		result.Attempts = append(result.Attempts, FallbackAttempt{
			Provider: candidate.Provider,
			Model:    candidate.Model,
			Error:    failErr,
			Reason:   failErr.Reason,
			Duration: elapsed,
		})

		// If this was the last candidate, return aggregate error.
		if i == len(candidates)-1 {
			return nil, &FallbackExhaustedError{Attempts: result.Attempts}
		}
	}

	// All candidates were skipped (all in cooldown).
	return nil, &FallbackExhaustedError{Attempts: result.Attempts}
}

// ExecuteImage runs the fallback chain for image/vision requests.
// Simpler than Execute: no cooldown checks (image endpoints have different rate limits).
// Image dimension/size errors abort immediately (non-retriable).
func (fc *FallbackChain) ExecuteImage(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(ctx context.Context, provider, model string) (*LLMResponse, error),
) (*FallbackResult, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("image fallback: no candidates configured")
	}

	result := &FallbackResult{
		Attempts: make([]FallbackAttempt, 0, len(candidates)),
	}

	for i, candidate := range candidates {
		if ctx.Err() == context.Canceled {
			return nil, context.Canceled
		}

		start := time.Now()
		resp, err := run(ctx, candidate.Provider, candidate.Model)
		elapsed := time.Since(start)

		if err == nil {
			result.Response = resp
			result.Provider = candidate.Provider
			result.Model = candidate.Model
			return result, nil
		}

		if ctx.Err() == context.Canceled {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    err,
				Duration: elapsed,
			})
			return nil, context.Canceled
		}

		// Image dimension/size errors are non-retriable.
		errMsg := strings.ToLower(err.Error())
		if IsImageDimensionError(errMsg) || IsImageSizeError(errMsg) {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    err,
				Reason:   FailoverFormat,
				Duration: elapsed,
			})
			return nil, &FailoverError{
				Reason:   FailoverFormat,
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Wrapped:  err,
			}
		}

		// Any other error: record and try next.
		result.Attempts = append(result.Attempts, FallbackAttempt{
			Provider: candidate.Provider,
			Model:    candidate.Model,
			Error:    err,
			Duration: elapsed,
		})

		if i == len(candidates)-1 {
			return nil, &FallbackExhaustedError{Attempts: result.Attempts}
		}
	}

	return nil, &FallbackExhaustedError{Attempts: result.Attempts}
}

// FallbackExhaustedError indicates all fallback candidates were tried and failed.
type FallbackExhaustedError struct {
	Attempts []FallbackAttempt
}

// AllContextLimit returns true if every non-skipped attempt failed with FailoverContextLimit.
// Used by the agent loop to decide whether to attempt history compression.
func (e *FallbackExhaustedError) AllContextLimit() bool {
	nonSkipped := 0
	for _, a := range e.Attempts {
		if a.Skipped {
			continue
		}
		nonSkipped++
		if a.Reason != FailoverContextLimit {
			return false
		}
	}
	return nonSkipped > 0
}

func (e *FallbackExhaustedError) Error() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("fallback: all %d candidates failed:", len(e.Attempts)))
	for i, a := range e.Attempts {
		if a.Skipped {
			sb.WriteString(fmt.Sprintf("\n  [%d] %s/%s: skipped (cooldown)", i+1, a.Provider, a.Model))
		} else {
			sb.WriteString(fmt.Sprintf("\n  [%d] %s/%s: %v (reason=%s, %s)",
				i+1, a.Provider, a.Model, a.Error, a.Reason, a.Duration.Round(time.Millisecond)))
		}
	}
	return sb.String()
}
