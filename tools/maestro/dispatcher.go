// ClawEh
// License: MIT

// Package maestro adapts the embedded Maestro task-orchestration provider into
// ClawEh: it builds Maestro's per-agent config, routes Maestro's logs to a
// per-agent log file, and dispatches every Maestro task as a ClawEh sub-agent so
// the host owns model selection + fallback.
package maestro

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	mconfig "github.com/PivotLLM/Maestro/config"
	mllm "github.com/PivotLLM/Maestro/llm"

	"github.com/PivotLLM/ClawEh/global"
)

// hostProviderModel is the model label recorded when the sub-agent run did not
// report which model served it.
const hostProviderModel = "claw"

// dispatcher implements maestro/llm.Dispatcher by running each Maestro task as a
// ClawEh sub-agent (a copy of the agent with full tools, MCP, fresh context). The
// host chooses the model + fallback — Maestro's tools only describe the work.
// A task's llm_model_id, when set, is passed through as a host model alias.
type dispatcher struct {
	run global.SyncRunner
	// timeout bounds one dispatched prompt (the whole sub-agent run). 0 = none.
	timeout time.Duration
}

// hostModel maps Maestro's llm_model_id hint to a host model request. Maestro's
// own placeholders ("default", the "host" label its runner records, and the
// synthetic "claw" model) all mean "the agent's default model".
func hostModel(llmID string) string {
	m := strings.TrimSpace(llmID)
	switch strings.ToLower(m) {
	case "", "default", "host", hostProviderModel:
		return ""
	}
	return m
}

// isPermanent reports whether a SyncRunner error can never succeed on retry.
func isPermanent(err error) bool {
	return errors.Is(err, global.ErrSpawnUnavailable) ||
		errors.Is(err, global.ErrSpawnDepthExceeded) ||
		errors.Is(err, global.ErrModelNotAvailable)
}

// Dispatch runs the task's prompt as a sub-agent and maps the result back. ctx
// carries the spawning agent's request scope — including its sub-agent depth —
// so a Maestro worker that dispatches its own tasks stays within MaxSpawnDepth.
//
// Error mapping: failures that no retry can fix (no runner, depth exceeded,
// unknown model alias) are returned as a Maestro permanent error so the runner
// fails the task at once; anything else (provider errors, the timeout) is
// returned as a non-zero exit so the runner's normal retry budget applies.
func (d *dispatcher) Dispatch(ctx context.Context, req *mllm.DispatchRequest) (*mllm.DispatchResult, error) {
	if d == nil || d.run == nil {
		return nil, mllm.Permanent(fmt.Errorf("%w: host dispatcher not available", global.ErrSpawnUnavailable))
	}
	parent := ctx
	if d.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.timeout)
		defer cancel()
	}

	start := time.Now()
	res, err := d.run.RunSync(ctx, req.Prompt, hostModel(req.LLMID))
	elapsed := time.Since(start).Milliseconds()

	if err != nil {
		if isPermanent(err) {
			return nil, mllm.Permanent(err)
		}
		stopReason := "error"
		// Only our own deadline counts as the turn timeout; a caller whose
		// context ended is reported as a plain error.
		if d.timeout > 0 && parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			stopReason = "timeout"
			err = fmt.Errorf("sub-agent exceeded the host turn timeout (%s): %w", d.timeout, err)
		}
		return &mllm.DispatchResult{
			ExitCode:   1,
			Stderr:     err.Error(),
			Text:       err.Error(),
			StopReason: stopReason,
			DurationMs: elapsed,
		}, nil
	}

	model := res.Model
	if model == "" {
		model = hostProviderModel
	}
	return &mllm.DispatchResult{
		ExitCode:            0,
		Stdout:              res.Content,
		Text:                res.Content,
		ResponseParsed:      true,
		NormalTermination:   true,
		Success:             true,
		NumTurns:            res.Iterations,
		ResponseSize:        len(res.Content),
		BytesReceived:       int64(len(res.Content)),
		BytesSent:           int64(len(req.Prompt)),
		InputTokens:         res.InputTokens,
		OutputTokens:        res.OutputTokens,
		CacheReadTokens:     res.CacheReadTokens,
		CacheCreationTokens: res.CacheCreationTokens,
		CostUSD:             res.CostUSD,
		DurationMs:          elapsed,
		ProviderModel:       model,
	}, nil
}

// The metadata methods return host stubs. This is deliberate: the host owns
// model selection and availability, so Maestro's per-LLM machinery is inert
// here — GetLLM carries no RecoveryConfig (Maestro never enters its rate-limit
// recovery mode; ClawEh's fallback chain and cooldowns handle that), and
// TestLLM always passes (no pre-flight probe; an unavailable model fails over
// or fails the task, which the runner then retries within its limits).
func (d *dispatcher) GetLLM(llmID string) *mconfig.LLM {
	id := llmID
	if id == "" {
		id = hostProviderModel
	}
	return &mconfig.LLM{ID: id}
}

func (d *dispatcher) GetExecInfo(_ string) *mllm.LLMExecInfo { return &mllm.LLMExecInfo{} }

func (d *dispatcher) TestLLM(_ string) (bool, error) { return true, nil }
