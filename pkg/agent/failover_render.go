package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
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
	return func(failed providers.FallbackAttempt, next providers.FallbackCandidate) {
		notice := formatFallbackNotice(failed, next)
		if seen[notice] {
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

// formatFallbackNotice builds the mid-chain heads-up. For a failure, e.g.
// "⚠️ grok-2 error HTTP 402 (out of credits).\nTrying gpt-4o…"; for a
// cooldown-skipped candidate, "⚠️ DeepSeek V4 Pro Writing skipped (in cooldown).
// \nTrying Abliteration…" — so a skip is never silent after an earlier
// "Trying <this>…".
func formatFallbackNotice(failed providers.FallbackAttempt, next providers.FallbackCandidate) string {
	nextName := next.Alias
	if nextName == "" {
		nextName = next.Model
	}
	failedName := failed.Alias
	if failedName == "" {
		failedName = failed.Model
	}
	if failed.Skipped {
		return fmt.Sprintf("⚠️ %s skipped (in cooldown).\nTrying %s…", failedName, nextName)
	}
	return fmt.Sprintf("⚠️ %s.\nTrying %s…",
		attemptDescription(failedName, failoverStatus(failed.Error), failed.Reason),
		nextName,
	)
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
