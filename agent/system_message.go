// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
)

// processSystemMessage runs the turn for a "system" message (a tool or
// sub-agent result, a forum notice) in the session of the agent it names,
// replying to the chat it answers.
func (al *AgentLoop) processSystemMessage(
	ctx context.Context,
	msg bus.InboundMessage,
	outcome *string,
) (string, error) {
	if msg.Channel != "system" {
		return "", fmt.Errorf(
			"processSystemMessage called with non-system message channel: %s",
			msg.Channel,
		)
	}

	// The chat the turn answers: ChatID in the channel MetaOriginChannel names.
	// Every system message names it; one that does not is a bug in its sender.
	originChannel, originChatID := inboundMetadata(msg, bus.MetaOriginChannel), msg.ChatID
	if originChannel == "" {
		logger.WarnCF("agent", "System message dropped: it names no origin channel",
			map[string]any{"sender_id": msg.SenderID, "chat_id": msg.ChatID})
		return "", nil
	}
	logger.InfoCF("agent", "Processing system message",
		map[string]any{
			"sender_id":      msg.SenderID,
			"origin_channel": originChannel,
			"origin_chat_id": originChatID,
		})

	// A background result of an asked turn: the asker has its reply already,
	// so the result goes to the agent's own main conversation only. The turn
	// runs on the ask channel (no chat id): nothing is sent to any chat, no
	// chat becomes the session's source, and no recovery record is kept.
	askOrigin := originChannel == constants.AgentMessageChannel
	if askOrigin {
		originChatID = ""
	}

	// Skip internal channels - only log, don't send to user
	if !askOrigin && constants.IsInternalChannel(originChannel) {
		logger.InfoCF("agent", "Subagent completed (internal channel)",
			map[string]any{
				"sender_id":   msg.SenderID,
				"content_len": len(subagentResult(msg.Content)),
				"channel":     originChannel,
			})
		return "", nil
	}

	agent, sessionKey := al.resolveSystemMessageTarget(msg)
	if agent == nil {
		if inboundMetadata(msg, metadataKeyPreresolvedAgentID) != "" {
			return "", errAgentGone
		}
		return "", errors.New("no agent available for system message")
	}

	result := capAsyncResult(agent, msg)
	response, err := al.runMeteredTurn(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         originChannel,
		ChatID:          originChatID,
		UserMessage:     fmt.Sprintf("[System: %s] %s", msg.SenderID, result),
		DefaultResponse: "Background task completed.",
		SendResponse:    !askOrigin,
		OutcomeOut:      outcome,
		OnReplyDelivery: al.systemReplyFallback(ctx, msg, agent.ID, originChannel, originChatID),
	})
	if askOrigin && err == nil {
		// Kept in the conversation; not sent anywhere.
		logger.InfoCF("agent", "Background result of an asked turn kept in the main conversation",
			map[string]any{"agent_id": agent.ID, "sender_id": msg.SenderID, "reply_len": len(response)})
		return "", nil
	}
	return response, err
}

// subagentResult is the result part of a sub-agent's completion message
// ("Task 'label' completed.\n\nResult:\n<actual content>"), or all of it.
func subagentResult(content string) string {
	if _, result, found := strings.Cut(content, "Result:\n"); found {
		return result
	}
	return content
}

// capAsyncResult caps an async result for agent's context. It is a tool
// result that arrives late: capped as the synchronous path does, or an
// oversized one fails the turn it lands in.
func capAsyncResult(agent *AgentInstance, msg bus.InboundMessage) string {
	result := capToolResult(msg.Content, false, agent.ContextWindow)
	if len(result) != len(msg.Content) {
		logger.WarnCF("agent", "Async task result truncated for context", map[string]any{
			"agent_id":     agent.ID,
			"sender_id":    msg.SenderID,
			"original_len": len(msg.Content),
			"kept_len":     len(result),
		})
	}
	return result
}

// Metadata of a "system" message naming the chat its reply falls back to
// when the chat in its ChatID is offline or unknown (the forum's notice:
// the launching chat first, the launcher's default chat after).
const (
	metadataKeyFallbackChannel = "fallback_channel"
	metadataKeyFallbackChatID  = "fallback_chat_id"
)

// systemReplyFallback returns the OnReplyDelivery of a system message's
// reply: when msg names a fallback chat and the reply could not reach its
// own chat because the recipient is offline or not found, the same reply is
// sent to the fallback chat. It returns nil when msg names no fallback.
func (al *AgentLoop) systemReplyFallback(ctx context.Context, msg bus.InboundMessage, agentID, channel, chatID string) func(string, error) {
	fbChannel := inboundMetadata(msg, metadataKeyFallbackChannel)
	fbChatID := inboundMetadata(msg, metadataKeyFallbackChatID)
	if fbChannel == "" || fbChatID == "" {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	return func(reply string, err error) {
		if !errors.Is(err, channels.ErrRecipientOffline) && !errors.Is(err, channels.ErrRecipientNotFound) {
			return
		}
		fields := map[string]any{
			"agent_id": agentID, "sender_id": msg.SenderID, "channel": channel, "chat_id": chatID,
			"fallback_channel": fbChannel, "fallback_chat_id": fbChatID, "error": err.Error(),
		}
		// OnDelivery must not block: the fallback is published on its own.
		go func() {
			pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
			defer cancel()
			if perr := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{Channel: fbChannel, ChatID: fbChatID, Content: reply}); perr != nil {
				fields["publish_error"] = perr.Error()
				logger.WarnCF("agent", "Reply's chat unavailable; the fallback chat could not be queued either", fields)
				return
			}
			logger.InfoCF("agent", "Reply's chat unavailable; reply sent to the fallback chat", fields)
		}()
	}
}

// resolveSystemMessageTarget picks the agent and session a "system" message
// (e.g. an async tool/sub-agent completion) should be processed in. A spawn or
// other async tool carries the originating agent on preresolved_agent_id and its
// session on session_key, so the completion is handled by the SPAWNING agent in
// its own session — not whichever agent happens to be the default. Falls back to
// the default agent and that agent's main session when no originator is given.
// A preresolved agent that does not exist is never replaced
// by another: the message is dropped. Returns (nil, "") when no agent is
// available or the addressed one is gone.
func (al *AgentLoop) resolveSystemMessageTarget(msg bus.InboundMessage) (*AgentInstance, string) {
	var agent *AgentInstance
	if preresolved := inboundMetadata(msg, metadataKeyPreresolvedAgentID); preresolved != "" {
		a, ok := al.GetRegistry().Get(routing.NormalizeAgentID(preresolved))
		if !ok || a == nil {
			// Never another agent: the addressed one is gone.
			logger.WarnCF("agent", "System message dropped: addressed agent does not exist",
				map[string]any{"preresolved_agent_id": preresolved, "sender_id": msg.SenderID})
			return nil, ""
		}
		agent = a
	} else {
		agent = al.GetRegistry().Default()
	}
	if agent == nil {
		return nil, ""
	}

	// Any key resolves to the agent's one conversation.
	return agent, routing.ResolveAgentSessionKey(agent.ID, msg.SessionKey)
}
