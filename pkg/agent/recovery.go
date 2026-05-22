package agent

import (
	"context"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/session"
)

// recoverPendingTurns iterates over all agents and re-queues any sessions
// that were interrupted mid-turn (PendingTurn == true on startup).
func (al *AgentLoop) recoverPendingTurns(ctx context.Context) {
	registry := al.GetRegistry()
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}
		sessions, err := agent.Sessions.ListPendingSessions()
		if err != nil {
			logger.WarnCF("agent", "Failed to list pending sessions for recovery",
				map[string]any{"agent": agentID, "error": err.Error()})
			continue
		}
		for _, sessionKey := range sessions {
			al.recoverSession(ctx, agentID, sessionKey, agent.Sessions)
		}
	}
}

// recoverSession re-queues a single interrupted session on the inbound bus.
// If no user message is found in history the pending flag is cleared and
// the session is skipped.
func (al *AgentLoop) recoverSession(ctx context.Context, agentID, sessionKey string, store session.SessionStore) {
	history := store.GetHistory(sessionKey)

	// Find last user message (walk backwards for efficiency).
	content := ""
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			content = history[i].Content
			break
		}
	}

	if content == "" {
		logger.InfoCF("agent", "No user message found for pending session; clearing flag",
			map[string]any{"session": sessionKey})
		_ = store.ClearPendingTurn(sessionKey)
		return
	}

	msg := bus.InboundMessage{
		Channel:    "recovery",
		SenderID:   "recovery",
		Content:    content,
		SessionKey: sessionKey,
		IsRetry:    true,
		Metadata: map[string]string{
			"preresolved_agent_id": agentID,
		},
	}

	if err := al.bus.PublishInbound(ctx, msg); err != nil {
		logger.WarnCF("agent", "Failed to queue recovery message",
			map[string]any{"session": sessionKey, "agent": agentID, "error": err.Error()})
		return
	}

	logger.InfoCF("agent", "Queued recovery for interrupted session",
		map[string]any{"session": sessionKey, "agent": agentID})
}
