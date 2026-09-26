package agent

import (
	"context"
	"slices"

	"github.com/PivotLLM/ctxengine/session"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/state"
)

// recoveryMaxAttempts caps how often restarts replay one interrupted turn: a
// message that crashes the process must not crash-loop it.
const recoveryMaxAttempts = 2

const (
	recoveryReplayNotice = "I was restarted while working on your last request — here is the result; resend it if anything is missing."
	recoveryGiveUpNotice = "I was restarted while working on your last request and could not finish it. Please resend it if it still matters."
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

// recoverSession re-queues a single interrupted session on the inbound bus,
// addressed to the channel and chat the interrupted message came from, and
// tells the user there that the reply follows a restart. The pending flag is
// cleared without a replay when the source is unknown, its channel is no
// longer configured, no user message is in history, or the turn has already
// been replayed recoveryMaxAttempts times (the user is then asked to resend).
func (al *AgentLoop) recoverSession(ctx context.Context, agentID, sessionKey string, store session.SessionStore) {
	fields := map[string]any{"session": sessionKey, "agent": agentID}
	clearPending := func(reason string) {
		logger.InfoCF("agent", reason+"; clearing pending turn", fields)
		if err := store.ClearPendingTurn(sessionKey); err != nil {
			logger.WarnCF("agent", "Failed to clear pending-turn flag",
				map[string]any{"session": sessionKey, "error": err.Error()})
		}
		al.clearPendingTurnSource(agentID, sessionKey)
	}

	sm, ok := al.agentStates[agentID]
	if !ok {
		clearPending("No state manager for agent")
		return
	}
	src, ok := sm.GetPendingTurn(sessionKey)
	if !ok || src.Channel == "" || src.ChatID == "" {
		clearPending("No source recorded for pending session")
		return
	}
	fields["channel"] = src.Channel
	fields["chat_id"] = src.ChatID
	if al.channelManager != nil {
		if _, exists := al.channelManager.GetChannel(src.Channel); !exists {
			clearPending("Channel of pending session is no longer configured")
			return
		}
	}

	// Find last user message (walk backwards for efficiency).
	content := ""
	for _, h := range slices.Backward(store.GetHistory(sessionKey)) {
		if h.Role == "user" {
			content = h.Content
			break
		}
	}
	if content == "" {
		clearPending("No user message found for pending session")
		return
	}

	fields["attempts"] = src.Attempts
	if src.Attempts >= recoveryMaxAttempts {
		clearPending("Recovery attempts exhausted")
		al.publishRecoveryNotice(ctx, src, recoveryGiveUpNotice)
		return
	}
	src.Attempts++
	if err := sm.SetPendingTurn(sessionKey, src); err != nil {
		// Without a persisted count a replay could crash-loop; give up instead.
		fields["error"] = err.Error()
		clearPending("Cannot persist recovery attempt count")
		al.publishRecoveryNotice(ctx, src, recoveryGiveUpNotice)
		return
	}

	al.publishRecoveryNotice(ctx, src, recoveryReplayNotice)
	msg := bus.InboundMessage{
		Channel:    src.Channel,
		ChatID:     src.ChatID,
		SenderID:   "recovery",
		Content:    content,
		SessionKey: sessionKey,
		IsRetry:    true,
		Metadata: map[string]string{
			metadataKeyPreresolvedAgentID: agentID,
		},
	}
	if err := al.bus.PublishInbound(ctx, msg); err != nil {
		fields["error"] = err.Error()
		logger.WarnCF("agent", "Failed to queue recovery message", fields)
		return
	}
	fields["attempts"] = src.Attempts
	logger.InfoCF("agent", "Queued recovery for interrupted session", fields)
}

// publishRecoveryNotice sends a one-line restart notice to the chat an
// interrupted turn came from.
func (al *AgentLoop) publishRecoveryNotice(ctx context.Context, src state.PendingTurn, text string) {
	if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
		Channel: src.Channel,
		ChatID:  src.ChatID,
		Content: text,
	}); err != nil {
		logger.WarnCF("agent", "Failed to publish recovery notice",
			map[string]any{"channel": src.Channel, "chat_id": src.ChatID, "error": err.Error()})
	}
}

// recordPendingTurnSource remembers where the turn now in flight came from, so
// a restart can replay it on the same channel and deliver the reply there.
// Internal channels have no handler to deliver to, so they are not recorded and
// an interrupted turn on one is never replayed. Call it when the session
// store's pending-turn flag is set; clearPendingTurnSource pairs with clearing
// the flag.
func (al *AgentLoop) recordPendingTurnSource(agent *AgentInstance, opts processOptions) {
	if opts.Channel == "" || opts.ChatID == "" || constants.IsInternalChannel(opts.Channel) {
		return
	}
	sm, ok := al.agentStates[agent.ID]
	if !ok {
		return
	}
	if err := sm.SetPendingTurn(opts.SessionKey, state.PendingTurn{Channel: opts.Channel, ChatID: opts.ChatID}); err != nil {
		logger.WarnCF("agent", "Failed to record pending turn source",
			map[string]any{"error": err.Error(), "session": opts.SessionKey})
	}
}

// clearPendingTurnSource forgets the source recorded by recordPendingTurnSource.
func (al *AgentLoop) clearPendingTurnSource(agentID, sessionKey string) {
	sm, ok := al.agentStates[agentID]
	if !ok {
		return
	}
	if err := sm.ClearPendingTurn(sessionKey); err != nil {
		logger.WarnCF("agent", "Failed to clear pending turn source",
			map[string]any{"error": err.Error(), "session": sessionKey})
	}
}
