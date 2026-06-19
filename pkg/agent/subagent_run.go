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
// session's own DB so the worker has the agent's memory as background; cogmem
// WRITE tools are PrimaryOnly (excluded), so the snapshot is effectively
// read-only and the primary's memory is never touched. The snapshot is deleted
// with the session after the run.
//
// Tools: runAgentLoop offers the agent's tools minus PrimaryOnly ones for a
// sub-agent session (agent_spawn → no recursion, cron_schedule, cogmem writes).
//
// Output is captured (SendResponse:false) and returned to the caller (the
// SubagentManager stores it to a result file / fires the callback).
func (al *AgentLoop) runSubagentTask(ctx context.Context, agentID, sessionKey, task, model string) (string, error) {
	agent, ok := al.GetRegistry().GetAgent(agentID)
	if !ok || agent == nil {
		return "", fmt.Errorf("subagent: agent %q not found", agentID)
	}
	if !routing.IsSubagentSessionKey(sessionKey) {
		return "", fmt.Errorf("subagent: %q is not a sub-agent session key", sessionKey)
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

	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:   sessionKey,
		Channel:      "subagent",
		ChatID:       sessionKey,
		UserMessage:  task,
		SendResponse: false,
	})
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
