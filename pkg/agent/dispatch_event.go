package agent

import (
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

const finishEventMaxErrLen = 500

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
