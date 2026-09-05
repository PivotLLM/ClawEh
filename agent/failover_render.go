package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

// cooldownPolicy maps the config cooldown section to a providers.CooldownPolicy
// (the per-category "settled" durations reached after the 1/3/5-minute
// escalation). A nil config yields the built-in defaults.
func cooldownPolicy(cfg *config.Config) providers.CooldownPolicy {
	if cfg == nil {
		return providers.DefaultCooldownPolicy()
	}
	c := cfg.Cooldown
	return providers.CooldownPolicy{
		BillingAuth: c.BillingAuth(),
		RateLimit:   c.RateLimit(),
		BadRequest:  c.BadRequest(),
		ClientError: c.ClientError(),
		ServerError: c.ServerError(),
	}
}

// renderTurnError converts a failed turn into a single user-facing string. Order
// of precedence:
//  1. the turn budget elapsed (hard backstop) — say so plainly;
//  2. a billing failure — the billing renderer adds an actionable top-up URL;
//  3. any other provider/fallback failure — surface the raw HTTP status code(s),
//     because operators are technical and want the code, not an interpretation;
//  4. anything else — the raw error.
func renderTurnError(turnCtx context.Context, budget time.Duration, err error) string {
	if turnCtx != nil && errors.Is(turnCtx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf(
			"⚠️ This turn ran past the %s time limit and was stopped. Some steps may have completed — ask me to continue if needed.",
			formatBudget(budget),
		)
	}
	if friendly := renderBillingError(err); friendly != "" {
		return friendly
	}
	if friendly := renderFailoverError(err); friendly != "" {
		return friendly
	}
	return fmt.Sprintf("Error processing message: %v", err)
}

// fallbackNotifier returns a providers.FallbackNotify that posts a short,
// technical heads-up to the originating chat each time a model fails and the
// chain moves to the next one. Returns nil for non-user contexts (no channel, or
// the internal "system" channel, or when SendResponse is off) so background work
// stays silent. The notice ALWAYS includes the HTTP status code when present.
func (al *AgentLoop) fallbackNotifier(opts processOptions) providers.FallbackNotify {
	if opts.Channel == "" || opts.Channel == "system" || opts.ChatID == "" {
		return nil
	}
	// De-duplicate identical notices across the turn: a primary that fails over the
	// same way on every tool iteration (e.g. a model that 400s each call) would
	// otherwise repeat its heads-up per iteration. One notifier spans the turn (see
	// runLLMIteration), so this memory suppresses the repeats.
	seen := make(map[string]bool)
	return func(passed []providers.FallbackAttempt, next providers.FallbackCandidate) {
		notice := formatFallbackNotice(passed, next)
		if notice == "" || seen[notice] {
			return
		}
		seen[notice] = true
		pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Channel: opts.Channel,
			ChatID:  opts.ChatID,
			Content: notice,
		}); err != nil {
			logger.WarnCF("agent", "Failed to publish fallback notice",
				map[string]any{"channel": opts.Channel, "error": err.Error()})
		}
	}
}

// formatFallbackNotice builds the mid-chain heads-up for the candidates the
// chain just moved past. For a failure, e.g. "⚠️ grok-2 error HTTP 402 (out of
// credits).\nTrying gpt-4o…"; for cooldown skips, ONE line naming the cause,
// e.g. "⚠️ 3 models unavailable — out of credits (retry in up to 58m). Using
// Grok Medium." — so a skip is never silent after an earlier "Trying <this>…"
// but a fully-parked provider costs one line per turn, not one per model.
func formatFallbackNotice(passed []providers.FallbackAttempt, next providers.FallbackCandidate) string {
	if len(passed) == 0 {
		return ""
	}
	nextName := candidateName(next)
	if passed[0].Skipped {
		return formatSkipNotice(passed, nextName)
	}
	failed := passed[0]
	return fmt.Sprintf("⚠️ %s.\nTrying %s…",
		attemptDescription(attemptName(failed), failoverStatus(failed.Error), failed.Reason),
		nextName,
	)
}

// formatSkipNotice collapses a run of cooldown-skipped candidates into a single
// line that names WHY they are parked — "in cooldown" on its own tells the user
// nothing actionable, which is the whole point of carrying the tracker's reason
// through the skip.
func formatSkipNotice(skipped []providers.FallbackAttempt, nextName string) string {
	reasons := distinctReasons(skipped)
	cause := strings.Join(reasons, ", ")
	if cause == "" {
		cause = "in cooldown"
	}

	var longest time.Duration
	for _, a := range skipped {
		if a.Remaining > longest {
			longest = a.Remaining
		}
	}
	retry := ""
	if r := formatRemaining(longest); r != "" {
		if len(skipped) == 1 {
			retry = fmt.Sprintf(" (retry in %s)", r)
		} else {
			retry = fmt.Sprintf(" (retry in up to %s)", r)
		}
	}

	if len(skipped) == 1 {
		return fmt.Sprintf("⚠️ %s unavailable — %s%s. Using %s.",
			attemptName(skipped[0]), cause, retry, nextName)
	}
	return fmt.Sprintf("⚠️ %d models unavailable — %s%s. Using %s.",
		len(skipped), cause, retry, nextName)
}

// distinctReasons lists the human-readable failure reasons across a batch of
// skipped attempts, in first-seen order and without repeats, so a mixed batch
// reads "out of credits, rate limited" rather than repeating one cause N times.
func distinctReasons(attempts []providers.FallbackAttempt) []string {
	seen := make(map[string]bool, len(attempts))
	var out []string
	for _, a := range attempts {
		if a.Reason == "" {
			continue
		}
		text := providers.ReasonText(a.Reason)
		if seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	return out
}

// formatRemaining renders a cooldown remainder compactly ("42m", "30s").
// Returns "" for a non-positive duration so the caller can omit the clause.
func formatRemaining(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d.Round(time.Second) < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
	}
	return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
}

// candidateName prefers the user-facing alias over the wire model id.
func candidateName(c providers.FallbackCandidate) string {
	if c.Alias != "" {
		return c.Alias
	}
	return c.Model
}

// attemptName prefers the user-facing alias over the wire model id.
func attemptName(a providers.FallbackAttempt) string {
	if a.Alias != "" {
		return a.Alias
	}
	return a.Model
}

// renderFailoverError formats a provider failover failure with the HTTP status
// code(s) intact. Returns "" when err is not a FailoverError/FallbackExhaustedError
// (so the caller can fall through to a generic rendering).
func renderFailoverError(err error) string {
	if err == nil {
		return ""
	}
	var exhausted *providers.FallbackExhaustedError
	if errors.As(err, &exhausted) {
		var attempts []string
		for _, a := range exhausted.Attempts {
			if a.Skipped {
				continue
			}
			attempts = append(attempts, attemptDescription(a.Model, failoverStatus(a.Error), a.Reason))
		}
		if len(attempts) == 0 {
			return ""
		}
		if len(attempts) == 1 {
			return "All models failed. " + attempts[len(attempts)-1] + "."
		}
		return "All models failed:\n  • " + strings.Join(attempts, "\n  • ")
	}
	var fe *providers.FailoverError
	if errors.As(err, &fe) {
		return attemptDescription(fe.Model, fe.Status, fe.Reason) + "."
	}
	return ""
}

// failoverStatus extracts the HTTP status code carried by a classified
// FailoverError, or 0 when the error has none (timeout, network, …).
func failoverStatus(err error) int {
	var fe *providers.FailoverError
	if errors.As(err, &fe) {
		return fe.Status
	}
	return 0
}

// attemptDescription renders one model failure: it ALWAYS includes the HTTP
// status when present (e.g. "grok error HTTP 402 (billing)") and otherwise falls
// back to the classified reason (e.g. "grok error: timeout").
func attemptDescription(model string, status int, reason providers.FailoverReason) string {
	name := model
	if name == "" {
		name = "model"
	}
	r := providers.ReasonText(reason)
	if status > 0 {
		return fmt.Sprintf("%s error HTTP %d (%s)", name, status, r)
	}
	return fmt.Sprintf("%s error: %s", name, r)
}

// formatBudget renders a turn budget compactly (e.g. "15m", "90s").
func formatBudget(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return d.String()
}
