package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

const finishEventMaxErrLen = 500

// dailySpend sums dispatch cost per UTC day and remembers whether the day's
// spend alert has fired. Resets when the day changes.
type dailySpend struct {
	mu      sync.Mutex
	day     string
	total   float64
	alerted bool
}

// add records cost at now and reports the day (UTC, yyyy-mm-dd), the day's
// running total and whether that total reached threshold for the first time
// today. A threshold <= 0 never fires.
func (s *dailySpend) add(cost, threshold float64, now time.Time) (day string, total float64, crossed bool) {
	day = now.UTC().Format(time.DateOnly)
	s.mu.Lock()
	defer s.mu.Unlock()
	if day != s.day {
		s.day, s.total, s.alerted = day, 0, false
	}
	s.total += cost
	if threshold <= 0 || s.alerted || s.total < threshold {
		return day, s.total, false
	}
	s.alerted = true
	return day, s.total, true
}

// recordSpend folds one turn's cost into the day's total and raises the
// daily-spend alert the first time it reaches
// agents.defaults.daily_spend_alert_usd (0 = off).
func (al *AgentLoop) recordSpend(costUSD float64) {
	cfg := al.GetConfig()
	if costUSD <= 0 || cfg == nil {
		return
	}
	threshold := cfg.Agents.Defaults.DailySpendAlertUSD
	day, total, crossed := al.spend.add(costUSD, threshold, time.Now())
	if !crossed {
		return
	}
	al.Alerter().Send(alerter.Alert{
		Title:       "Daily model spend over threshold",
		Description: fmt.Sprintf("Model spend on %s (UTC) is $%.2f, over the daily_spend_alert_usd threshold of $%.2f", day, total, threshold),
		EventID:     "spend:" + day,
	})
}

// emitLLMFinishEvent writes a single "LLM finish" INFO record at the agent loop
// call site. It is invoked for both success and error returns from callLLM so
// the dispatch / finish pair is always balanced.
//
// If the provider returned a populated DispatchStatus the event uses it
// verbatim; otherwise a fallback is synthesised using the requested model and
// the wall-clock elapsed since dispatchStart.
func emitLLMFinishEvent(
	agentID string,
	iteration int,
	provider, requestedModel string,
	dispatchStart time.Time,
	resp *providers.LLMResponse,
	callErr error,
) {
	fields := buildLLMFinishFields(
		agentID, iteration, provider, requestedModel,
		time.Since(dispatchStart).Milliseconds(),
		resp, callErr,
	)
	logger.InfoCF("agent", "LLM finish", fields)
}

// buildLLMFinishFields assembles the structured fields for the LLM finish
// event. Split out from emitLLMFinishEvent so tests can assert the payload
// without intercepting the global logger.
func buildLLMFinishFields(
	agentID string,
	iteration int,
	provider, requestedModel string,
	elapsedMs int64,
	resp *providers.LLMResponse,
	callErr error,
) map[string]any {
	status := resolveDispatchStatus(resp, callErr, requestedModel, elapsedMs)
	model := status.Model
	if model == "" {
		model = requestedModel
	}
	fields := map[string]any{
		"agent_id":              agentID,
		"iteration":             iteration,
		"success":               status.Success,
		"provider":              provider,
		"model":                 model,
		"num_turns":             status.NumTurns,
		"input_tokens":          status.InputTokens,
		"output_tokens":         status.OutputTokens,
		"cache_read_tokens":     status.CacheReadTokens,
		"cache_creation_tokens": status.CacheCreationTokens,
		"stop_reason":           status.StopReason,
		"cost_usd":              status.CostUSD,
		"duration_ms":           status.DurationMs,
		"bytes_sent":            status.BytesSent,
		"bytes_received":        status.BytesReceived,
	}
	if callErr != nil {
		fields["error"] = truncateForLog(callErr.Error(), finishEventMaxErrLen)
	}
	return fields
}

// resolveDispatchStatus returns the response's DispatchStatus, or a synthesized
// one when the provider failed to populate it (e.g. an error path returning
// nil). The synthesized status records what the call site does know: requested
// model, wall-clock duration, and success=false on error.
func resolveDispatchStatus(
	resp *providers.LLMResponse,
	callErr error,
	requestedModel string,
	elapsed int64,
) *providers.DispatchStatus {
	if resp != nil && resp.Status != nil {
		return resp.Status
	}
	stopReason := "success"
	success := callErr == nil
	if !success {
		stopReason = "error"
	}
	return &providers.DispatchStatus{
		Success:    success,
		Model:      requestedModel,
		StopReason: stopReason,
		DurationMs: elapsed,
	}
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
