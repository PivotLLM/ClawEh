// ClawEh - Personal AI Assistant
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/commands"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/utils"
)

// sessionCancelState tracks a pending cancel request for a session. When
// pending is set, goroutines waiting for the session mutex will skip themselves
// rather than process, incrementing skipCount. The /cancel command reads and
// resets both fields.
type sessionCancelState struct {
	pending   atomic.Bool
	skipCount atomic.Int32
}

// getOrCreateSessionMu returns the per-session mutex for the given key,
// creating one if it does not already exist. This is safe for concurrent use.
func (al *AgentLoop) getOrCreateSessionMu(key string) *sync.Mutex {
	mu := &sync.Mutex{}
	actual, _ := al.sessionMus.LoadOrStore(key, mu)
	return actual.(*sync.Mutex)
}

// getOrCreateCancelState returns the per-session cancel state for the given key,
// creating one if it does not already exist.
func (al *AgentLoop) getOrCreateCancelState(key string) *sessionCancelState {
	cs := &sessionCancelState{}
	actual, _ := al.sessionCancelStates.LoadOrStore(key, cs)
	return actual.(*sessionCancelState)
}

// isCancelCommand returns true if the message content is a /cancel command.
func (al *AgentLoop) isCancelCommand(content string) bool {
	name, ok := commands.ParseCommandName(content)
	return ok && name == "cancel"
}

// processSessionMessage dispatches a single inbound message within its session's
// serialized goroutine. Messages sharing the same session scope key (resolved via
// resolveMessageRoute) are ordered via a per-session mutex so concurrent sessions
// never block each other, and so that multiple channel:chatID pairs that map to
// the same agent session are properly serialized.
func (al *AgentLoop) processSessionMessage(ctx context.Context, msg bus.InboundMessage) {
	defer al.activeRequests.Done()

	// Resolve the route before acquiring the mutex so that all channel:chatID
	// pairs that share the same agent session use the same mutex key. This
	// prevents concurrent LLM history reads/writes across unified sessions.
	var dispatchKey string
	route, _, routeErr := al.resolveMessageRoute(msg)
	if routeErr != nil {
		// Fall back to channel:chatID if routing fails; processMessage will
		// return the same error and report it to the user.
		dispatchKey = msg.Channel + ":" + msg.ChatID
	} else {
		scopeKey := resolveScopeKey(route, msg.SessionKey)
		dispatchKey = scopeKey
		// Pre-stamp the session key so processMessage's internal call to
		// resolveScopeKey returns the same value without re-doing routing.
		msg.SessionKey = scopeKey
	}

	isCancelCmd := al.isCancelCommand(msg.Content)
	if isCancelCmd {
		al.getOrCreateCancelState(dispatchKey).pending.Store(true)
	}

	mu := al.getOrCreateSessionMu(dispatchKey)
	mu.Lock()
	defer mu.Unlock()

	cs := al.getOrCreateCancelState(dispatchKey)
	if cs.pending.Load() && !isCancelCmd {
		cs.skipCount.Add(1)
		return
	}

	var roundSent atomic.Bool
	// Overall turn budget: a hard backstop so a hung provider or tool can never
	// leave the user waiting forever. When it elapses the context is cancelled,
	// the LLM/tool loop unwinds, and we deliver a clear message below (which also
	// clears the typing indicator via the channel manager's preSend).
	turnTimeout := al.GetConfig().Agents.Defaults.GetTurnTimeout()
	turnCtx, turnCancel := context.WithTimeout(ctx, turnTimeout)
	defer turnCancel()
	msgCtx := tools.WithRoundSentFlag(turnCtx, &roundSent)

	response, err := al.processMessageSafely(msgCtx, msg)
	if err != nil {
		response = renderTurnError(turnCtx, turnTimeout, err)
	}

	if response != "" && !roundSent.Load() {
		if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Channel:           msg.Channel,
			ChatID:            msg.ChatID,
			Content:           response,
			OriginalMessageID: msg.MessageID,
		}); err != nil {
			logger.WarnCF("agent", "Failed to publish outbound response",
				map[string]any{
					"channel": msg.Channel,
					"chat_id": msg.ChatID,
					"error":   err.Error(),
				})
		} else {
			logger.InfoCF("agent", "Published outbound response",
				map[string]any{
					"channel":     msg.Channel,
					"chat_id":     msg.ChatID,
					"content_len": len(response),
				})
		}
	} else if roundSent.Load() && response != "" {
		logger.DebugCF("agent", "Skipped outbound (message tool already sent)",
			map[string]any{"channel": msg.Channel})
	}
}

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
func (al *AgentLoop) HandleExternalMessage(_ context.Context, agentID, body string) error {
	cfg := al.GetConfig()
	if cfg == nil {
		return fmt.Errorf("configuration not loaded")
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
	}

	// Publish on a fresh bounded context (not the request context) so a client
	// that hangs up right after POSTing does not abort delivery — matching cron.
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pubCancel()
	return al.bus.PublishInbound(pubCtx, msg)
}

// RecordLastChannel records the last active channel for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChannel(channel string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChannel(channel)
}

// RecordLastChatID records the last active chat ID for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChatID(chatID string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChatID(chatID)
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
func (al *AgentLoop) processMessageSafely(ctx context.Context, msg bus.InboundMessage) (resp string, err error) {
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
	return al.processMessage(ctx, msg)
}

func (al *AgentLoop) processMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	logFields := map[string]any{
		"channel":     msg.Channel,
		"chat_id":     msg.ChatID,
		"sender_id":   msg.SenderID,
		"session_key": msg.SessionKey,
	}
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
	msg, hadAudio = al.transcribeAudioInMessage(ctx, msg)

	// For audio messages the placeholder was deferred by the channel.
	// Now that transcription (and optional feedback) is done, send it.
	if hadAudio && al.channelManager != nil {
		al.channelManager.SendPlaceholder(ctx, msg.Channel, msg.ChatID)
	}

	// Route system messages to processSystemMessage
	if msg.Channel == "system" {
		return al.processSystemMessage(ctx, msg)
	}

	// Extract agent mention from trigger prefix (e.g. "@alice do X" → routes to alice with "do X")
	{
		cfg := al.GetConfig()
		triggers := cfg.AgentMentions.Triggers
		if len(triggers) == 0 {
			triggers = []string{"@", "/", "."}
		}
		registry := al.GetRegistry()
		agentIDs := registry.ListAgentIDs()
		if mentionedAgent, stripped := channels.ExtractAgentMention(msg.Content, triggers, agentIDs); mentionedAgent != "" {
			msg.Content = stripped
			if msg.Metadata == nil {
				msg.Metadata = make(map[string]string)
			}
			msg.Metadata["mentioned_agent"] = mentionedAgent
		}
	}

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
		map[string]any{
			"agent_id":      agent.ID,
			"scope_key":     scopeKey,
			"session_key":   sessionKey,
			"matched_by":    route.MatchedBy,
			"route_agent":   route.AgentID,
			"route_channel": route.Channel,
		})

	userContent := prependSenderLabel(msg.Content, msg.Sender)
	// Drop received attachments into the agent's workspace so its file tools can read
	// them (the model otherwise only sees an annotation it can't open).
	userContent += al.materializeInboundMedia(msg, agent)

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
	}

	// context-dependent commands check their own Runtime fields and report
	// "unavailable" when the required capability is nil.
	if response, handled := al.handleCommand(ctx, msg, agent, &opts); handled {
		return response, nil
	}

	response, err := al.runAgentLoop(ctx, agent, opts)
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

func (al *AgentLoop) resolveMessageRoute(msg bus.InboundMessage) (routing.ResolvedRoute, *AgentInstance, error) {
	registry := al.GetRegistry()

	// Honor an explicit preresolved agent ID (set by trusted internal callers
	// such as the callback HTTP handler). This bypasses binding-based routing
	// so the message is delivered to the named agent regardless of which
	// bindings would otherwise match the channel/peer/account cascade.
	if preresolved := inboundMetadata(msg, metadataKeyPreresolvedAgentID); preresolved != "" {
		normalized := routing.NormalizeAgentID(preresolved)
		if agent, ok := registry.GetAgent(normalized); ok {
			route := routing.ResolvedRoute{
				AgentID:        normalized,
				Channel:        msg.Channel,
				AccountID:      routing.NormalizeAccountID(inboundMetadata(msg, metadataKeyAccountID)),
				SessionKey:     routing.BuildAgentMainSessionKey(normalized),
				MainSessionKey: routing.BuildAgentMainSessionKey(normalized),
				MatchedBy:      "preresolved",
			}
			return route, agent, nil
		}
		logger.WarnCF("agent", "preresolved_agent_id not registered; falling back to binding routing",
			map[string]any{"preresolved_agent_id": preresolved})
	}

	route := registry.ResolveRoute(routing.RouteInput{
		Channel:        msg.Channel,
		AccountID:      inboundMetadata(msg, metadataKeyAccountID),
		Peer:           extractPeer(msg),
		ParentPeer:     extractParentPeer(msg),
		GuildID:        inboundMetadata(msg, metadataKeyGuildID),
		TeamID:         inboundMetadata(msg, metadataKeyTeamID),
		MentionedAgent: inboundMetadata(msg, "mentioned_agent"),
	})

	agent, ok := registry.GetAgent(route.AgentID)
	if !ok {
		agent = registry.GetDefaultAgent()
	}
	if agent == nil {
		return routing.ResolvedRoute{}, nil, fmt.Errorf("no agent available for route (agent_id=%s)", route.AgentID)
	}

	return route, agent, nil
}

func resolveScopeKey(route routing.ResolvedRoute, msgSessionKey string) string {
	if msgSessionKey != "" && strings.HasPrefix(msgSessionKey, sessionKeyAgentPrefix) {
		return msgSessionKey
	}
	return route.SessionKey
}

func (al *AgentLoop) processSystemMessage(
	ctx context.Context,
	msg bus.InboundMessage,
) (string, error) {
	if msg.Channel != "system" {
		return "", fmt.Errorf(
			"processSystemMessage called with non-system message channel: %s",
			msg.Channel,
		)
	}

	logger.InfoCF("agent", "Processing system message",
		map[string]any{
			"sender_id": msg.SenderID,
			"chat_id":   msg.ChatID,
		})

	// Parse origin channel from chat_id (format: "channel:chat_id")
	var originChannel, originChatID string
	if idx := strings.Index(msg.ChatID, ":"); idx > 0 {
		originChannel = msg.ChatID[:idx]
		originChatID = msg.ChatID[idx+1:]
	} else {
		originChannel = "cli"
		originChatID = msg.ChatID
	}

	// Extract subagent result from message content
	// Format: "Task 'label' completed.\n\nResult:\n<actual content>"
	content := msg.Content
	if idx := strings.Index(content, "Result:\n"); idx >= 0 {
		content = content[idx+8:] // Extract just the result part
	}

	// Skip internal channels - only log, don't send to user
	if constants.IsInternalChannel(originChannel) {
		logger.InfoCF("agent", "Subagent completed (internal channel)",
			map[string]any{
				"sender_id":   msg.SenderID,
				"content_len": len(content),
				"channel":     originChannel,
			})
		return "", nil
	}

	agent, sessionKey := al.resolveSystemMessageTarget(msg)
	if agent == nil {
		return "", fmt.Errorf("no agent available for system message")
	}

	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         originChannel,
		ChatID:          originChatID,
		UserMessage:     fmt.Sprintf("[System: %s] %s", msg.SenderID, msg.Content),
		DefaultResponse: "Background task completed.",
		SendResponse:    true,
	})
}

// resolveSystemMessageTarget picks the agent and session a "system" message
// (e.g. an async tool/sub-agent completion) should be processed in. A spawn or
// other async tool carries the originating agent on preresolved_agent_id and its
// session on session_key, so the completion is handled by the SPAWNING agent in
// its own session — not whichever agent happens to be the default. Falls back to
// the default agent and that agent's main session when no originator is given
// (legacy behavior). Returns (nil, "") when no agent is available.
func (al *AgentLoop) resolveSystemMessageTarget(msg bus.InboundMessage) (*AgentInstance, string) {
	var agent *AgentInstance
	if preresolved := inboundMetadata(msg, metadataKeyPreresolvedAgentID); preresolved != "" {
		if a, ok := al.GetRegistry().GetAgent(routing.NormalizeAgentID(preresolved)); ok && a != nil {
			agent = a
		}
	}
	if agent == nil {
		agent = al.GetRegistry().GetDefaultAgent()
	}
	if agent == nil {
		return nil, ""
	}

	// Prefer the originator's session (so the completion lands in the conversation
	// that spawned the work); otherwise the agent's main session.
	sessionKey := strings.TrimSpace(msg.SessionKey)
	if sessionKey == "" || !strings.HasPrefix(sessionKey, sessionKeyAgentPrefix) {
		sessionKey = routing.BuildAgentMainSessionKey(agent.ID)
	}
	return agent, sessionKey
}

// extractPeer extracts the routing peer from the inbound message's structured Peer field.
func extractPeer(msg bus.InboundMessage) *routing.RoutePeer {
	if msg.Peer.Kind == "" {
		return nil
	}
	peerID := msg.Peer.ID
	if peerID == "" {
		if msg.Peer.Kind == "direct" {
			peerID = msg.SenderID
		} else {
			peerID = msg.ChatID
		}
	}
	return &routing.RoutePeer{Kind: msg.Peer.Kind, ID: peerID}
}

func inboundMetadata(msg bus.InboundMessage, key string) string {
	if msg.Metadata == nil {
		return ""
	}
	return msg.Metadata[key]
}

// senderLabel returns a short human-readable identifier for a sender, used to
// prefix group/channel messages so the LLM knows who sent each turn.
// Falls back gracefully: DisplayName → "@Username" → empty (caller decides).
func senderLabel(s bus.SenderInfo) string {
	if s.DisplayName != "" && s.Username != "" {
		return s.DisplayName + " (@" + s.Username + ")"
	}
	if s.DisplayName != "" {
		return s.DisplayName
	}
	if s.Username != "" {
		return "@" + s.Username
	}
	return ""
}

// prependSenderLabel attributes a message with its sender ("[From: <label>]").
// Applied to every message, direct chats included: under the default unified
// session scope, direct and group messages share one session, so a "private"
// message is just one of several senders in the shared context — the label keeps
// attribution unambiguous. Cheap, and the clarity is worth the few tokens. No-op
// when the sender yields no label.
func prependSenderLabel(content string, s bus.SenderInfo) string {
	label := senderLabel(s)
	if label == "" {
		return content
	}
	return "[From: " + label + "]\n" + content
}

// senderSource returns a human-readable source string for message archiving.
// Combines the display label with the canonical ID when both are available.
func senderSource(canonicalID string, s bus.SenderInfo) string {
	label := senderLabel(s)
	if label == "" {
		return canonicalID
	}
	if canonicalID == "" {
		return label
	}
	return label + " [" + canonicalID + "]"
}

// extractParentPeer extracts the parent peer (reply-to) from inbound message metadata.
func extractParentPeer(msg bus.InboundMessage) *routing.RoutePeer {
	parentKind := inboundMetadata(msg, metadataKeyParentPeerKind)
	parentID := inboundMetadata(msg, metadataKeyParentPeerID)
	if parentKind == "" || parentID == "" {
		return nil
	}
	return &routing.RoutePeer{Kind: parentKind, ID: parentID}
}
