// ClawEh - sub-agent execution
// License: MIT

package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	cogmemstore "github.com/PivotLLM/ClawEh/pkg/cogmem/store"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/routing"
	toolsagents "github.com/PivotLLM/ClawEh/pkg/tools/agents"
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
func (al *AgentLoop) runSubagentTask(ctx context.Context, agentID, sessionKey, task, model string) (string, int, error) {
	agent, ok := al.GetRegistry().GetAgent(agentID)
	if !ok || agent == nil {
		return "", 0, fmt.Errorf("subagent: agent %q not found", agentID)
	}
	if !routing.IsSubagentSessionKey(sessionKey) {
		return "", 0, fmt.Errorf("subagent: %q is not a sub-agent session key", sessionKey)
	}

	// Snapshot the agent's main-session memory into this sub-agent session's DB so
	// the worker starts with the agent's background. Best-effort: a missing/empty
	// primary DB just means the sub-agent starts with empty memory.
	mainSession := routing.BuildAgentMainSessionKey(agentID)
	src := cogmemstore.SessionDBPath(agent.Workspace, mainSession)
	dst := cogmemstore.SessionDBPath(agent.Workspace, sessionKey)
	if _, statErr := os.Stat(src); statErr == nil {
		if err := cogmemstore.Snapshot(ctx, src, dst); err != nil {
			logger.WarnCF("agent", "subagent cogmem snapshot failed", map[string]any{
				"agent": agentID, "error": err.Error(),
			})
		}
	}
	// Clean up the ephemeral session's DB files when the run is done (after the
	// context manager is released).
	defer al.cleanupSubagentSession(agent, sessionKey)

	// Optional model override (already validated against the agent's candidates by
	// the Spawner): point this session at the chosen model.
	if strings.TrimSpace(model) != "" {
		if matched, found := toolsagents.MatchCandidate(agent.Candidates, model); found {
			for i, c := range agent.Candidates {
				if c.Alias == matched.Alias && c.Model == matched.Model {
					_ = al.setActiveModelIndex(agent, sessionKey, i)
					break
				}
			}
		}
	}

	// This worker runs one level deeper than the agent that spawned it. Recording
	// the incremented depth on the loop context bounds any further spawning the
	// worker itself does (Spawner refuses once depth >= MaxSpawnDepth).
	ctx = toolsagents.WithSpawnDepth(ctx, toolsagents.SpawnDepth(ctx)+1)

	logger.InfoCF("agent", "subagent.run.start", map[string]any{
		"agent": agentID, "session_key": sessionKey, "model": model,
		"task_len": len(task), "depth": toolsagents.SpawnDepth(ctx),
	})

	var iterations int
	content, err := al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:    sessionKey,
		Channel:       "subagent",
		ChatID:        sessionKey,
		UserMessage:   task,
		SendResponse:  false,
		IterationsOut: &iterations,
	})
	if err != nil {
		logger.WarnCF("agent", "subagent.run.end", map[string]any{
			"agent": agentID, "session_key": sessionKey, "iterations": iterations, "error": err.Error(),
		})
	} else {
		logger.InfoCF("agent", "subagent.run.end", map[string]any{
			"agent": agentID, "session_key": sessionKey, "iterations": iterations, "content_len": len(content),
		})
	}
	return content, iterations, err
}

// cleanupSubagentSession evicts the sub-agent session's context manager (closing
// its DB handles) and removes the ephemeral session DB files. Best-effort.
func (al *AgentLoop) cleanupSubagentSession(agent *AgentInstance, sessionKey string) {
	al.dropContextManager(agent, sessionKey)
	base := cogmemstore.SanitizeSessionKey(sessionKey)
	dir := cogmemstore.SessionDBPath(agent.Workspace, sessionKey)
	sessionsDir := dir[:len(dir)-len(base)-len(".cogmem.db")]
	for _, suffix := range []string{
		".cogmem.db", ".cogmem.db-wal", ".cogmem.db-shm",
		".archive.db", ".archive.db-wal", ".archive.db-shm",
	} {
		_ = os.Remove(sessionsDir + base + suffix)
	}
}
