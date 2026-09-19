// ClawEh - sub-agent execution
// License: MIT

package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	cogmemstore "github.com/PivotLLM/cogmem/store"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// runSubagentTask runs a spawned sub-agent as a *copy of the target agent* in an
// isolated sub-agent session: same workspace, tools, MCP, and curated prompt as
// the primary, differing only in a fresh context (the task) and an optional model.
//
// Memory: the agent's main-session cogmem is snapshotted onto the sub-agent
// session's own DB so the worker has the agent's memory as background. The
// snapshot is a throwaway copy deleted with the session after the run, so any
// writes the worker makes stay on that copy and never reach the primary's memory.
//
// Tools: a sub-agent is an instance of the parent and gets the parent's FULL
// toolset (including maestro and spawn). Runaway recursion is bounded by
// MaxSpawnDepth in the Spawner — this worker runs one level deeper than whoever
// spawned it (see the WithSpawnDepth increment below).
//
// Output is captured (SendResponse:false) and returned to the caller (the
// SubagentManager stores it to a result file / fires the callback).
func (al *AgentLoop) runSubagentTask(ctx context.Context, agentID, sessionKey, task, model string, media []string) (*global.SyncResult, error) {
	agent, ok := al.GetRegistry().GetAgent(agentID)
	if !ok || agent == nil {
		return nil, fmt.Errorf("subagent: agent %q not found", agentID)
	}
	if !routing.IsSubagentSessionKey(sessionKey) {
		return nil, fmt.Errorf("subagent: %q is not a sub-agent session key", sessionKey)
	}

	// Validate attached media refs up front so a typo'd or expired ref fails the
	// spawn loudly instead of the worker silently seeing nothing.
	for _, ref := range media {
		if al.mediaStore == nil {
			return nil, fmt.Errorf("subagent: media refs passed but no media store is configured")
		}
		if _, err := al.mediaStore.Resolve(ref); err != nil {
			return nil, fmt.Errorf("subagent: media ref %s not found (expired or invalid) — it cannot be attached", ref)
		}
	}

	// Snapshot the agent's memory into a throwaway directory for this sub-agent
	// so the worker starts with the agent's background. Best-effort: a
	// missing/empty primary DB just means the sub-agent starts with empty memory.
	src := cogmemstore.DBPath(cogmemhost.Dir(agent.Workspace))
	dstDir := cogmemhost.SubagentDir(agent.Workspace, sessionKey)
	if _, statErr := os.Stat(src); statErr == nil {
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			logger.WarnCF("agent", "subagent cogmem snapshot dir failed", map[string]any{
				"agent": agentID, "error": err.Error(),
			})
		} else if err := cogmemstore.Snapshot(ctx, src, cogmemstore.DBPath(dstDir)); err != nil {
			logger.WarnCF("agent", "subagent cogmem snapshot failed", map[string]any{
				"agent": agentID, "error": err.Error(),
			})
		}
	}
	// Clean up the ephemeral session's DB files when the run is done (after the
	// context manager is released).
	defer al.cleanupSubagentSession(agent, sessionKey)

	// Optional model override (already validated against the agent's candidates by
	// the Spawner): point this session at the chosen model. A model that does not
	// match is an error rather than a silent run on the default model.
	if strings.TrimSpace(model) != "" {
		matched, found := toolsagents.MatchCandidate(agent.Candidates, model)
		if !found {
			return nil, fmt.Errorf("%w: model %q is not configured for agent %q", global.ErrModelNotAvailable, model, agentID)
		}
		for i, c := range agent.Candidates {
			if c.Alias == matched.Alias && c.Model == matched.Model {
				_ = al.setActiveModelIndex(agent, sessionKey, i)
				break
			}
		}
	}

	// This worker runs one level deeper than the agent that spawned it. Recording
	// the incremented depth on the loop context bounds any further spawning the
	// worker itself does (Spawner refuses once depth >= MaxSpawnDepth).
	ctx = toolsagents.WithSpawnDepth(ctx, toolsagents.SpawnDepth(ctx)+1)

	logger.InfoCF("agent", "subagent.run.start", map[string]any{
		"agent": agentID, "session_key": sessionKey, "model": model,
		"task_len": len(task), "media": len(media), "depth": toolsagents.SpawnDepth(ctx),
	})

	res := &global.SyncResult{}
	content, err := al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:    sessionKey,
		Channel:       "subagent",
		ChatID:        sessionKey,
		UserMessage:   task,
		Media:         media,
		SendResponse:  false,
		IterationsOut: &res.Iterations,
		UsageOut:      &res.TurnUsage,
	})
	if err != nil {
		logger.WarnCF("agent", "subagent.run.end", map[string]any{
			"agent": agentID, "session_key": sessionKey, "iterations": res.Iterations, "error": err.Error(),
		})
		return nil, err
	}
	res.Content = content
	logger.InfoCF("agent", "subagent.run.end", map[string]any{
		"agent": agentID, "session_key": sessionKey, "iterations": res.Iterations, "content_len": len(content),
		"model": res.Model, "input_tokens": res.InputTokens, "output_tokens": res.OutputTokens,
	})
	return res, nil
}

// cleanupSubagentSession evicts the sub-agent session's context manager (closing
// its DB handles), removes the memory snapshot directory and the ephemeral
// session's archive files. Best-effort.
func (al *AgentLoop) cleanupSubagentSession(agent *AgentInstance, sessionKey string) {
	al.dropContextManager(agent, sessionKey)
	al.releaseSessionPins(sessionKey)
	_ = os.RemoveAll(cogmemhost.SubagentDir(agent.Workspace, sessionKey))
	archive := archiveDBPath(agent.Workspace, sessionKey)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(archive + suffix)
	}
}
