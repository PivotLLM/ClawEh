// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
)

// webUIChannel is the WebUI chat's channel name. Its sender has no name of
// its own (only an internal id), so commandSender names it webUISender.
const (
	webUIChannel = "webui"
	webUISender  = "the WebUI user"
)

// commandSender names the sender of a chat message for the agent it asks or
// whispers to.
func commandSender(msg bus.InboundMessage) string {
	if msg.Channel == webUIChannel {
		return webUISender
	}
	if label := senderLabel(msg.Sender); label != "" {
		return label
	}
	if msg.SenderID != "" {
		return msg.SenderID
	}
	return msg.Channel
}

// senderMayReach reports whether the sender of msg may talk to agentID: msg
// is routed to it, or a binding matching msg's channel, account and chat
// routes to it or lets it be mentioned.
func (al *AgentLoop) senderMayReach(msg bus.InboundMessage, agentID string) bool {
	if route, _, err := al.resolveMessageRoute(msg); err == nil && route.AgentID == agentID {
		return true
	}
	cfg := al.GetConfig()
	if cfg == nil {
		return false
	}
	return routing.NewRouteResolver(cfg).Reaches(routing.RouteInput{
		Channel:    msg.Channel,
		AccountID:  inboundMetadata(msg, metadataKeyAccountID),
		Peer:       extractPeer(msg),
		ParentPeer: extractParentPeer(msg),
		GuildID:    inboundMetadata(msg, metadataKeyGuildID),
		TeamID:     inboundMetadata(msg, metadataKeyTeamID),
	}, agentID)
}

// commandTarget resolves the agent a /ask or /whisper names (by id or name,
// config agents only) and checks the sender may talk to it. It returns the
// agent, or the reply to give instead.
func (al *AgentLoop) commandTarget(command string, msg bus.InboundMessage, ref string) (*AgentInstance, string) {
	cfg := al.GetConfig()
	var target *AgentInstance
	if cfg != nil {
		if ac := cfg.FindAgent(ref); ac != nil {
			target, _ = al.GetRegistry().GetConfigured(ac.ID)
		}
	}
	if target == nil {
		return nil, fmt.Sprintf("There is no agent named %s.", ref)
	}
	if !al.senderMayReach(msg, target.ID) {
		logger.WarnCF("agent", "Agent message refused: the sender may not talk to the agent",
			map[string]any{"command": command, "agent_id": target.ID, "channel": msg.Channel, "sender_id": msg.SenderID})
		return nil, fmt.Sprintf("You don't have permission to /%s %s.", command, target.DisplayName())
	}
	return target, ""
}

// commandWhisper runs /whisper <agent> <text> for msg's sender.
func (al *AgentLoop) commandWhisper(ctx context.Context, msg bus.InboundMessage, ref, text string) string {
	if tools.AgentMessageTooLong(text) {
		return tools.AgentMessageLimitText()
	}
	target, refusal := al.commandTarget("whisper", msg, ref)
	if target == nil {
		return refusal
	}
	name := target.DisplayName()
	from := sender{name: commandSender(msg), note: personNote("whisper", msg.Channel)}
	if err := al.whisper(ctx, from, target.ID, text); err != nil {
		logger.WarnCF("agent", "/whisper failed",
			map[string]any{"agent_id": target.ID, "channel": msg.Channel, "error": err.Error()})
		return fmt.Sprintf("Could not whisper to %s.", name)
	}
	return "Whispered to " + name + "."
}

// commandAsk runs /ask <agent> <text> for msg's sender. The ask runs in the
// background, waiting up to the agent's request_timeout (the turn timeout
// when none is set), so the chat is not held; the answer is posted to the
// chat as "<agent>: <reply>". It returns the reply to give now: empty when
// the ask was sent.
func (al *AgentLoop) commandAsk(ctx context.Context, msg bus.InboundMessage, ref, text string) string {
	if tools.AgentMessageTooLong(text) {
		return tools.AgentMessageLimitText()
	}
	target, refusal := al.commandTarget("ask", msg, ref)
	if target == nil {
		return refusal
	}
	wait := al.requestTimeoutFor(target)
	if wait <= 0 {
		if cfg := al.GetConfig(); cfg != nil {
			wait = cfg.Agents.Defaults.GetTurnTimeout()
		} else {
			wait = config.DefaultTurnTimeout
		}
	}
	from := sender{name: commandSender(msg), note: personNote("ask", msg.Channel)}
	// The ask outlives the command's turn, so it is not cancelled with it, but
	// it stops when the service does. It keeps the turn's values: a person's
	// message carries no sub-agent depth and nobody waits on it, so the ask
	// starts at depth 0 with an empty chain, and it is marked remote when the
	// chat is.
	// It holds no turn slot: the command's turn ends before it does.
	askCtx, cancel := context.WithCancel(withTurnSlot(context.WithoutCancel(ctx), nil))
	stop := func() bool { return false }
	if runCtx := al.runContext(); runCtx != nil {
		stop = context.AfterFunc(runCtx, cancel) //nolint:contextcheck // the run context only ends the ask; its values come from the command's turn
	}
	go func() {
		defer cancel()
		defer stop()
		reply, err := al.ask(askCtx, from, target.ID, text, wait, false)
		if errors.Is(err, context.Canceled) {
			logger.InfoCF("agent", "/ask abandoned: the service is stopping",
				map[string]any{"agent_id": target.ID, "channel": msg.Channel, "chat_id": msg.ChatID})
			return
		}
		if err != nil && !errors.Is(err, tools.ErrMaxDepth) && !errors.Is(err, tools.ErrAskLoop) {
			logger.WarnCF("agent", "/ask failed",
				map[string]any{"agent_id": target.ID, "channel": msg.Channel, "chat_id": msg.ChatID, "error": err.Error()})
		}
		content := commandAskReply(target.DisplayName(), reply, err)
		pubCtx, cancel := context.WithTimeout(askCtx, publishTimeout)
		defer cancel()
		if pubErr := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Channel:           msg.Channel,
			ChatID:            msg.ChatID,
			Content:           content,
			OriginalMessageID: msg.MessageID,
		}); pubErr != nil {
			logger.WarnCF("agent", "Failed to post the /ask reply",
				map[string]any{"channel": msg.Channel, "chat_id": msg.ChatID, "error": pubErr.Error()})
		}
	}()
	return ""
}

// commandAskReply renders an ask's result for the chat that sent /ask: one
// plain sentence naming the agent, never the underlying error (the caller
// logs it).
func commandAskReply(name string, reply tools.AgentReply, err error) string {
	switch {
	case errors.Is(err, tools.ErrMaxDepth):
		return fmt.Sprintf("Could not ask %s: the maximum sub-agent depth is reached.", name)
	case errors.Is(err, tools.ErrAskLoop):
		return fmt.Sprintf("Could not ask %s: it is waiting for a reply in this exchange.", name)
	case err != nil:
		return fmt.Sprintf("Could not ask %s.", name)
	case reply.Outcome == tools.OutcomeTimeout || reply.Outcome == tools.OutcomePersonCancelled ||
		reply.Outcome == tools.OutcomePersonUnreachable:
		return reply.Text
	case strings.TrimSpace(reply.Text) == "":
		return name + " gave no reply."
	default:
		return name + ": " + reply.Text
	}
}
