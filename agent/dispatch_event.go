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

// Kinds of model call named by the kind field of the "LLM finish" line.
const (
	llmKindAgent       = "agent"
	llmKindSubagent    = "subagent"
	llmKindCompression = "compression"
	llmKindVision      = "vision"
)

// costUnknown is what the cost field shows when the provider reported no cost.
const costUnknown = "unknown"

// llmCaller is the agent a model call is made for, as named on the finish
// line: its id, its label ("alice (clone 1a2b3c4d)" for a clone) and, for a
// clone, the id of the agent it was cloned from.
type llmCaller struct {
	agentID  string
	label    string
	parentID string
}

// callerOf names agent for the finish line.
func callerOf(agent *AgentInstance) llmCaller {
	c := llmCaller{agentID: agent.ID, label: agent.Label()}
	if agent.Spec.IsClone() {
		c.parentID = agent.Spec.SourceID
	}
	return c
}

// llmFinish is one completed model call, the input of the "LLM finish" line.
// Empty optional fields are left off the line.
type llmFinish struct {
	caller llmCaller
	kind   string
	// iteration is the agent loop's iteration; logged for agent and
	// subagent calls only.
	iteration int
	// ourProvider and ourModel are the config provider name and model_name
	// alias of the model that answered.
	ourProvider string
	ourModel    string
	// requestedModel is the model logged when the provider's status names
	// none.
	requestedModel string
	sessionKey     string
	channel        string
	chatID         string
	turnID         string
	elapsedMs      int64
	resp           *providers.LLMResponse
	err            error
}

// emitLLMFinishEvent writes the single "LLM finish" INFO record for one model
// call, on success and on error alike, so every call leaves one usage line.
func emitLLMFinishEvent(f llmFinish) {
	logger.InfoCF("agent", "LLM finish", f.fields())
}

// fields assembles the structured fields of the finish line. If the provider
// returned a populated DispatchStatus it is used verbatim; otherwise a
// fallback is synthesised from the requested model and the elapsed time.
func (f llmFinish) fields() map[string]any {
	status := resolveDispatchStatus(f.resp, f.err, f.requestedModel, f.elapsedMs)
	model := status.Model
	if model == "" {
		model = f.requestedModel
	}
	// DispatchStatus has no "cost reported" flag: a cost above zero is one
	// the provider reported.
	var cost any = costUnknown
	if status.CostUSD > 0 {
		cost = status.CostUSD
	}
	fields := map[string]any{
		"agent_id":              f.caller.agentID,
		"agent":                 f.caller.label,
		"kind":                  f.kind,
		"success":               status.Success,
		"our_provider":          f.ourProvider,
		"our_model":             f.ourModel,
		"model":                 model,
		"num_turns":             status.NumTurns,
		"input_tokens":          status.InputTokens,
		"output_tokens":         status.OutputTokens,
		"cache_read_tokens":     status.CacheReadTokens,
		"cache_creation_tokens": status.CacheCreationTokens,
		"stop_reason":           status.StopReason,
		"cost":                  cost,
		"duration_ms":           status.DurationMs,
		"bytes_sent":            status.BytesSent,
		"bytes_received":        status.BytesReceived,
	}
	if f.kind == llmKindAgent || f.kind == llmKindSubagent {
		fields["iteration"] = f.iteration
	}
	optional := map[string]string{
		"parent_agent_id": f.caller.parentID,
		"session_key":     f.sessionKey,
		"channel":         f.channel,
		"chat_id":         f.chatID,
		"turn_id":         f.turnID,
	}
	for k, v := range optional {
		if v != "" {
			fields[k] = v
		}
	}
	if f.err != nil {
		fields["error"] = truncateForLog(redactSecrets(f.err.Error()), finishEventMaxErrLen)
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
