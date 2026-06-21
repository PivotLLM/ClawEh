// ClawEh
// License: MIT

// Package maestro adapts the embedded Maestro task-orchestration provider into
// ClawEh: it builds Maestro's per-agent config, routes Maestro's logs to a
// per-agent log file, and dispatches every Maestro task as a ClawEh sub-agent so
// the host owns model selection + fallback.
package maestro

import (
	"context"

	mconfig "github.com/PivotLLM/Maestro/config"
	mllm "github.com/PivotLLM/Maestro/llm"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

// dispatcher implements maestro/llm.Dispatcher by running each Maestro task as a
// ClawEh sub-agent (a copy of the agent with full tools, MCP, fresh context). The
// host chooses the model + fallback — Maestro's tools only describe the work, and
// Maestro's own LLM IDs are ignored.
type dispatcher struct {
	run global.SyncRunner
}

// Dispatch runs the task's prompt as a sub-agent and maps the result back.
func (d *dispatcher) Dispatch(req *mllm.DispatchRequest) (*mllm.DispatchResult, error) {
	if d == nil || d.run == nil {
		return &mllm.DispatchResult{ExitCode: 1, Stderr: "host dispatcher not available"}, nil
	}
	// Model "" → the spawning agent's own model selection (with fallback).
	content, err := d.run.RunSync(context.Background(), req.Prompt, "")
	if err != nil {
		return &mllm.DispatchResult{
			ExitCode: 1,
			Stderr:   err.Error(),
			Text:     err.Error(),
		}, nil
	}
	return &mllm.DispatchResult{
		ExitCode:          0,
		Stdout:            content,
		Text:              content,
		ResponseParsed:    true,
		NormalTermination: true,
		Success:           true,
		ResponseSize:      len(content),
		BytesReceived:     int64(len(content)),
		ProviderModel:     "claw",
	}, nil
}

// The metadata methods return host stubs: the host owns model selection, so the
// runner's per-LLM logging/recovery sees a single synthetic "claw" LLM that is
// always available.
func (d *dispatcher) GetLLM(llmID string) *mconfig.LLM {
	id := llmID
	if id == "" {
		id = "claw"
	}
	return &mconfig.LLM{ID: id}
}

func (d *dispatcher) GetExecInfo(_ string) *mllm.LLMExecInfo { return &mllm.LLMExecInfo{} }

func (d *dispatcher) TestLLM(_ string) (bool, error) { return true, nil }
