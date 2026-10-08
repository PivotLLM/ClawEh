// ClawEh - sub-agent execution
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// runSubagentTask runs a spawned sub-agent as a temporary clone of the target
// agent: same workspace, tools, MCP, curated prompt and configuration, its own
// fresh conversation, and an optional model. It is the RunFunc behind
// agent_spawn and Maestro dispatch.
//
// Memory: the clone starts with a snapshot of the agent's memory, ephemeral,
// so the worker has the agent's memory as background but nothing it does is
// observed into it, and any memory it writes stays on its copy, deleted with
// the clone.
//
// Tools: a sub-agent gets the target's FULL toolset (including maestro and
// spawn). Runaway recursion is bounded by MaxSpawnDepth in the Spawner — this
// worker runs one level deeper than whoever spawned it.
//
// Output is captured (SendResponse:false) and returned to the caller (the
// SubagentManager stores it to a result file / fires the callback), which
// calls release to delete the clone once the result has been delivered.
func (al *AgentLoop) runSubagentTask(ctx context.Context, agentID, task, model string, media []string) (*global.SyncResult, func(), error) {
	noop := func() {}
	registry := al.GetRegistry()
	target, ok := registry.GetConfigured(agentID)
	if !ok || target == nil {
		return nil, noop, fmt.Errorf("subagent: agent %q not found", agentID)
	}
	if target.HumanModel != "" {
		return nil, noop, fmt.Errorf("subagent: agent %q represents a person and cannot be spawned", agentID)
	}

	// Validate attached media refs up front so a typo'd or expired ref fails the
	// spawn loudly instead of the worker silently seeing nothing.
	for _, ref := range media {
		if al.mediaStore == nil {
			return nil, noop, errors.New("subagent: media refs passed but no media store is configured")
		}
		if _, err := al.mediaStore.Resolve(ref); err != nil {
			return nil, noop, fmt.Errorf("subagent: media ref %s not found (expired or invalid) — it cannot be attached", ref)
		}
	}

	// The clone runs on the agent's configuration; an optional model (already
	// validated against the agent's candidates by the Spawner) narrows it to
	// that one model. A model that does not match is an error rather than a
	// silent run on the default model.
	opts := []agentreg.Option{agentreg.CloneOf(target.ID), agentreg.EphemeralMemory()}
	if strings.TrimSpace(model) != "" {
		matched, found := toolsagents.MatchCandidate(target.Candidates, model)
		if !found {
			return nil, noop, fmt.Errorf("%w: model %q is not configured for agent %q", global.ErrModelNotAvailable, model, agentID)
		}
		opts = append(opts, agentreg.CloneModel(candidateName(matched)))
	}

	// The clone is in a turn from the moment it exists, so nothing (a reload,
	// the sweep) can replace or delete it before the run is over.
	cloneID, endTurn, err := registry.CreateInTurn(config.AgentConfig{}, opts...)
	if err != nil {
		return nil, noop, fmt.Errorf("subagent: %w", err)
	}
	release := func() {
		endTurn()
		if delErr := registry.Delete(cloneID); delErr != nil {
			logger.WarnCF("agent", "Sub-agent clone not deleted; it is removed once idle past its TTL or at the next start",
				map[string]any{"agent_id": cloneID, "ttl": agentreg.DefaultTTL.String(), "error": delErr.Error()})
		}
	}
	clone, ok := registry.Get(cloneID)
	if !ok {
		return nil, release, fmt.Errorf("subagent: clone of %q vanished", agentID)
	}
	sessionKey := routing.BuildAgentMainSessionKey(clone.ID)

	// This worker runs one level deeper than the agent that spawned it. Recording
	// the incremented depth on the loop context bounds any further spawning the
	// worker itself does (Spawner refuses once depth >= MaxSpawnDepth).
	ctx = toolsagents.WithSpawnDepth(ctx, toolsagents.SpawnDepth(ctx)+1)

	logger.InfoCF("agent", "subagent.run.start", map[string]any{
		"agent": clone.Label(), "agent_id": clone.ID, "session_key": sessionKey, "model": model,
		"task_len": len(task), "media": len(media), "depth": toolsagents.SpawnDepth(ctx),
	})

	// Tally the worker's tool calls (in the loop and over MCP) under its session
	// key, so a caller can tell a worker whose every tool call failed from one
	// that did its work. The deferred End removes the entry if the run never
	// reaches the read below (a panic); after the read it is a no-op.
	tools.BeginToolStats(sessionKey)
	defer tools.EndToolStats(sessionKey)

	res := &global.SyncResult{}
	content, err := al.runAgentLoop(ctx, clone, processOptions{
		SessionKey:    sessionKey,
		Channel:       "subagent",
		ChatID:        sessionKey,
		UserMessage:   task,
		Media:         media,
		SendResponse:  false,
		IterationsOut: &res.Iterations,
		UsageOut:      &res.TurnUsage,
	})
	toolCalls, toolErrors, lastToolError := tools.EndToolStats(sessionKey)
	if err != nil {
		logger.WarnCF("agent", "subagent.run.end", map[string]any{
			"agent": clone.Label(), "agent_id": clone.ID, "session_key": sessionKey,
			"iterations": res.Iterations, "error": err.Error(),
			"tool_calls": toolCalls, "tool_errors": toolErrors,
		})
		return nil, release, err
	}
	res.Content = content
	res.ToolCalls, res.ToolErrors, res.LastToolError = toolCalls, toolErrors, lastToolError
	res.SessionKey = sessionKey
	logger.InfoCF("agent", "subagent.run.end", map[string]any{
		"agent": clone.Label(), "agent_id": clone.ID, "session_key": sessionKey,
		"iterations": res.Iterations, "content_len": len(content),
		"model": res.Model, "input_tokens": res.InputTokens, "output_tokens": res.OutputTokens,
		"tool_calls": toolCalls, "tool_errors": toolErrors,
	})
	return res, release, nil
}
