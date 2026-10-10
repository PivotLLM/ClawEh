// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
)

// agentGoneText is the reply to a sender that required one from an agent that
// no longer exists.
func agentGoneText(agentID string) string {
	if agentID == "" {
		return "That agent no longer exists."
	}
	return "Agent " + agentID + " no longer exists."
}

// runTurn processes one (possibly merged) message under the turn budget and
// publishes the reply. The turn runs under turnParent (which /cancel may end);
// the reply is published under ctx, the loop's run context, so a cancelled turn
// can still say so.
func (al *AgentLoop) runTurn(ctx, turnParent context.Context, msg bus.InboundMessage) {
	// An ask nobody waits for any more (timed out, or its asker stopped) is
	// not answered: no model turn runs for it.
	if msg.Channel == constants.AgentMessageChannel && !al.asks.isWaiting(msg.ChatID) {
		logger.InfoCF("agent", "Ask skipped: the asker stopped waiting",
			map[string]any{"ask_id": msg.ChatID, "agent_id": inboundMetadata(msg, metadataKeyPreresolvedAgentID)})
		return
	}
	turnCtx, turnTimeout, cancel := al.turnContext(turnParent, msg)
	defer cancel()
	var roundSent atomic.Bool
	msgCtx := tools.WithRoundSentFlag(turnCtx, &roundSent)

	// outcome is set by the turn only when it did not end in a plain reply
	// (an empty one); failure and cancellation are decided from err.
	var outcome string
	response, err := al.processMessageSafely(msgCtx, msg, &outcome)
	if err != nil && shuttingDown(turnCtx) {
		al.endTurnOnShutdown(ctx, turnCtx, msg)
		return
	}

	action, text, outcome := decideTurnReply(turnEnd{
		response:      response,
		outcome:       outcome,
		err:           err,
		cause:         context.Cause(turnCtx),
		ask:           msg.Channel == constants.AgentMessageChannel,
		replyRequired: msg.ReplyRequired(),
		fromPerson:    fromPerson(msg),
		agentID:       inboundMetadata(msg, metadataKeyPreresolvedAgentID),
	})
	switch action {
	case replyDrop:
		return
	case replyDropAskCancelled:
		logger.InfoCF("agent", "Asked turn cancelled: the asker stopped waiting",
			turnFields(turnCtx, map[string]any{"ask_id": msg.ChatID, "agent_id": inboundMetadata(msg, metadataKeyPreresolvedAgentID)}))
		return
	case replyDropPersonAskerStopped:
		logger.InfoCF("agent", "Request to a person ended: the asker stopped waiting",
			turnFields(turnCtx, map[string]any{"ask_id": msg.ChatID}))
		return
	case replyTurnError:
		text = renderTurnErrorFor(al.turnAssistantName(msg), turnCtx, turnTimeout, err)
	case replySend:
	}

	// An ask's reply goes back to the asker, never to a channel.
	if msg.Channel == constants.AgentMessageChannel {
		if !al.deliverAskReply(msg, text, askReplyOutcome(err, outcome)) && err == nil && outcome == bus.OutcomeOK {
			// A person's answer that arrived as the asker stopped waiting:
			// they are told it is no longer needed rather than left unsure.
			al.humanAnswerUnused(ctx, msg)
		}
		return
	}

	// A system message's turn sent its reply to the chat it answers itself
	// (processSystemMessage); the system channel reaches no chat.
	if msg.Channel == "system" {
		return
	}
	al.publishInboundReply(ctx, turnCtx, msg, text, outcome, roundSent.Load())
}

// turnContext is the context a turn runs under, with the function that
// releases it. The overall turn budget is a hard backstop, so a hung
// provider or tool never leaves the user waiting forever: when it elapses
// the model/tool loop unwinds and the user is told (which also clears the
// typing indicator via the channel manager's preSend). The context carries
// one turn id, so every log line and audit row of the turn can be pulled
// together.
func (al *AgentLoop) turnContext(turnParent context.Context, msg bus.InboundMessage) (context.Context, time.Duration, context.CancelFunc) {
	turnTimeout := al.GetConfig().Agents.Defaults.GetTurnTimeout()
	// A request to a person may wait longer than a model turn would.
	turnTimeout = al.humanTurnBudget(msg, turnTimeout)
	turnCtx, turnCancel := context.WithTimeout(turnParent, turnTimeout)
	cancel := turnCancel
	// An ask whose asker gives up on it (the forum's) ends the turn then,
	// so its model call is aborted rather than finishing for no one. A
	// person's turn withdraws its request instead (askHuman).
	if msg.Channel == constants.AgentMessageChannel && al.humanTarget(msg) == nil {
		if w, ok := al.asks.wait(msg.ChatID); ok && w.stopTurn {
			var stopTurn context.CancelCauseFunc
			turnCtx, stopTurn = context.WithCancelCause(turnCtx)
			cancel = func() {
				stopTurn(nil)
				turnCancel()
			}
			go func(ctx context.Context) {
				select {
				case <-w.gone:
					stopTurn(errAskerStopped)
				case <-ctx.Done():
				}
			}(turnCtx)
		}
	}
	return withTurnID(turnCtx, newTurnID()), turnTimeout, cancel
}

// endTurnOnShutdown ends a turn shutdown interrupted. A model turn stays
// pending and is replayed on restart, so nothing is sent now. A request to a
// person is never replayed (the person may already have read it): it ends as
// cancelled, and a sender that requires a reply gets one.
func (al *AgentLoop) endTurnOnShutdown(ctx, turnCtx context.Context, msg bus.InboundMessage) {
	if al.humanTarget(msg) == nil {
		logger.InfoCF("agent", "Turn interrupted by shutdown; it is replayed on restart",
			turnFields(turnCtx, map[string]any{"channel": msg.Channel, "chat_id": msg.ChatID}))
		return
	}
	logger.InfoCF("agent", "Request to a person cancelled by shutdown",
		turnFields(turnCtx, map[string]any{"channel": msg.Channel, "chat_id": msg.ChatID}))
	const shutdownText = "The request was cancelled because the service is shutting down."
	if msg.Channel == constants.AgentMessageChannel {
		al.deliverAskReply(msg, shutdownText, bus.OutcomeCancelled)
		return
	}
	if !msg.ReplyRequired() {
		return
	}
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	if perr := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: msg.Channel, ChatID: msg.ChatID, OriginalMessageID: msg.MessageID,
		Content: shutdownText, Outcome: bus.OutcomeCancelled,
	}); perr != nil {
		logger.WarnCF("agent", "Failed to publish cancelled reply", map[string]any{"channel": msg.Channel, "error": perr.Error()})
	}
}

// turnAssistantName names the agent msg is routed to, for an error reply;
// empty when it cannot be resolved.
func (al *AgentLoop) turnAssistantName(msg bus.InboundMessage) string {
	route, _, err := al.resolveMessageRoute(msg)
	if err != nil {
		return ""
	}
	if a := al.GetConfig().AgentByID(route.AgentID); a != nil {
		return a.DisplayName()
	}
	return ""
}

// publishInboundReply sends a turn's reply to the chat its message came
// from. A required reply is always sent, even after msg_send replied in the
// turn; otherwise a reply msg_send already covered is skipped.
func (al *AgentLoop) publishInboundReply(ctx, turnCtx context.Context, msg bus.InboundMessage, text, outcome string, roundSent bool) {
	if !msg.ReplyRequired() && (text == "" || roundSent) {
		if roundSent && text != "" {
			logger.DebugCF("agent", "Skipped outbound (message tool already sent)",
				turnFields(turnCtx, map[string]any{"channel": msg.Channel}))
		}
		return
	}
	if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
		Channel:           msg.Channel,
		ChatID:            msg.ChatID,
		Content:           text,
		OriginalMessageID: msg.MessageID,
		Outcome:           outcome,
	}); err != nil {
		logger.WarnCF("agent", "Failed to publish outbound response",
			turnFields(turnCtx, map[string]any{
				"channel": msg.Channel,
				"chat_id": msg.ChatID,
				"error":   err.Error(),
			}))
		return
	}
	logger.InfoCF("agent", "Published outbound response",
		turnFields(turnCtx, map[string]any{
			"channel":     msg.Channel,
			"chat_id":     msg.ChatID,
			"content_len": len(text),
			"outcome":     outcome,
		}))
}

// turnEnd is how a turn ended, as decideTurnReply needs it.
type turnEnd struct {
	response string
	// outcome is what the turn reported when it did not end in a plain reply.
	outcome string
	err     error
	// cause is why the turn's context ended; nil while it is live.
	cause         error
	ask           bool // the message is an ask from another agent
	replyRequired bool
	fromPerson    bool
	agentID       string // the agent the message was addressed to, if any
}

// replyAction is what runTurn does with a turn's end.
type replyAction int

const (
	// replySend sends the decided text and outcome.
	replySend replyAction = iota
	// replyTurnError sends the turn's error, rendered for the user.
	replyTurnError
	// replyDrop sends nothing.
	replyDrop
	// replyDropAskCancelled sends nothing: the asked turn was cancelled
	// because its asker stopped waiting (askStoppingTurn).
	replyDropAskCancelled
	// replyDropPersonAskerStopped sends nothing: the asker stopped waiting
	// for a person's answer; it has its own outcome, and the person was told
	// (askHuman).
	replyDropPersonAskerStopped
)

// decideTurnReply maps how a turn ended to what its sender gets: the action,
// the reply text and the outcome.
func decideTurnReply(end turnEnd) (replyAction, string, string) {
	err := end.err
	switch {
	case errors.Is(err, errAgentGone):
		// Addressed to an agent that no longer exists (a deleted temporary
		// agent): dropped, logged where it was detected, and never handed to
		// another agent. A sender that requires a reply is told it failed.
		if !end.replyRequired {
			return replyDrop, "", ""
		}
		return replySend, agentGoneText(end.agentID), bus.OutcomeError
	case errors.As(err, new(humanNotAskedError)):
		// Dropped (logged where detected): a person takes only questions from
		// agents. Someone who wrote to it (a mention, a chat, a device) or a
		// sender that requires a reply is told so, which also clears the
		// chat's indicators; claw's own messages are dropped silently.
		if !end.replyRequired && !end.fromPerson {
			return replyDrop, "", ""
		}
		return replySend, err.Error(), bus.OutcomeError
	case err != nil && end.ask && errors.Is(end.cause, errAskerStopped):
		return replyDropAskCancelled, "", ""
	case errors.Is(err, errAskerStopped):
		return replyDropPersonAskerStopped, "", ""
	case errors.Is(err, errHumanCancelled):
		return replySend, err.Error(), bus.OutcomeCancelled
	case errors.As(err, new(humanUnreachableError)):
		// The request never reached the person's chat (logged in askHuman).
		return replySend, err.Error(), bus.OutcomeError
	case err != nil && errors.Is(end.cause, errCancelledByUser):
		return replySend, "⚠️ Cancelled by /cancel. Some steps may have completed — ask me to continue if needed.", bus.OutcomeCancelled
	case err != nil:
		return replyTurnError, end.response, bus.OutcomeError
	case end.response == "" || end.outcome == bus.OutcomeEmpty:
		if end.replyRequired {
			// The fallback advice is for a person; the sender gets the bare outcome.
			return replySend, "", bus.OutcomeEmpty
		}
		return replySend, end.response, bus.OutcomeEmpty
	case end.outcome == "":
		return replySend, end.response, bus.OutcomeOK
	default:
		return replySend, end.response, end.outcome
	}
}

// askReplyOutcome is the outcome an asker is given: a person's cancel or
// unreachable chat has its own, anything else is the turn's.
func askReplyOutcome(err error, outcome string) string {
	switch {
	case errors.Is(err, errHumanCancelled):
		return tools.OutcomePersonCancelled
	case errors.As(err, new(humanUnreachableError)):
		return tools.OutcomePersonUnreachable
	default:
		return outcome
	}
}

// publishCancelledReplies sends the final "cancelled" reply for each message
// that requires one (bus.MetaReplyRequired) and was dropped by /cancel before
// its turn ran. Messages without the flag get nothing.
func (al *AgentLoop) publishCancelledReplies(ctx context.Context, msgs []bus.InboundMessage) {
	for _, m := range msgs {
		if !m.ReplyRequired() {
			continue
		}
		if m.Channel == constants.AgentMessageChannel {
			al.deliverAskReply(m, "Cancelled by /cancel before it started.", bus.OutcomeCancelled)
			continue
		}
		if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Channel:           m.Channel,
			ChatID:            m.ChatID,
			Content:           "⚠️ Cancelled by /cancel before it started.",
			OriginalMessageID: m.MessageID,
			Outcome:           bus.OutcomeCancelled,
		}); err != nil {
			logger.WarnCF("agent", "Failed to publish cancelled reply",
				map[string]any{"channel": m.Channel, "chat_id": m.ChatID, "error": err.Error()})
		}
	}
}
