// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
	"github.com/PivotLLM/ClawEh/utils"
)

// errAgentGone marks a message addressed to an agent that does not exist (a
// deleted temporary agent): it is dropped, never given to another agent.
var errAgentGone = errors.New("addressed agent does not exist")

// HandleExternalMessage delivers a raw external-message body to an agent as an
// unsolicited event — the same way a cron job fires (see schedule.ExecuteJob). It
// does NOT continue any existing conversation: it resolves the agent's default
// channel via CronTarget and publishes an inbound message there, so a monitoring
// webhook / GPS tracker / alarm reaches the agent even with no active chat.
//
// Returns ErrNoDefaultChannel (wrapped) when the agent has no default channel
// binding, so the HTTP layer can report a precondition (4xx) rather than a 500.
//
// The body is wrapped with the security prefix (so the model treats external
// input with caution) but the raw text is preserved intact. Delivery mirrors cron
// exactly: same CronTarget resolution and a fixed SenderID so downstream routing
// and dedupe treat it as a system-originated event.
func (al *AgentLoop) HandleExternalMessage(ctx context.Context, agentID, body string) error {
	cfg := al.GetConfig()
	if cfg == nil {
		return errors.New("configuration not loaded")
	}
	if cfg.IsHumanAgent(agentID) {
		return fmt.Errorf("%w: agent %q is a person and takes no external messages", ErrHumanAgent, agentID)
	}
	channel, chatID, peerKind, ok := cfg.CronTarget(agentID)
	if !ok {
		return fmt.Errorf("%w: agent %q — configure a default channel binding for it", ErrNoDefaultChannel, agentID)
	}

	prefix := global.DefaultMessagePrefix
	if cfg.Security.MessagePrefix != "" {
		prefix = cfg.Security.MessagePrefix
	}

	msg := bus.InboundMessage{
		Channel:  channel,
		SenderID: "webhook",
		ChatID:   chatID,
		Content:  prefix + body,
		Peer:     bus.Peer{Kind: peerKind, ID: chatID},
		Internal: true,
	}

	// Publish on a fresh bounded context (not the request context) so a client
	// that hangs up right after POSTing does not abort delivery — matching cron.
	pubCtx, pubCancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer pubCancel()
	return al.bus.PublishInbound(pubCtx, msg)
}

func (al *AgentLoop) ProcessDirect(
	ctx context.Context,
	content, sessionKey string,
) (string, error) {
	return al.ProcessDirectWithChannel(ctx, content, sessionKey, "cli", "direct", "direct")
}

func (al *AgentLoop) ProcessDirectWithChannel(
	ctx context.Context,
	content, sessionKey, channel, chatID, peerKind string,
) (string, error) {
	if err := al.ensureMCPInitialized(ctx); err != nil {
		return "", err
	}

	msg := bus.InboundMessage{
		Channel:    channel,
		SenderID:   "cron",
		ChatID:     chatID,
		Content:    content,
		SessionKey: sessionKey,
		Internal:   true,
	}
	// Set peer so channel-based bindings (e.g. a specific Slack channel mapped
	// to a named agent) are matched by the route resolver, exactly as they are
	// for live inbound messages.
	if chatID != "" && chatID != "direct" {
		kind := peerKind
		if kind == "" {
			kind = "channel"
		}
		msg.Peer = bus.Peer{Kind: kind, ID: chatID}
	}

	return al.processMessage(ctx, msg)
}

// processMessageSafely runs processMessage with a panic guard so a bug in the
// turn (or a tool) is converted into a returned error and delivered to the user
// instead of crashing the per-message goroutine and leaving the typing indicator
// stuck. processSessionMessage runs us in our own goroutine, so an unrecovered
// panic here would otherwise take down the process.
//
// outcome, when non-nil, receives how the turn ended when that was not a plain
// reply (see processOptions.OutcomeOut).
func (al *AgentLoop) processMessageSafely(ctx context.Context, msg bus.InboundMessage, outcome *string) (resp string, err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.ErrorCF("agent", "panic while processing message",
				map[string]any{
					"channel": msg.Channel,
					"chat_id": msg.ChatID,
					"panic":   fmt.Sprintf("%v", r),
					"stack":   string(debug.Stack()),
				})
			resp = ""
			err = fmt.Errorf("internal error while processing the message: %v", r)
		}
	}()
	return al.processMessageOutcome(ctx, msg, outcome)
}

func (al *AgentLoop) processMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	return al.processMessageOutcome(ctx, msg, nil)
}

// processMessageOutcome is processMessage reporting through outcome (may be
// nil) how the turn ended when that was not a plain reply.
func (al *AgentLoop) processMessageOutcome(ctx context.Context, msg bus.InboundMessage, outcome *string) (string, error) {
	// Direct callers (CLI, cron, external messages) do not come through runTurn;
	// give them a turn id too so their logs and audit rows are correlated.
	if turnIDFrom(ctx) == "" {
		ctx = withTurnID(ctx, newTurnID())
	}
	logFields := turnFields(ctx, map[string]any{
		"channel":     msg.Channel,
		"chat_id":     msg.ChatID,
		"sender_id":   msg.SenderID,
		"session_key": msg.SessionKey,
	})
	if logger.GetLogMessageContent() {
		var logContent string
		if strings.Contains(msg.Content, "Error:") || strings.Contains(msg.Content, "error") {
			logContent = msg.Content // Full content for errors
		} else {
			logContent = utils.Truncate(msg.Content, 80)
		}
		logFields["preview"] = logContent
	}
	logger.InfoCF(
		"agent",
		fmt.Sprintf("Processing message from %s:%s", msg.Channel, msg.SenderID),
		logFields,
	)

	var hadAudio bool
	// A request to a person is relayed as written: no transcription model
	// sees a human agent's conversation.
	if al.humanTarget(msg) == nil {
		msg, hadAudio = al.transcribeAudioInMessage(ctx, msg)
	}

	// For audio messages the placeholder was deferred by the channel.
	// Now that transcription (and optional feedback) is done, send it.
	if hadAudio && al.channelManager != nil {
		al.channelManager.SendPlaceholder(ctx, msg.Channel, msg.ChatID)
	}

	maxDepth := config.DefaultMaxSubagentDepth
	if cfg := al.GetConfig(); cfg != nil {
		maxDepth = cfg.Agents.Defaults.GetMaxSubagentDepth()
	}
	ctx = withInboundSpawnDepth(ctx, msg, maxDepth)
	ctx = withInboundAskChain(ctx, msg)

	// Route system messages to processSystemMessage
	if msg.Channel == "system" {
		return al.processSystemMessage(ctx, msg, outcome)
	}

	// Extract agent mention from trigger prefix (e.g. "@alice do X" → routes to alice with "do X")
	al.extractMention(&msg)

	route, agent, routeErr := al.resolveMessageRoute(msg)
	if routeErr != nil {
		return "", routeErr
	}

	// Detect whether a mention caused routing to a specific agent so we can
	// attribute the response. A mention is "honored" when the extracted agent
	// name matches the resolved agent ID (i.e. routing was overridden by the mention).
	mentionedAgent := inboundMetadata(msg, "mentioned_agent")
	mentionHonored := mentionedAgent != "" && strings.EqualFold(route.AgentID, mentionedAgent)

	// Legacy: reset the shared sentInRound flag on the message tool. The concurrent
	// dispatch path uses per-round context flags instead, so this is a no-op there.
	messageTool, hasMsg := agent.Tools.Get("message")
	if !hasMsg {
		messageTool, hasMsg = agent.Tools.Get("msg_send")
	}
	if hasMsg {
		tool := messageTool
		if resetter, ok := tool.(interface{ ResetSentInRound() }); ok {
			resetter.ResetSentInRound()
		}
	}

	// Resolve session key from route, while preserving explicit agent-scoped keys.
	scopeKey := resolveScopeKey(route, msg.SessionKey)
	sessionKey := scopeKey

	logger.InfoCF("agent", "Routed message",
		turnFields(ctx, map[string]any{
			"agent_id":      agent.ID,
			"scope_key":     scopeKey,
			"session_key":   sessionKey,
			"matched_by":    route.MatchedBy,
			"route_agent":   route.AgentID,
			"route_channel": route.Channel,
		}))

	userContent := prependSenderLabel(msg.Content, msg.Sender)
	// Drop received attachments into the agent's workspace so its file tools can read
	// them (the model otherwise only sees an annotation it can't open).
	// Not for a fresh temporary agent: it has no file tools to read them
	// with, and its workspace stays empty. The media refs still reach the
	// model with the message.
	if !agent.Spec.Fresh {
		userContent += al.materializeInboundMedia(msg, agent)
	}

	opts := processOptions{
		SessionKey:      sessionKey,
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		UserMessage:     userContent,
		Media:           msg.Media,
		DefaultResponse: defaultResponse,
		SendResponse:    false,
		IsRetry:         msg.IsRetry,
		ResetSession:    msg.Metadata[metaSessionReset] == "true",
		SenderID:        msg.SenderID,
		SenderName:      senderSource(msg.SenderID, msg.Sender),
		IsGroup:         inboundMetadata(msg, "is_group") == "true",
		OutcomeOut:      outcome,
		MessageID:       msg.MessageID,
		ReplyRequired:   msg.ReplyRequired(),
	}

	// context-dependent commands check their own Runtime fields and report
	// "unavailable" when the required capability is nil.
	if response, handled := al.handleCommand(ctx, msg, agent, &opts); handled {
		if agent.Spec.SingleShot() {
			// Whatever the command opened or wrote (a /compact, a session
			// reset) is not kept by an agent that keeps nothing.
			al.discardConversation(context.WithoutCancel(ctx), agent, sessionKey)
		}
		return response, nil
	}

	response, err := al.runMeteredTurn(ctx, agent, opts)
	if err != nil {
		return response, err
	}

	if mentionHonored && response != "" {
		displayName := agent.Name
		if displayName == "" {
			displayName = agent.ID
		}
		response = "**" + displayName + ":**\n" + response
	}

	return response, nil
}

// runMeteredTurn runs the turn with usage accounting and folds its cost into
// the day's spend total (see recordSpend).
func (al *AgentLoop) runMeteredTurn(ctx context.Context, agent *AgentInstance, opts processOptions) (string, error) {
	var usage global.TurnUsage
	opts.UsageOut = &usage
	response, err := al.runAgentLoop(ctx, agent, opts)
	al.recordSpend(usage.CostUSD)
	return response, err
}

// withInboundSpawnDepth applies the sender's sub-agent depth
// (bus.MetaSpawnDepth) to the turn's context. It only ever raises the depth:
// a value at or below the context's own is ignored, and an invalid one is
// ignored with a log line. A value above maxDepth (the configured
// max_subagent_depth) is clamped to it.
func withInboundSpawnDepth(ctx context.Context, msg bus.InboundMessage, maxDepth int) context.Context {
	raw := inboundMetadata(msg, bus.MetaSpawnDepth)
	if raw == "" {
		return ctx
	}
	depth, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || depth < 0 {
		logger.WarnCF("agent", "Ignoring invalid spawn depth on inbound message",
			map[string]any{"channel": msg.Channel, "chat_id": msg.ChatID, "spawn_depth": raw})
		return ctx
	}
	depth = min(depth, maxDepth)
	if depth <= toolsagents.SpawnDepth(ctx) {
		return ctx
	}
	return toolsagents.WithSpawnDepth(ctx, depth)
}
