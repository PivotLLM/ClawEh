// ClawEh
// License: MIT

package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/PivotLLM/cogmem"
	"github.com/PivotLLM/ctxengine"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
	"github.com/PivotLLM/ClawEh/utils"
)

// processOptions configures how a message is processed
type processOptions struct {
	SessionKey      string            // Session identifier for history/context
	Channel         string            // Target channel for tool execution
	ChatID          string            // Target chat ID for tool execution
	UserMessage     string            // User message content (may include prefix)
	Media           []string          // media:// refs from inbound message
	DefaultResponse string            // Response when LLM returns empty
	SendResponse    bool              // Whether to send response via bus
	IsRetry         bool              // True when message is a /retry retrigger (skip AddMessage)
	ResetSession    bool              // True when this message is a session_clear handoff: reset before handling
	SenderID        string            // Originating sender identifier for source attribution
	SenderName      string            // Human-readable sender label (display name + canonical ID)
	IsGroup         bool              // True when the inbound message came from a group/multi-listener chat
	IterationsOut   *int              // optional: runAgentLoop writes the LLM iteration count here
	UsageOut        *global.TurnUsage // optional: every successful LLM call's accounting is added here
	// OutcomeOut, when set, receives bus.OutcomeEmpty when the model produced
	// no reply (whatever fallback text is returned) or bus.OutcomeError for an
	// abnormal empty termination. Left untouched for a normal reply, and on an
	// error return (the caller decides that outcome from the error).
	OutcomeOut *string
	// MessageID and ReplyRequired describe the inbound message the turn
	// answers; they are recorded with the pending turn so a restart's replay
	// still owes the sender its reply.
	MessageID     string
	ReplyRequired bool
	// OnReplyDelivery, when set with SendResponse, is told how the delivery
	// of the reply ended (bus.OutboundMessage.OnDelivery), with the reply.
	// It must not block.
	OnReplyDelivery func(reply string, err error)
}

// setOutcome records how the turn ended in opts.OutcomeOut, when requested.
func (opts processOptions) setOutcome(outcome string) {
	if opts.OutcomeOut != nil {
		*opts.OutcomeOut = outcome
	}
}

// runAgentLoop is the core message processing logic: one turn of agent on
// opts, from saving the user's message to the reply.
func (al *AgentLoop) runAgentLoop(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
) (string, error) {
	// Every provider call of the turn names the agent in its error logs,
	// including the compression paths that fan out from here before
	// runLLMIteration is reached.
	ctx = providers.WithAgentID(ctx, agent.ID)

	agent, endTurn, err := al.beginAgentTurn(agent, opts.SessionKey)
	if err != nil {
		return "", err
	}
	defer endTurn()

	// A human agent's turn is answered by the person, never by a model.
	if agent.HumanModel != "" {
		return al.runHumanTurn(ctx, agent, opts)
	}

	// A single-shot agent keeps nothing between turns: its conversation is
	// discarded once the turn is over (after the context manager is released
	// below), however the turn ended. That includes a turn shutdown
	// interrupted: restart recovery replays config agents' turns only, never a
	// temporary agent's, so nothing would ever read it again.
	if agent.Spec.SingleShot() {
		defer al.discardConversation(context.WithoutCancel(ctx), agent, opts.SessionKey)
	}

	// The context manager and the cognitive-memory session (nil for an agent
	// without it) of this session.
	cm, mem, releaseCtxMgr := al.getSessionContext(agent, opts.SessionKey) //nolint:contextcheck // compaction reporter: ctxengine's callback has no context, so it publishes on its own
	defer releaseCtxMgr()

	// After the session context, so the session's token exists.
	ctx, restoreSource := al.scopeSessionToken(ctx, agent, opts)
	defer restoreSource()

	if err = al.prepareSession(ctx, agent, cm, &opts); err != nil {
		return "", err
	}
	// A retried message is already in history.
	if !opts.IsRetry {
		if err = al.saveUserMessage(ctx, agent, opts, cm, mem); err != nil {
			return "", err
		}
	}
	messages, err := al.turnRequest(ctx, agent, opts, cm, mem)
	if err != nil {
		return "", err
	}

	al.markTurnPending(ctx, agent, opts)
	// A turn shutdown cancelled stays pending, so a restart replays it.
	interrupted := false
	defer func() {
		if !interrupted {
			al.clearTurnPending(agent, opts.SessionKey)
		}
	}()

	var reply modelReply
	reply.content, reply.normal, reply.degenerate, reply.finishReason, reply.iterations, err = al.runLLMIteration(ctx, agent, messages, opts, cm, mem)
	if err != nil {
		interrupted = shuttingDown(ctx)
		return "", err
	}
	return al.finishTurn(ctx, agent, opts, cm, mem, reply)
}

// beginAgentTurn marks agent in a turn: a temporary agent is never deleted
// mid-turn, its idle time counts from the end of its last turn, and an
// instance a reload replaced is closed only once no turn runs on it. An
// instance resolved before a reload replaced it is closed (or about to be),
// so the turn runs on the current instance instead, and is dropped with
// errAgentGone only when the agent is gone. It returns the instance the turn
// runs on and the function that ends the turn.
func (al *AgentLoop) beginAgentTurn(agent *AgentInstance, sessionKey string) (*AgentInstance, func(), error) {
	registry := al.GetRegistry()
	if registry == nil {
		return agent, func() {}, nil
	}
	endTurn, current := registry.BeginTurn(agent.ID, agent)
	if current {
		return agent, endTurn, nil
	}
	if fresh, ok := registry.Get(agent.ID); ok {
		agent = fresh
		endTurn, current = registry.BeginTurn(agent.ID, agent)
	}
	if !current {
		logger.WarnCF("agent", "Turn dropped: the agent no longer exists",
			map[string]any{"agent_id": agent.ID, "agent": agent.Label(), "session_key": sessionKey})
		return agent, nil, errAgentGone
	}
	return agent, endTurn, nil
}

// scopeSessionToken records the turn on the session's token, so a CLI
// provider's MCP tool calls, which bypass the agent loop, act within it: the
// turn's sub-agent depth (every turn, so a later ordinary turn resets it),
// the agents waiting on the turn (none of which may be asked from it; see
// Ask), and the chat to send user-facing output to. It returns ctx with the
// running agent added to the ask chain, and the function that puts the
// token's source back after an asked turn.
func (al *AgentLoop) scopeSessionToken(ctx context.Context, agent *AgentInstance, opts processOptions) (context.Context, func()) {
	al.mu.RLock()
	sti := al.sessionTokenIssuer
	al.mu.RUnlock()
	if sti != nil {
		sti.SetDepth(opts.SessionKey, toolsagents.SpawnDepth(ctx))
	}
	ctx = tools.WithAskChainAgent(ctx, agent.ID)
	if sti == nil {
		return ctx, func() {}
	}
	sti.SetTurnScope(opts.SessionKey, tools.AskChain(ctx))

	// The source follows the user across channels. Internal channels have no
	// channel handler, so they are not recorded: the MCP ForUser publish path
	// drops on the empty channel/chat guard instead.
	if !constants.IsInternalChannel(opts.Channel) {
		sti.SetSource(opts.SessionKey, opts.Channel, opts.ChatID)
	}
	if opts.Channel != constants.AgentMessageChannel {
		return ctx, func() {}
	}
	// An asked turn's MCP output (ForUser, breadcrumbs, msg_send with no
	// target, background results) belongs to the ask, never to the chat the
	// session last heard from: the source points at the ask for the turn and
	// is put back after it.
	prevChannel, prevChatID := sti.Source(opts.SessionKey)
	sti.SetSource(opts.SessionKey, opts.Channel, opts.ChatID)
	return ctx, func() { sti.SetSource(opts.SessionKey, prevChannel, prevChatID) }
}

// prepareSession readies the session for the turn's message, adjusting opts:
// a session_clear handoff resets it, a single-shot turn starts blank, and
// whispers held for the agent open its message.
func (al *AgentLoop) prepareSession(ctx context.Context, agent *AgentInstance, cm ctxengine.ContextManager, opts *processOptions) error {
	// session_clear publishes an inbound tagged metaSessionReset. The reset
	// happens here, at a clean turn boundary, before the notice is saved, so
	// this turn starts on a clean (archive-preserved) context; the reissued
	// session token reaches the model in this turn's system prompt.
	if opts.ResetSession {
		if err := cm.Reset(ctx); err != nil {
			logger.WarnCF("agent", "session_clear: Reset failed", map[string]any{
				"session_key": opts.SessionKey,
				"error":       err.Error(),
			})
		}
		// The cleared history no longer references any media; let its files age out.
		al.releaseSessionPins(opts.SessionKey)
		al.reissueSessionToken(agent, opts.SessionKey)
	}

	// Whatever a previous single-shot turn left behind (its discard skipped
	// because the session was in use) is cleared, and a retried message is
	// saved again as the turn's only message.
	if agent.Spec.SingleShot() {
		if err := cm.Reset(ctx); err != nil {
			return fmt.Errorf("single-shot reset: %w", err)
		}
		opts.IsRetry = false
	}

	// Whispers open the agent's next message, once. A retried message is
	// already in history, so they wait for the next one.
	if !opts.IsRetry {
		opts.UserMessage = al.prependWhispers(agent, opts.UserMessage)
	}
	return nil
}

// saveUserMessage stores the turn's user message, which may trigger
// compression, and observes it into cognitive memory.
func (al *AgentLoop) saveUserMessage(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
	cm ctxengine.ContextManager,
	mem *cogmem.Session,
) error {
	userMsg := providers.Message{Role: "user", Content: opts.UserMessage}
	if opts.SenderName != "" {
		userMsg.Source = opts.SenderName
	} else if opts.SenderID != "" {
		userMsg.Source = opts.SenderID
	}
	if strings.HasPrefix(opts.SenderID, "callback") {
		userMsg.Type = "callback"
	}
	if len(opts.Media) > 0 {
		al.attachInboundMedia(ctx, agent, opts, &userMsg)
	}
	seq, err := cm.AddUserMessage(ctx, userMsg)
	if err != nil {
		return al.sessionStoreWriteFailed(agent, opts.SessionKey, "user message", err)
	}
	mem.Observe(ctx, seq, userMsg.Role, userMsg.Content)
	return nil
}

// attachInboundMedia puts the inbound message's media on userMsg.
func (al *AgentLoop) attachInboundMedia(ctx context.Context, agent *AgentInstance, opts processOptions, userMsg *providers.Message) {
	userMsg.Media = opts.Media
	// A text-only primary with a vision side-model configured gets the
	// image(s) described once here, the description folded into the text
	// (clearing Media), so every dispatch carries usable text instead of an
	// image the model cannot see. No-op for vision-capable primaries and when
	// vision is not configured.
	al.describeInboundMedia(ctx, agent, userMsg)
	// The media:// refs stay visible in the stored text and are pinned for
	// the session, so TTL cleanup does not reap files the history still
	// references. They are actionable: agent_spawn's media parameter accepts
	// them, so even a text-only primary can hand the original attachment to a
	// vision-capable worker.
	if refs := mediaRefsIn(opts.Media); len(refs) > 0 {
		al.pinSessionMediaRefs(opts.SessionKey, refs)
		userMsg.Content = appendMediaRefMarker(userMsg.Content, refs)
	}
}

// turnRequest assembles the turn's first request: eviction sweep, safety-net
// compaction on both stored history and the built request, and memory placed
// from the message the user just sent. Media refs are then resolved for
// dispatch.
func (al *AgentLoop) turnRequest(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
	cm ctxengine.ContextManager,
	mem *cogmem.Session,
) ([]providers.Message, error) {
	asm, err := cm.Assemble(ctx, al.assembleRequest(ctx, agent, mem, opts))
	if err != nil {
		return nil, fmt.Errorf("context manager assemble: %w", err)
	}
	messages := asm.Messages

	// Pins follow what the (possibly compacted) context still references, so
	// refs compacted out become reapable. Must run before resolveMediaRefs
	// replaces refs with data URLs in the dispatch copy.
	al.reconcileSessionPins(opts.SessionKey, messages)

	// Images become base64 data URLs; other media become local paths in the
	// content.
	maxMediaSize := al.GetConfig().Agents.Defaults.GetMaxMediaSize()
	return resolveMediaRefs(messages, al.mediaStore, maxMediaSize), nil
}

// markTurnPending marks the turn in flight, so a restart can detect an
// interrupted model call and replay it.
func (al *AgentLoop) markTurnPending(ctx context.Context, agent *AgentInstance, opts processOptions) {
	if err := agent.Sessions.SetPendingTurn(opts.SessionKey); err != nil {
		logger.WarnCF("agent", "Failed to set pending turn flag",
			map[string]any{"error": err.Error(), "session": opts.SessionKey})
	}
	al.recordPendingTurnSource(ctx, agent, opts)
}

// clearTurnPending clears what markTurnPending recorded.
func (al *AgentLoop) clearTurnPending(agent *AgentInstance, sessionKey string) {
	if err := agent.Sessions.ClearPendingTurn(sessionKey); err != nil {
		logger.WarnCF("agent", "Failed to clear pending turn flag",
			map[string]any{"error": err.Error(), "session": sessionKey})
	}
	al.clearPendingTurnSource(agent.ID, sessionKey)
}

// modelReply is how the model loop of a turn ended.
type modelReply struct {
	content      string
	normal       bool // the last response ended normally
	degenerate   bool // the reply stayed empty after every poke
	finishReason string
	iterations   int
}

// finishTurn turns the model's reply into the turn's: intentional silence
// and empty replies are handled, a real reply is saved to the session, and
// the reply is published when opts asks for it.
func (al *AgentLoop) finishTurn(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
	cm ctxengine.ContextManager,
	mem *cogmem.Session,
	reply modelReply,
) (string, error) {
	// The no-response sentinel (e.g. a group message addressed to someone
	// else) is intentional silence: the typing indicator is cleared and
	// nothing is sent, unlike a degenerate empty reply.
	if isNoResponseSentinel(reply.content) {
		logger.DebugCF("agent", "LLM declined to respond (no-response sentinel)",
			map[string]any{"agent_id": agent.ID, "session_key": opts.SessionKey})
		al.stopTyping(opts.Channel, opts.ChatID)
		opts.setOutcome(bus.OutcomeEmpty)
		return "", nil
	}

	content := reply.content
	isSystemError := false
	if content == "" {
		var outcome string
		content, outcome = al.emptyReply(agent, opts, reply)
		opts.setOutcome(outcome)
		if content == "" {
			al.stopTyping(opts.Channel, opts.ChatID)
			return "", nil
		}
		isSystemError = true
	}

	// A system error string is not the model's reply, so it is not saved.
	if !isSystemError {
		if err := al.saveAssistantReply(ctx, agent, opts.SessionKey, cm, mem, content); err != nil {
			return "", err
		}
	}
	if opts.SendResponse {
		al.publishTurnReply(ctx, opts, content)
	}

	// Content is logged only with log_message_content, for privacy.
	logMsg := "Response"
	if logger.GetLogMessageContent() {
		logMsg = "Response: " + utils.Truncate(content, 120)
	}
	logger.InfoCF("agent", logMsg,
		map[string]any{
			"agent_id":     agent.ID,
			"session_key":  opts.SessionKey,
			"iterations":   reply.iterations,
			"final_length": len(content),
			"system_error": isSystemError,
		})

	if opts.IterationsOut != nil {
		*opts.IterationsOut = reply.iterations
	}
	return content, nil
}

// emptyReply decides what an empty model reply becomes: the text sent in its
// place and the turn's outcome. An empty text means the turn stays silent.
func (al *AgentLoop) emptyReply(agent *AgentInstance, opts processOptions, reply modelReply) (string, string) {
	fields := map[string]any{
		"agent_id":      agent.ID,
		"session_key":   opts.SessionKey,
		"finish_reason": reply.finishReason,
	}
	switch {
	case reply.normal && !reply.degenerate && opts.IsGroup:
		// An empty, normal reply in a group chat means the message was not for
		// this agent (e.g. @-directed at another).
		logger.DebugCF("agent", "empty normal response in group; staying silent", fields)
		return "", bus.OutcomeEmpty
	case reply.degenerate:
		// Reasoning but no reply, even after poking.
		logger.WarnCF("agent", "degenerate empty response; sending fallback reply", fields)
		return "Sorry — I couldn't compose a reply to that. Please try again.", bus.OutcomeEmpty
	case reply.normal:
		// A direct chat expects an answer, so an empty reply is a failure
		// (e.g. a degraded fallback model returning nothing): the user is
		// told rather than left waiting.
		logger.WarnCF("agent", "empty response on a direct message; advising the user", fields)
		return "The model returned an empty response. This can happen when a fallback model can't handle the request — please try again.", bus.OutcomeEmpty
	default:
		return fmt.Sprintf("The AI provider returned an empty response (finish reason: %s). Check provider logs for details.", reply.finishReason), bus.OutcomeError
	}
}

// saveAssistantReply stores the turn's final reply and saves the session.
func (al *AgentLoop) saveAssistantReply(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
	cm ctxengine.ContextManager,
	mem *cogmem.Session,
	content string,
) error {
	finalMsg := providers.Message{Role: "assistant", Content: content}
	seq, err := cm.AddAssistantMessage(ctx, finalMsg)
	if err != nil {
		return al.sessionStoreWriteFailed(agent, sessionKey, "assistant reply", err)
	}
	mem.Observe(ctx, seq, finalMsg.Role, finalMsg.Content)
	if err := agent.Sessions.Save(sessionKey); err != nil {
		logger.WarnCF("agent", "Failed to save session",
			map[string]any{"error": err.Error(), "session": sessionKey})
		al.Alerter().Send(alerter.Alert{
			Title:       "Session not saved",
			Description: "conversation history is being lost (disk full or unwritable?)",
			Details:     err.Error(),
			EventID:     "session-store",
		})
	}
	return nil
}

// publishTurnReply sends the reply to the turn's chat, reporting its
// delivery to opts.OnReplyDelivery.
func (al *AgentLoop) publishTurnReply(ctx context.Context, opts processOptions, content string) {
	var onDelivery func(error)
	if opts.OnReplyDelivery != nil {
		onDelivery = func(err error) { opts.OnReplyDelivery(content, err) }
	}
	if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
		Channel:    opts.Channel,
		ChatID:     opts.ChatID,
		Content:    content,
		OnDelivery: onDelivery,
	}); err != nil {
		logger.WarnCF("agent", "Failed to publish response",
			map[string]any{"error": err.Error(), "channel": opts.Channel, "session": opts.SessionKey})
	}
}

// noResponseSentinel is the reply a model uses to decline responding (e.g. a
// group message addressed to someone else). It is treated as intentional
// silence: the typing indicator is cleared, nothing is sent, and the empty-
// response poke/fallback does NOT fire (distinguishing it from a real failure).
const noResponseSentinel = "!none"

// isNoResponseSentinel reports whether content is the model's "decline to reply"
// signal. Tolerant of surrounding quotes/backticks, whitespace, and any trailing
// text after the token — e.g. `"!none"`, "!none.", "!none — that's for Bob" all
// count — but not a longer word that merely starts with it (e.g. "!nonexistent").
func isNoResponseSentinel(content string) bool {
	s := strings.TrimSpace(content)
	// Strip any leading quotes/backticks the model may have wrapped it in; the
	// boundary check below tolerates a trailing quote/punctuation/text.
	s = strings.TrimSpace(strings.TrimLeft(s, "\"'`"))
	s = strings.ToLower(s)
	if !strings.HasPrefix(s, noResponseSentinel) {
		return false
	}
	rest := s[len(noResponseSentinel):]
	if rest == "" {
		return true
	}
	// The next character must be a boundary (not a letter/digit), so a longer
	// token like "!nonexistent" does not match.
	c := rest[0]
	alnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
	return !alnum
}

// summarizeEvictions renders one consolidated notice for all of a turn's
// evictions: total count, total bytes freed, and a per-resource breakdown (top
// few by count). Keeps the chat to a single line even when an agent re-reads the
// same file every iteration; per-eviction detail lives in the DEBUG log.
func summarizeEvictions(events []ctxengine.EvictionEvent) string {
	totalBytes := 0
	counts := map[string]int{}
	var order []string
	for _, e := range events {
		totalBytes += e.Bytes
		if _, seen := counts[e.Resource]; !seen {
			order = append(order, e.Resource)
		}
		counts[e.Resource]++
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })

	const maxRes = 3
	parts := make([]string, 0, maxRes+1)
	for i, res := range order {
		if i >= maxRes {
			parts = append(parts, fmt.Sprintf("+%d more", len(order)-maxRes))
			break
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", capEvictResource(res), counts[res]))
	}
	return fmt.Sprintf("🧹 Context evicted %d read(s) %s — %s",
		len(events), humanBytes(totalBytes), strings.Join(parts, ", "))
}

func humanBytes(n int) string {
	if n >= 1024 {
		return fmt.Sprintf("%.0f KB", float64(n)/1024.0)
	}
	return fmt.Sprintf("%d B", n)
}

// capEvictResource shortens an evicted resource for the notice: the file basename
// (paths are noise in a one-line notice), capped for a very long name.
func capEvictResource(s string) string {
	base := filepath.Base(s)
	const maxLen = 48
	if len(base) > maxLen {
		return base[:maxLen-1] + "…"
	}
	return base
}

// evictionNotifyUser reports whether the agent's resolved eviction policy has
// notify_user enabled, so the loop can surface a consolidated notice at the end
// of the turn. The DEBUG log of evictions is unconditional and happens inside
// the sweep.
func (al *AgentLoop) evictionNotifyUser(agent *AgentInstance) bool {
	cfg := al.GetConfig()
	p := ctxengine.DefaultEvictionPolicy()
	applyEvictionConfig(&p, cfg.Agents.Defaults.ContextEviction)
	if agent != nil && agent.Config != nil {
		applyEvictionConfig(&p, agent.Config.ContextEviction)
	}
	return p.NotifyUser
}

// assembleRequest builds the per-dispatch request for the context manager:
// the system-prompt layers for this conversation, the cost of the tool schemas
// this dispatch will send, the memory blocks recalled for the user's message
// this turn, and the channel the compaction reporter should answer on. mem
// may be nil.
func (al *AgentLoop) assembleRequest(ctx context.Context, agent *AgentInstance, mem *cogmem.Session, opts processOptions) ctxengine.AssembleRequest {
	defs := agent.Tools.ToProviderDefs()
	if agent.NoTools {
		defs = nil
	}
	return al.assembleRequestWithDefs(ctx, agent, mem, opts, defs)
}

// assembleRequestWithDefs is assembleRequest for a caller that already holds
// this dispatch's tool definitions.
func (al *AgentLoop) assembleRequestWithDefs(ctx context.Context, agent *AgentInstance, mem *cogmem.Session, opts processOptions, defs []providers.ToolDefinition) ctxengine.AssembleRequest {
	return ctxengine.AssembleRequest{
		ToolDefinitionTokens: ctxengine.EstimateToolDefinitionTokens(defs),
		Layers:               al.promptLayers(agent, opts),
		Injections:           recallInjections(ctx, mem, opts.UserMessage),
		Channel:              opts.Channel,
		ChatID:               opts.ChatID,
	}
}

// promptLayers is the system prompt for one dispatch: the agent's static and
// dynamic prompt for this channel/chat, then the session token behind the
// summary. A test that injects a bare instance without a ContextBuilder gets
// only the token layer. A fresh temporary agent has no tools, so no token:
// its prompt is exactly its creator's.
func (al *AgentLoop) promptLayers(agent *AgentInstance, opts processOptions) []ctxengine.Layer {
	var layers []ctxengine.Layer
	if agent.ContextBuilder != nil {
		layers = agent.ContextBuilder.PromptLayers(opts.Channel, opts.ChatID)
	}
	if agent.Spec.Fresh {
		return layers
	}
	return append(layers, sessionTokenLayer(al.sessionToken(agent, opts.SessionKey)))
}

// estimateTokens estimates the number of tokens in a message list.
// Uses a safe heuristic of 2.5 characters per token to account for CJK and other
// overheads better than the previous 3 chars/token.
func (al *AgentLoop) estimateTokens(messages []providers.Message) int {
	totalChars := 0
	for _, m := range messages {
		totalChars += utf8.RuneCountInString(m.Content)
	}
	// 2.5 chars per token = totalChars * 2 / 5
	return totalChars * 2 / 5
}

// sessionStoreWriteFailed handles a message the session store refused to
// record: it logs at error level, raises the operator alert, and returns the
// error that fails the turn. The turn is abandoned rather than continued,
// because a reply built on a history the store did not accept would be
// invisible to the next turn and to a restart replay. what names the message
// kind for the log, the alert and the error text.
func (al *AgentLoop) sessionStoreWriteFailed(agent *AgentInstance, sessionKey, what string, err error) error {
	logger.ErrorCF("agent", "Session store write failed", map[string]any{
		"agent_id":    agent.ID,
		"session_key": sessionKey,
		"message":     what,
		"error":       err.Error(),
	})
	al.Alerter().Send(alerter.Alert{
		Title:       "Session store write failed",
		Description: "a " + what + " could not be written to the session store and the turn was abandoned (disk full or unwritable?)",
		Details:     err.Error(),
		EventID:     "session-store",
	})
	return fmt.Errorf("session store rejected the %s, so the turn was abandoned: %w", what, err)
}
