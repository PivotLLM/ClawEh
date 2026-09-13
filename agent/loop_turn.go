// ClawEh - Personal AI Assistant
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/dump"
	"github.com/PivotLLM/ClawEh/llmcontext"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/utils"
)

// processOptions configures how a message is processed
type processOptions struct {
	SessionKey      string   // Session identifier for history/context
	Channel         string   // Target channel for tool execution
	ChatID          string   // Target chat ID for tool execution
	UserMessage     string   // User message content (may include prefix)
	Media           []string // media:// refs from inbound message
	DefaultResponse string   // Response when LLM returns empty
	SendResponse    bool     // Whether to send response via bus
	IsRetry         bool     // True when message is a /retry retrigger (skip AddMessage)
	ResetSession    bool     // True when this message is a session_clear handoff: reset before handling
	SenderID        string   // Originating sender identifier for source attribution
	SenderName      string   // Human-readable sender label (display name + canonical ID)
	IsGroup         bool     // True when the inbound message came from a group/multi-listener chat
	IterationsOut   *int     // optional: runAgentLoop writes the LLM iteration count here
}

// runAgentLoop is the core message processing logic.
func (al *AgentLoop) runAgentLoop(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
) (string, error) {
	// 0. Record last channel (skip internal channels and cli)
	if opts.Channel != "" && opts.ChatID != "" {
		if !constants.IsInternalChannel(opts.Channel) {
			channelKey := fmt.Sprintf("%s:%s", opts.Channel, opts.ChatID)
			if err := al.RecordLastChannel(channelKey); err != nil {
				logger.WarnCF(
					"agent",
					"Failed to record last channel",
					map[string]any{"error": err.Error()},
				)
			}
			// Also record in per-agent state so callback routing works for named agents.
			if sm, ok := al.agentStates[agent.ID]; ok {
				if err := sm.SetLastChannel(channelKey); err != nil {
					logger.WarnCF("agent", "Failed to record last channel for agent",
						map[string]any{"agent": agent.ID, "error": err.Error()})
				}
			}
		}
	}

	// Attach the agent ID to the context so every downstream provider call —
	// including compression-time invocations through PreDispatchCheck,
	// CheckAndCompress, AddUserMessage's trigger check, and the compact_session
	// MCP tool — surfaces the agent in error logs. runLLMIteration also wraps
	// ctx for its own scope, but compression paths fan out from runAgentLoop
	// before reaching that point, so we set it once here at the top.
	ctx = providers.WithAgentID(ctx, agent.ID)

	// 1. Get or create the ContextManager (and the cognitive-memory session, nil
	// for agents without it) for this session.
	cm, mem, releaseCtxMgr := al.getSessionContext(agent, opts.SessionKey)
	defer releaseCtxMgr()
	cm.SetCallContext(opts.Channel, opts.ChatID)

	// Record the inbound source on the session token record so MCP-routed tool
	// calls (which bypass the agent loop) can publish their ForUser payloads
	// back to the originating user. Done after getContextManager so the token
	// for this session is guaranteed to exist. Unified-mode sessions overwrite
	// on each turn to follow the user across channels; non-unified sessions
	// only ever see one channel so the field is stable. Internal channels
	// (cli/subagent/recovery/system) have no real channel handler, so skip
	// the record entirely — the MCP ForUser publish path drops on the empty
	// channel/chatID guard rather than dispatching to a nonexistent handler.
	if !constants.IsInternalChannel(opts.Channel) {
		al.mu.RLock()
		stiSource := al.sessionTokenIssuer
		al.mu.RUnlock()
		if stiSource != nil {
			stiSource.SetSource(opts.SessionKey, opts.Channel, opts.ChatID)
		}
	}

	// Session-reset handshake: session_clear publishes an inbound tagged
	// metaSessionReset. Reset here — at a clean turn boundary, never mid-turn —
	// before the notice message is saved, so this turn starts on a clean
	// (archive-preserved) context, and reissue the session token so the LLM gets
	// a fresh one in this turn's system prompt.
	if opts.ResetSession {
		if err := cm.Reset(ctx); err != nil {
			logger.WarnCF("agent", "session_clear: Reset failed", map[string]any{
				"session_key": opts.SessionKey,
				"error":       err.Error(),
			})
		}
		// The cleared history no longer references any media; let its files age out.
		al.releaseSessionPins(opts.SessionKey)
		al.mu.RLock()
		sti := al.sessionTokenIssuer
		al.mu.RUnlock()
		if sti != nil {
			archiveDir := filepath.Join(agent.Workspace, "sessions")
			if tok := sti.Issue(agent.ID, opts.SessionKey, archiveDir); tok != "" {
				cm.SetSessionToken(tok)
			}
		}
	}

	// 2. Save user message and trigger compression check (skip on retry — already in history).
	if !opts.IsRetry {
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
			userMsg.Media = opts.Media
			// Flow B: text-only primary + vision side-model configured → describe
			// the inbound image(s) once here and fold the description into the
			// message text (clearing Media), so every dispatch iteration carries
			// usable text instead of an image the model can't see. No-op for
			// vision-capable primaries and when vision is not configured.
			al.describeInboundMedia(ctx, agent, &userMsg)
			// Keep the media:// refs visible in the stored text and pin them for
			// this session so TTL cleanup doesn't reap files the history still
			// references. The refs are actionable: agent_spawn's media parameter
			// accepts them, so even a text-only primary can hand the original
			// attachment to a vision-capable worker.
			if refs := mediaRefsIn(opts.Media); len(refs) > 0 {
				al.pinSessionMediaRefs(opts.SessionKey, refs)
				userMsg.Content = appendMediaRefMarker(userMsg.Content, refs)
			}
		}
		seq, err := cm.AddUserMessage(ctx, userMsg)
		if err != nil {
			logger.WarnCF("agent", "Failed to add user message to context manager",
				map[string]any{"error": err.Error(), "session": opts.SessionKey})
		}
		mem.Observe(ctx, seq, userMsg)
	}

	// 3. Assemble the request: eviction sweep, safety-net compaction on both
	// stored history and the built request, and memory placement. Routed
	// memory is selected from the message the user just sent.
	asm, buildErr := cm.Assemble(ctx, al.assembleRequest(ctx, agent, mem, opts.UserMessage))
	if buildErr != nil {
		return "", fmt.Errorf("context manager assemble: %w", buildErr)
	}
	messages := asm.Messages

	// Reconcile media pins to what the (possibly compacted) context still
	// references — refs compacted out of the context become reapable. Must run
	// before resolveMediaRefs replaces refs with data URLs in the dispatch copy.
	al.reconcileSessionPins(opts.SessionKey, messages)

	// Resolve media:// refs: images→base64 data URLs, non-images→local paths in content
	cfg := al.GetConfig()
	maxMediaSize := cfg.Agents.Defaults.GetMaxMediaSize()
	messages = resolveMediaRefs(messages, al.mediaStore, maxMediaSize)

	// Mark the turn as in-flight so a restart can detect an interrupted LLM call.
	if setErr := agent.Sessions.SetPendingTurn(opts.SessionKey); setErr != nil {
		logger.WarnCF("agent", "Failed to set pending turn flag",
			map[string]any{"error": setErr.Error(), "session": opts.SessionKey})
	}
	defer func() {
		if clrErr := agent.Sessions.ClearPendingTurn(opts.SessionKey); clrErr != nil {
			logger.WarnCF("agent", "Failed to clear pending turn flag",
				map[string]any{"error": clrErr.Error(), "session": opts.SessionKey})
		}
	}()

	// 4. Run LLM iteration loop
	finalContent, normal, degenerate, finishReason, iteration, err := al.runLLMIteration(ctx, agent, messages, opts, cm, mem)
	if err != nil {
		return "", err
	}

	// Intentional silence: the model replied with the no-response sentinel (e.g.
	// a group message addressed to someone else). Clear the typing indicator and
	// send nothing — never poked or surfaced, unlike a degenerate empty reply.
	if isNoResponseSentinel(finalContent) {
		logger.DebugCF("agent", "LLM declined to respond (no-response sentinel)",
			map[string]any{"agent_id": agent.ID, "session_key": opts.SessionKey})
		al.stopTyping(opts.Channel, opts.ChatID)
		return "", nil
	}

	// If last tool had ForUser content and we already sent it, we might not need to send final response
	// This is controlled by the tool's Silent flag and ForUser content

	// 5. Handle empty response.
	isSystemError := false
	if finalContent == "" {
		switch {
		case normal && !degenerate && opts.IsGroup:
			// Legitimate silence: an empty, normal reply in a GROUP chat means the
			// message wasn't for this agent (e.g. @-directed at another). Stay silent
			// but clear the typing indicator so nobody is left waiting.
			logger.DebugCF("agent", "empty normal response in group; staying silent",
				map[string]any{
					"agent_id":      agent.ID,
					"session_key":   opts.SessionKey,
					"finish_reason": finishReason,
				})
			al.stopTyping(opts.Channel, opts.ChatID)
			return "", nil
		case degenerate:
			// Model produced reasoning but no reply, even after poking.
			isSystemError = true
			finalContent = "Sorry — I couldn't compose a reply to that. Please try again."
			logger.WarnCF("agent", "degenerate empty response; sending fallback reply",
				map[string]any{
					"agent_id":      agent.ID,
					"session_key":   opts.SessionKey,
					"finish_reason": finishReason,
				})
		case normal:
			// Direct chat, empty-but-normal reply — the user is addressing this agent
			// and expects an answer, so an empty response is a failure (e.g. a degraded
			// fallback model returning nothing). Advise rather than swallow it.
			isSystemError = true
			finalContent = "The model returned an empty response. This can happen when a fallback model can't handle the request — please try again."
			logger.WarnCF("agent", "empty response on a direct message; advising the user",
				map[string]any{
					"agent_id":      agent.ID,
					"session_key":   opts.SessionKey,
					"finish_reason": finishReason,
				})
		default:
			// Abnormal termination.
			isSystemError = true
			finalContent = fmt.Sprintf("The AI provider returned an empty response (finish reason: %s). Check provider logs for details.", finishReason)
		}
	}

	// 6. Save final assistant message to session (skip system error strings)
	if !isSystemError {
		finalMsg := providers.Message{Role: "assistant", Content: finalContent}
		if seq, err := cm.AddAssistantMessage(ctx, finalMsg); err == nil {
			mem.Observe(ctx, seq, finalMsg)
		} else {
			logger.WarnCF("agent", "Failed to add assistant message to context manager",
				map[string]any{"error": err.Error(), "session": opts.SessionKey})
		}
		agent.Sessions.Save(opts.SessionKey)
	}

	// 7. Optional: send response via bus
	if opts.SendResponse {
		al.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Channel: opts.Channel,
			ChatID:  opts.ChatID,
			Content: finalContent,
		})
	}

	// 8. Log response — content gated behind log_message_content for privacy
	logMsg := "Response"
	if logger.GetLogMessageContent() {
		logMsg = fmt.Sprintf("Response: %s", utils.Truncate(finalContent, 120))
	}
	logger.InfoCF("agent", logMsg,
		map[string]any{
			"agent_id":     agent.ID,
			"session_key":  opts.SessionKey,
			"iterations":   iteration,
			"final_length": len(finalContent),
			"system_error": isSystemError,
		})

	if opts.IterationsOut != nil {
		*opts.IterationsOut = iteration
	}
	return finalContent, nil
}

func (al *AgentLoop) targetReasoningChannelID(channelName string) (chatID string) {
	if al.channelManager == nil {
		return ""
	}
	if ch, ok := al.channelManager.GetChannel(channelName); ok {
		return ch.ReasoningChannelID()
	}
	return ""
}

func (al *AgentLoop) handleReasoning(
	ctx context.Context,
	reasoningContent, channelName, channelID string,
	inline bool,
) {
	if reasoningContent == "" || channelName == "" || channelID == "" {
		return
	}
	// When delivered inline in the main chat (no dedicated reasoning channel),
	// mark it so the user can tell the model's thinking from its actual reply.
	if inline {
		reasoningContent = "💭 _Reasoning:_\n" + reasoningContent
	}

	// Check context cancellation before attempting to publish,
	// since PublishOutbound's select may race between send and ctx.Done().
	if ctx.Err() != nil {
		return
	}

	// Use a short timeout so the goroutine does not block indefinitely when
	// the outbound bus is full.  Reasoning output is best-effort; dropping it
	// is acceptable to avoid goroutine accumulation.
	pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pubCancel()

	if err := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: channelName,
		ChatID:  channelID,
		Content: reasoningContent,
	}); err != nil {
		// Treat context.DeadlineExceeded / context.Canceled as expected
		// (bus full under load, or parent canceled).  Check the error
		// itself rather than ctx.Err(), because pubCtx may time out
		// (5 s) while the parent ctx is still active.
		// Also treat ErrBusClosed as expected — it occurs during normal
		// shutdown when the bus is closed before all goroutines finish.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
			errors.Is(err, bus.ErrBusClosed) {
			logger.DebugCF("agent", "Reasoning publish skipped (timeout/cancel)", map[string]any{
				"channel": channelName,
				"error":   err.Error(),
			})
		} else {
			logger.WarnCF("agent", "Failed to publish reasoning (best-effort)", map[string]any{
				"channel": channelName,
				"error":   err.Error(),
			})
		}
	}
}

// maxIdenticalToolBatches caps how many consecutive iterations may request the
// exact same tool-call batch before the loop aborts (degenerate-loop guard).
// The model is steered (told it is repeating, and to re-read/adjust) on each
// repeat before this cap, so the cap allows a couple of correction chances.
const maxIdenticalToolBatches = 4

// maxEmptyRetries caps how many times the loop pokes the model to actually reply
// after it finished a turn with no visible content but non-empty reasoning (a
// common degenerate-model failure mode). After the cap the caller sends a
// graceful fallback rather than leaving the user with no answer.
const maxEmptyRetries = 2

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

// toolCallSignature returns a stable signature for a tool-call batch (sorted
// name+arguments), used to detect a model repeating the identical call. Returns
// "" for an empty batch.
func toolCallSignature(calls []providers.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		args := ""
		if len(c.Arguments) > 0 {
			if b, err := json.Marshal(c.Arguments); err == nil { // json sorts map keys
				args = string(b)
			}
		} else if c.Function != nil {
			args = c.Function.Arguments
		}
		name := c.Name
		if name == "" && c.Function != nil {
			name = c.Function.Name
		}
		parts = append(parts, name+":"+args)
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

// runLLMIteration executes the LLM call loop with tool handling.
// summarizeEvictions renders one consolidated notice for all of a turn's
// evictions: total count, total bytes freed, and a per-resource breakdown (top
// few by count). Keeps the chat to a single line even when an agent re-reads the
// same file every iteration; per-eviction detail lives in the DEBUG log.
func summarizeEvictions(events []llmcontext.EvictionEvent) string {
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
	const max = 48
	if len(base) > max {
		return base[:max-1] + "…"
	}
	return base
}

// evictionNotifyUser reports whether the agent's resolved eviction policy has
// notify_user enabled, so the loop can surface a consolidated notice at the end
// of the turn. The DEBUG log of evictions is unconditional and happens inside
// the sweep.
func (al *AgentLoop) evictionNotifyUser(agent *AgentInstance) bool {
	cfg := al.GetConfig()
	p := llmcontext.DefaultEvictionPolicy()
	applyEvictionConfig(&p, cfg.Agents.Defaults.ContextEviction)
	if agent != nil && agent.Config != nil {
		applyEvictionConfig(&p, agent.Config.ContextEviction)
	}
	return p.NotifyUser
}

// assembleRequest builds the per-dispatch request for the context manager:
// the cost of the tool schemas this dispatch will send, and the memory blocks
// recalled for routeText (the user's message for this turn). mem may be nil.
func (al *AgentLoop) assembleRequest(ctx context.Context, agent *AgentInstance, mem *memorySession, routeText string) llmcontext.AssembleRequest {
	defs := agent.Tools.ToProviderDefs()
	if agent.NoTools {
		defs = nil
	}
	return al.assembleRequestWithDefs(ctx, agent, mem, routeText, defs)
}

// assembleRequestWithDefs is assembleRequest for a caller that already holds
// this dispatch's tool definitions.
func (al *AgentLoop) assembleRequestWithDefs(ctx context.Context, _ *AgentInstance, mem *memorySession, routeText string, defs []providers.ToolDefinition) llmcontext.AssembleRequest {
	return llmcontext.AssembleRequest{
		ToolDefinitionTokens: llmcontext.EstimateToolDefinitionTokens(defs),
		Injections:           mem.Recall(ctx, routeText),
	}
}

func (al *AgentLoop) runLLMIteration(
	ctx context.Context,
	agent *AgentInstance,
	messages []providers.Message,
	opts processOptions,
	cm llmcontext.ContextManager,
	mem *memorySession,
) (string, bool, bool, string, int, error) {
	iteration := 0
	var finalContent string
	lastNormal := false
	lastFinishReason := "max_iterations"

	// Progress heartbeat: keep a long turn visibly alive by editing its
	// placeholder every progress_interval with a running tool-call count. Stops
	// when the turn returns (defer). completedTools is bumped after each batch.
	var completedTools atomic.Int64
	stopProgress := al.startProgressUpdates(opts.Channel, opts.ChatID,
		al.GetConfig().Agents.Defaults.GetProgressInterval(), &completedTools)
	defer stopProgress()

	// Context-eviction notices are accumulated across the turn's per-iteration
	// sweeps and posted as a single consolidated line at the end (when the agent's
	// policy has notify_user on). An agent that re-reads the same file every
	// iteration otherwise floods the chat with one notice per eviction; the
	// per-eviction detail is always in the DEBUG log regardless.
	var evictedThisTurn []llmcontext.EvictionEvent
	defer func() {
		if len(evictedThisTurn) == 0 || opts.Channel == "" || !al.evictionNotifyUser(agent) {
			return
		}
		_ = al.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Channel: opts.Channel,
			ChatID:  opts.ChatID,
			Content: summarizeEvictions(evictedThisTurn),
		})
	}()

	// Loop protection: if the model requests the exact same tool call(s) on
	// consecutive iterations, break rather than spin (e.g. re-writing the same
	// memory over and over). Tracks the prior iteration's tool-call signature.
	var lastToolSig string
	identicalToolBatches := 0
	// Empty-response handling: poke the model to reply when it finishes with no
	// content but non-empty reasoning; degenerate flags that the retries were
	// exhausted so the caller sends a fallback instead of going silent.
	emptyRetries := 0
	degenerate := false

	// Partial-text streaming: if the target channel opts into streaming, install a
	// coalescing forwarder that batches provider token deltas into sentence/length
	// chunks and pushes them to the channel as the model generates. The callback is
	// shared by every Chat call in this turn (v1 streams each iteration's assistant
	// text with no reset). Non-streaming channels get no callback and are unaffected.
	var streamCoalescer *streamCoalescer
	if streamToolNarration && al.channelManager != nil && al.channelManager.SupportsStreaming(opts.Channel) {
		channel, chatID := opts.Channel, opts.ChatID
		streamCoalescer = newStreamCoalescer(func(batch string) {
			al.channelManager.StreamDelta(channel, chatID, batch)
		})
		// Flush any buffered remainder that never hit a boundary before the turn's
		// terminal reply is published, so no trailing partial text is lost.
		defer streamCoalescer.Flush()
	}

	// Inject agent ID into context so provider error log entries can attribute
	// failures to the specific agent without correlating timestamps.
	ctx = providers.WithAgentID(ctx, agent.ID)

	// Determine effective model for this conversation turn from the session's
	// active model selection. The decision is sticky for all tool-follow-up
	// iterations within the same turn so that a multi-step tool chain doesn't
	// switch models mid-way through.
	activeCandidates, activeModel := al.selectCandidates(agent, opts.SessionKey)

	// Log which model/provider this turn is routed to.
	{
		loggedModel := activeModel
		loggedProvider := ""
		if len(activeCandidates) > 0 {
			loggedProvider = activeCandidates[0].Provider
			loggedModel = activeCandidates[0].Model
		}
		fields := map[string]any{"agent_id": agent.ID, "model": loggedModel}
		if loggedProvider != "" {
			fields["provider"] = loggedProvider
		}
		logger.InfoCF("agent", "Dispatching to model", fields)
	}

	// One notifier for the whole turn so its de-dup memory spans all tool
	// iterations: a primary that fails over on every iteration (e.g. a model that
	// 400s each call) posts its heads-up once, not once per iteration.
	turnNotifier := al.fallbackNotifier(opts)

	// Follow-along breadcrumbs (/tools on): post a one-line note per tool call.
	// Resolved once per turn — a user chat, not the internal "system" channel.
	showToolActivity := opts.Channel != "" && opts.Channel != "system" && opts.ChatID != "" &&
		al.getShowToolActivity(agent, opts.SessionKey)

	for iteration < agent.MaxIterations {
		iteration++

		logger.DebugCF("agent", "LLM iteration",
			map[string]any{
				"agent_id":  agent.ID,
				"iteration": iteration,
				"max":       agent.MaxIterations,
			})

		// Build tool definitions. A sub-agent is an instance of the parent and is
		// offered the parent's full toolset; recursion is bounded by MaxSpawnDepth
		// in the Spawner, not by withholding tools.
		providerToolDefs := agent.Tools.ToProviderDefs()
		if agent.NoTools {
			providerToolDefs = nil
		}
		// Assemble for this dispatch: the LLM-free eviction sweep first (cheap
		// relief so summarization fires far less often), then the safety-net
		// compaction checks, then memory placement. The slice in hand is kept
		// unless stored history was rewritten — it carries this turn's unsaved
		// steering messages and already-resolved media — so only a changed
		// assembly replaces it. Tool-schema cost rides on the request so every
		// trigger measures the real request and not stored history alone.
		if asm, aerr := cm.Assemble(ctx, al.assembleRequestWithDefs(ctx, agent, mem, opts.UserMessage, providerToolDefs)); aerr != nil {
			logger.WarnCF("agent", "assemble failed (continuing with current slice)", map[string]any{
				"agent_id": agent.ID,
				"error":    aerr.Error(),
			})
		} else {
			evictedThisTurn = append(evictedThisTurn, asm.Evictions...)
			if asm.Changed() {
				messages = asm.Messages
			}
		}

		// Log session status at INFO level for operational visibility
		sessionSummary := agent.Sessions.GetSummary(opts.SessionKey)
		logger.InfoCF("agent", "Session status",
			map[string]any{
				"agent_id":    agent.ID,
				"messages":    len(messages),
				"compacted":   sessionSummary != "",
				"summary_len": len(sessionSummary),
				"tools":       len(providerToolDefs),
				"model":       activeModel,
			})

		// Log LLM request details
		logger.DebugCF("agent", "LLM request",
			map[string]any{
				"agent_id":       agent.ID,
				"iteration":      iteration,
				"model":          activeModel,
				"messages_count": len(messages),
				"tools_count":    len(providerToolDefs),
				"max_tokens":     agent.MaxTokens,
				"temperature":    agent.Temperature,
				"system_prompt_len": func() int {
					if len(messages) > 0 {
						return len(messages[0].Content)
					}
					return 0
				}(),
			})

		// Log full messages (detailed) — gated behind log_message_content for privacy
		if logger.GetLogMessageContent() {
			logger.DebugCF("agent", "Full LLM request",
				map[string]any{
					"iteration":     iteration,
					"messages_json": formatMessagesForLog(messages),
					"tools_json":    formatToolsForLog(providerToolDefs),
				})
		}

		// Call LLM with fallback chain if multiple candidates are configured.
		var response *providers.LLMResponse
		var err error

		// Resolve the provider that will actually serve this turn's request so
		// the type assertions below reflect that provider's capabilities rather
		// than the shared agent.Provider (which on the default config is
		// claude-cli for every agent, including those whose primary is a
		// non-CLI / non-Anthropic protocol).
		runProvider, runModel := al.resolveRunProvider(agent, activeCandidates, activeModel)

		llmOpts := map[string]any{
			"max_tokens":       agent.MaxTokens,
			"prompt_cache_key": agent.ID,
		}
		// CLI providers (claude-cli, codex-cli, gemini-cli) invoke a subprocess and
		// do not accept HTTP request parameters. Skip temperature for those providers.
		if _, isCLI := runProvider.(providers.CLIProvider); !isCLI {
			llmOpts["temperature"] = agent.Temperature
		}
		// parseThinkingLevel guarantees ThinkingOff for empty/unknown values,
		// so checking != ThinkingOff is sufficient.
		if agent.ThinkingLevel != ThinkingOff {
			if tc, ok := runProvider.(providers.ThinkingCapable); ok && tc.SupportsThinking() {
				llmOpts["thinking_level"] = string(agent.ThinkingLevel)
			} else {
				logger.WarnCF("agent", "thinking_level is set but current provider does not support it, ignoring",
					map[string]any{"agent_id": agent.ID, "thinking_level": string(agent.ThinkingLevel)})
			}
		}
		// Opt the provider into per-delta text streaming for this turn. Providers that
		// don't support it (CLI, Anthropic) ignore the option; the coalescer forwards
		// batched deltas to the streaming-capable channel.
		if streamCoalescer != nil {
			llmOpts[providers.TextDeltaOption] = providers.TextDeltaFunc(streamCoalescer.Add)
		}

		// activeProvider tracks the protocol name surfaced in the finish event.
		activeProvider := ""
		if len(activeCandidates) > 0 {
			activeProvider = activeCandidates[0].Provider
		}

		callLLM := func() (*providers.LLMResponse, error) {
			al.activeRequests.Add(1)
			defer al.activeRequests.Done()

			if len(activeCandidates) >= 1 && al.fallback != nil {
				fbResult, fbErr := al.fallback.ExecuteWithNotify(
					ctx,
					activeCandidates,
					func(ctx context.Context, c providers.FallbackCandidate) (*providers.LLMResponse, error) {
						// Drop image parts for candidates whose model can't accept them.
						// A screenshot persisted in history would otherwise 404 a
						// non-vision candidate (e.g. a cheaper default), failing every
						// turn. Keyed by alias like GetModelConfig/selectCandidates.
						modelKey := c.Alias
						if modelKey == "" {
							modelKey = c.Model
						}
						msgs := al.messagesForModel(messages, modelKey, len(providerToolDefs) > 0)
						if al.dispatcher != nil {
							key := c.Alias
							if key == "" {
								key = c.Provider + "/" + c.Model
							}
							if p, err := al.dispatcher.Get(key); err == nil {
								return p.Chat(ctx, msgs, providerToolDefs, c.Model, llmOpts)
							}
						}
						return agent.Provider.Chat(ctx, msgs, providerToolDefs, c.Model, llmOpts)
					},
					turnNotifier,
				)
				if fbErr != nil {
					return nil, fbErr
				}
				if fbResult.Provider != "" {
					activeProvider = fbResult.Provider
					if len(fbResult.Attempts) > 0 {
						logger.InfoCF(
							"agent",
							fmt.Sprintf("Fallback: succeeded with %s/%s after %d attempts",
								fbResult.Provider, fbResult.Model, len(fbResult.Attempts)+1),
							map[string]any{"agent_id": agent.ID, "iteration": iteration},
						)
						for _, attempt := range fbResult.Attempts {
							if attempt.Skipped {
								logger.WarnCF("agent", "Fallback: skipped candidate (cooldown)",
									map[string]any{
										"agent_id":  agent.ID,
										"provider":  attempt.Provider,
										"model":     attempt.Model,
										"reason":    attempt.Reason,
										"remaining": attempt.Remaining.Round(time.Second),
										"error":     attempt.Error,
									})
							} else {
								logger.WarnCF("agent", "Fallback: candidate failed",
									map[string]any{
										"agent_id": agent.ID,
										"provider": attempt.Provider,
										"model":    attempt.Model,
										"reason":   attempt.Reason,
										"duration": attempt.Duration.Round(time.Millisecond),
										"error":    attempt.Error,
									})
							}
						}
					}
				}
				return fbResult.Response, nil
			}
			// No active fallback chain (no candidates, or fallback disabled):
			// route through the dispatcher-resolved provider for the agent's
			// primary model so non-default protocols don't get silently routed
			// through the shared agent.Provider (the default-config claude-cli).
			// resolveRunProvider returns agent.Provider + activeModel as the
			// last-resort safety net when dispatcher resolution cannot satisfy
			// the request.
			return runProvider.Chat(ctx, al.messagesForModel(messages, runModel, len(providerToolDefs) > 0), providerToolDefs, runModel, llmOpts)
		}

		// Retry loop for context/token errors
		maxRetries := 2
		for retry := 0; retry <= maxRetries; retry++ {
			logger.InfoCF("agent", "LLM dispatch", map[string]any{
				"agent_id":     agent.ID,
				"iteration":    iteration,
				"provider":     activeProvider,
				"model":        activeModel,
				"num_messages": len(messages),
				"num_tools":    len(providerToolDefs),
				"max_tokens":   agent.MaxTokens,
			})
			dispatchStart := time.Now()
			response, err = callLLM()
			emitLLMFinishEvent(agent.ID, iteration, activeProvider, activeModel, dispatchStart, response, err)
			if err == nil {
				break
			}

			errMsg := strings.ToLower(err.Error())

			// Check if this is a network/HTTP timeout — not a context window error.
			isTimeoutError := errors.Is(err, context.DeadlineExceeded) ||
				strings.Contains(errMsg, "deadline exceeded") ||
				strings.Contains(errMsg, "client.timeout") ||
				strings.Contains(errMsg, "timed out") ||
				strings.Contains(errMsg, "timeout exceeded")

			// Detect real context window / token limit errors, excluding network timeouts.
			isContextError := !isTimeoutError && (strings.Contains(errMsg, "context_length_exceeded") ||
				strings.Contains(errMsg, "context window") ||
				strings.Contains(errMsg, "maximum context length") ||
				strings.Contains(errMsg, "token limit") ||
				strings.Contains(errMsg, "too many tokens") ||
				strings.Contains(errMsg, "max_tokens") ||
				strings.Contains(errMsg, "invalidparameter") ||
				strings.Contains(errMsg, "prompt is too long") ||
				strings.Contains(errMsg, "request too large"))

			var exhausted *providers.FallbackExhaustedError
			if errors.As(err, &exhausted) && exhausted.AllContextLimit() {
				isContextError = true
			}

			if isTimeoutError && retry < maxRetries {
				backoff := time.Duration(retry+1) * 5 * time.Second
				logger.WarnCF("agent", "Timeout error, retrying after backoff", map[string]any{
					"error":   err.Error(),
					"retry":   retry,
					"backoff": backoff.String(),
				})
				select {
				case <-ctx.Done():
					return "", false, false, "", 0, ctx.Err()
				case <-time.After(backoff):
				}
				continue
			}

			if isContextError && retry < maxRetries {
				logger.WarnCF(
					"agent",
					"Context window error detected, attempting compression",
					map[string]any{
						"error": err.Error(),
						"retry": retry,
					},
				)

				if retry == 0 && !constants.IsInternalChannel(opts.Channel) {
					al.bus.PublishOutbound(ctx, bus.OutboundMessage{
						Channel: opts.Channel,
						ChatID:  opts.ChatID,
						Content: "Context window exceeded. Compressing history and retrying...",
					})
				}

				prevMsgCount := len(messages)
				comprMgr, releaseComprMgr := al.getContextManager(agent, opts.SessionKey)
				defer releaseComprMgr()
				if ferr := comprMgr.ForceCompress(ctx); ferr != nil {
					logger.WarnCF("agent", "force compression failed",
						map[string]any{"error": ferr.Error(), "session": opts.SessionKey})
				}
				comprMgr.SetCallContext(opts.Channel, opts.ChatID)
				if asm, berr := comprMgr.Assemble(ctx, al.assembleRequestWithDefs(ctx, agent, mem, opts.UserMessage, providerToolDefs)); berr == nil {
					messages = asm.Messages
				}

				// If compression didn't reduce message count, history is already minimal.
				// The 413 is likely caused by max_tokens exceeding the provider's per-request
				// limit (e.g. Groq free tier limits total request size below the model's stated
				// max). Halve max_tokens for the next attempt so the request fits within the
				// provider's service limits.
				if len(messages) >= prevMsgCount {
					if current, ok := llmOpts["max_tokens"].(int); ok && current > 512 {
						reduced := current / 2
						llmOpts["max_tokens"] = reduced
						logger.WarnCF("agent", "History already minimal; reducing max_tokens for retry",
							map[string]any{
								"agent_id":       agent.ID,
								"old_max_tokens": current,
								"new_max_tokens": reduced,
							})
					}
				}
				continue
			}
			break
		}

		if err != nil {
			logger.ErrorCF("agent", "LLM call failed",
				map[string]any{
					"agent_id":  agent.ID,
					"iteration": iteration,
					"model":     activeModel,
					"error":     err.Error(),
				})
			return "", false, false, "", iteration, fmt.Errorf("LLM call failed after retries: %w", err)
		}

		// Dump responses when enabled.
		if al.dumpsDir != "" {
			isRefusal := response.FinishReason == "refusal"
			if isRefusal && al.cfg.Logging.DumpRefusals {
				al.dumpRefusal(agent, messages, response, opts, activeModel, iteration)
			} else if al.cfg.Logging.DumpAll {
				al.dumpAll(agent, messages, response, opts, activeModel, iteration)
			}
		}

		// Deliver reasoning only when the session opted in (/reasoning on). Route
		// to the configured reasoning channel if one exists, else inline in the
		// main chat so the toggle works on any channel.
		if al.getExposeReasoning(agent, opts.SessionKey) {
			reasoningTarget := al.targetReasoningChannelID(opts.Channel)
			inline := reasoningTarget == ""
			if inline {
				reasoningTarget = opts.ChatID
			}
			go al.handleReasoning(ctx, response.Reasoning, opts.Channel, reasoningTarget, inline)
		}

		respFields := map[string]any{
			"agent_id":        agent.ID,
			"iteration":       iteration,
			"content_chars":   len(response.Content),
			"tool_calls":      len(response.ToolCalls),
			"reasoning_chars": len(response.Reasoning),
			"target_channel":  al.targetReasoningChannelID(opts.Channel),
			"channel":         opts.Channel,
		}
		// Reasoning text is response body content — gate behind log_message_content.
		if logger.GetLogMessageContent() {
			respFields["reasoning"] = response.Reasoning
		}
		logger.DebugCF("agent", "LLM response", respFields)
		// Check if no tool calls - then check reasoning content if any
		if len(response.ToolCalls) == 0 {
			finalContent = response.Content
			// "Thinking" may arrive as reasoning_content (OpenAI-style) or reasoning
			// (OpenRouter-style); treat either as the model's reasoning.
			reasoningText := response.ReasoningContent
			if reasoningText == "" {
				reasoningText = response.Reasoning
			}
			// Only fall back to reasoning as the visible reply when the operator opts
			// in. Default off: a model that returns empty content but full reasoning
			// (e.g. degenerating into reasoning-only/repetition output) must NOT leak
			// its raw chain-of-thought to the user — empty content then hits the
			// graceful empty-response path instead.
			if finalContent == "" && reasoningText != "" &&
				al.cfg != nil && al.cfg.Agents.Defaults.ShowReasoningAsContent {
				finalContent = reasoningText
			}
			lastNormal = response.Normal
			lastFinishReason = response.FinishReason

			// Poke-and-retry: the model finished but produced no visible content
			// while clearly having "thought" (non-empty reasoning) — a common
			// failure mode of some models, which otherwise leaves the user with no
			// reply. Nudge it to actually respond, capped by maxEmptyRetries.
			if finalContent == "" && reasoningText != "" {
				if emptyRetries < maxEmptyRetries {
					emptyRetries++
					messages = append(messages, providers.Message{
						Role:    "user",
						Content: "Your previous turn produced no reply. Make sure you have completed the user's request, then respond to the user now.",
					})
					logger.InfoCF("agent", "empty response with reasoning; poking model to reply",
						map[string]any{
							"agent_id": agent.ID, "iteration": iteration, "retry": emptyRetries,
						})
					continue
				}
				// Retries exhausted: tell the caller this was a degenerate empty
				// response so it sends a fallback instead of going silent.
				degenerate = true
			}

			logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
				map[string]any{
					"agent_id":      agent.ID,
					"iteration":     iteration,
					"content_chars": len(finalContent),
				})
			break
		}

		normalizedToolCalls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			normalizedToolCalls = append(normalizedToolCalls, providers.NormalizeToolCall(tc))
		}

		// Log tool calls
		toolNames := make([]string, 0, len(normalizedToolCalls))
		for _, tc := range normalizedToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		// Loop protection: break if the model requests the identical tool-call
		// batch on consecutive iterations (e.g. re-writing the same memory in a
		// loop). After maxIdenticalToolBatches repeats, stop dispatching.
		if sig := toolCallSignature(normalizedToolCalls); sig != "" {
			if sig == lastToolSig {
				identicalToolBatches++
			} else {
				identicalToolBatches = 0
				lastToolSig = sig
			}
			if identicalToolBatches+1 >= maxIdenticalToolBatches {
				logger.WarnCF("agent", "aborting: identical tool call repeated", map[string]any{
					"agent_id":  agent.ID,
					"iteration": iteration,
					"tools":     toolNames,
					"repeats":   identicalToolBatches + 1,
				})
				finalContent = fmt.Sprintf(
					"Stopped: I kept making the same %s call (%d times) without success, so I'm aborting to avoid a loop. Please check the request or rephrase it.",
					strings.Join(toolNames, ", "), identicalToolBatches+1)
				lastFinishReason = "loop_detected"
				break
			}
		}

		// Feed the recent-tool ring so cognitive memory can auto-load domains whose
		// triggers match a tool the agent just used; the next assembly (same turn,
		// after tool results) picks it up. No-op for non-cognitive agents.
		mem.RecordToolUse(toolNames...)
		logger.InfoCF("agent", "LLM requested tool calls",
			map[string]any{
				"agent_id":  agent.ID,
				"tools":     toolNames,
				"count":     len(normalizedToolCalls),
				"iteration": iteration,
			})

		// If the LLM returned both text content and tool calls, publish that
		// inter-tool narration to the user — only when streaming tool activity is
		// enabled. Off by default so the user sees only the final answer, not the
		// model's "let me also check…" play-by-play.
		if response.Content != "" && opts.Channel != "" && al.GetConfig().Agents.Defaults.StreamToolActivity {
			pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
			_ = al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
				Channel: opts.Channel,
				ChatID:  opts.ChatID,
				Content: response.Content,
			})
			pubCancel()
		}

		// Build assistant message with tool calls
		assistantMsg := providers.Message{
			Role:             "assistant",
			Content:          response.Content,
			ReasoningContent: response.ReasoningContent,
			// Carry Responses reasoning items so the next turn can replay them
			// before this turn's function_call (reasoning models + tools).
			ResponsesReasoning: response.ResponsesReasoning,
		}
		for _, tc := range normalizedToolCalls {
			argumentsJSON, marshalErr := json.Marshal(tc.Arguments)
			if marshalErr != nil {
				logger.ErrorCF("agent", "Failed to marshal tool call arguments",
					map[string]any{
						"agent_id": agent.ID,
						"tool":     tc.Name,
						"error":    marshalErr.Error(),
					})
				argumentsJSON = []byte("{}")
			}
			// Copy ExtraContent to ensure thought_signature is persisted for Gemini 3
			extraContent := tc.ExtraContent
			thoughtSignature := ""
			if tc.Function != nil {
				thoughtSignature = tc.Function.ThoughtSignature
			}

			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:   tc.ID,
				Type: "function",
				Name: tc.Name,
				Function: &providers.FunctionCall{
					Name:             tc.Name,
					Arguments:        string(argumentsJSON),
					ThoughtSignature: thoughtSignature,
				},
				ExtraContent:     extraContent,
				ThoughtSignature: thoughtSignature,
			})
		}
		messages = append(messages, assistantMsg)

		// Save assistant message with tool calls through the context manager so
		// that msgCount is incremented and the message is written to the archive.
		if seq, err := cm.AddToolCallMessage(ctx, assistantMsg); err != nil {
			logger.WarnCF("agent", "AddToolCallMessage failed", map[string]any{
				"agent_id": agent.ID,
				"error":    err.Error(),
			})
		} else {
			mem.Observe(ctx, seq, assistantMsg)
		}

		// Execute tool calls in parallel
		type indexedAgentResult struct {
			result *tools.ToolResult
			tc     providers.ToolCall
		}

		agentResults := make([]indexedAgentResult, len(normalizedToolCalls))
		var wg sync.WaitGroup

		// Per-tool budget: a single tool call cannot run longer than this. The
		// tool's context is cancelled at the deadline so a well-behaved tool
		// returns a timeout error and the model can continue the turn. The overall
		// turn budget (applied to ctx upstream) is the hard backstop for any tool
		// that ignores cancellation.
		toolTimeout := al.GetConfig().Agents.Defaults.GetToolTimeout()

		for i, tc := range normalizedToolCalls {
			agentResults[i].tc = tc

			// Follow-along breadcrumb (/tools on): a one-line, privacy-safe note per
			// tool call, published in dispatch order before the tool runs.
			if showToolActivity {
				if line := toolCallBreadcrumb(tc); line != "" {
					bcCtx, bcCancel := context.WithTimeout(context.Background(), 5*time.Second)
					_ = al.bus.PublishOutbound(bcCtx, bus.OutboundMessage{
						Channel: opts.Channel,
						ChatID:  opts.ChatID,
						Content: line,
					})
					bcCancel()
				}
			}

			wg.Add(1)
			go func(idx int, tc providers.ToolCall) {
				defer wg.Done()
				// A panicking tool must not crash the process or wedge wg.Wait();
				// convert it into an error result so the turn proceeds.
				defer func() {
					if r := recover(); r != nil {
						logger.ErrorCF("agent", "panic in tool call",
							map[string]any{
								"tool":  tc.Name,
								"panic": fmt.Sprintf("%v", r),
								"stack": string(debug.Stack()),
							})
						agentResults[idx].result = &tools.ToolResult{
							Err:    fmt.Errorf("tool %s panicked: %v", tc.Name, r),
							ForLLM: fmt.Sprintf("Tool %s failed with an internal error.", tc.Name),
						}
					}
				}()

				// Tool arguments are intentionally NOT logged: they routinely carry
				// memory content, file contents, and other user data.
				logger.InfoCF("agent", "Tool call dispatched",
					map[string]any{
						"agent_id":  agent.ID,
						"tool":      tc.Name,
						"iteration": iteration,
					})

				// Create async callback for tools that implement AsyncExecutor.
				// When the background work completes, this publishes the result
				// as an inbound system message so processSystemMessage routes it
				// back to the user via the normal agent loop.
				asyncCallback := func(_ context.Context, result *tools.ToolResult) {
					// Send ForUser content directly to the user (immediate feedback),
					// mirroring the synchronous tool execution path.
					if !result.Silent && result.ForUser != "" {
						outCtx, outCancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer outCancel()
						logger.InfoCF("agent", "Async tool completed, delivering to user",
							map[string]any{
								"tool":        tc.Name,
								"channel":     opts.Channel,
								"chat_id":     opts.ChatID,
								"content_len": len(result.ForUser),
							})
						_ = al.bus.PublishOutbound(outCtx, bus.OutboundMessage{
							Channel: opts.Channel,
							ChatID:  opts.ChatID,
							Content: result.ForUser,
						})
					}

					// Determine content for the agent loop (ForLLM or error).
					content := result.ForLLM
					if content == "" && result.Err != nil {
						content = result.Err.Error()
					}
					if content == "" {
						return
					}

					logger.InfoCF("agent", "Async tool completed, publishing result",
						map[string]any{
							"tool":        tc.Name,
							"content_len": len(content),
							"channel":     opts.Channel,
						})

					pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer pubCancel()
					_ = al.bus.PublishInbound(pubCtx, bus.InboundMessage{
						Channel:    "system",
						SenderID:   fmt.Sprintf("async:%s", tc.Name),
						ChatID:     fmt.Sprintf("%s:%s", opts.Channel, opts.ChatID),
						Content:    content,
						SessionKey: opts.SessionKey,
						Metadata:   map[string]string{metadataKeyPreresolvedAgentID: agent.ID},
					})
				}

				// Inject agent config as allow checker so ExecuteWithContext can
				// enforce the tool allowlist as a defense-in-depth measure.
				// Config is always non-nil after construction.
				execCtx := tools.WithToolAllowChecker(ctx, agent.Config)
				execCtx = tools.WithSessionKey(execCtx, opts.SessionKey)
				if toolTimeout > 0 {
					var toolCancel context.CancelFunc
					execCtx, toolCancel = context.WithTimeout(execCtx, toolTimeout)
					defer toolCancel()
				}

				toolResult := agent.Tools.ExecuteWithContext(
					execCtx,
					tc.Name,
					tc.Arguments,
					opts.Channel,
					opts.ChatID,
					asyncCallback,
				)
				agentResults[idx].result = toolResult
			}(i, tc)
		}
		wg.Wait()
		completedTools.Add(int64(len(normalizedToolCalls)))

		// Process results in original order (send to user, save to session)
		streamActivity := al.GetConfig().Agents.Defaults.StreamToolActivity
		// Vision passthrough mode for the active model: how images returned by tools
		// reach it (off / follow-up user turn / on the tool result).
		visionMode := config.VisionOff
		if mc, err := al.GetConfig().GetModelConfig(activeModel); err == nil {
			visionMode = mc.Vision
		}
		var toolImages []string // accumulated for VisionUserMessage mode
		// Accumulated for VisionOff mode: tool images that would otherwise be
		// dropped are dispatched to the vision side-model (Flow A). offFocus
		// collects the image tools' ForLLM text (which carries any file_view_image
		// focus line) to steer the description.
		var offImages []string
		var offFocus []string
		for _, r := range agentResults {
			// Send ForUser content to user immediately if not Silent — only when
			// streaming tool activity is enabled (otherwise the user receives just
			// the final answer, not each tool's intermediate output).
			if streamActivity && !r.result.Silent && r.result.ForUser != "" {
				if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
					Channel: opts.Channel,
					ChatID:  opts.ChatID,
					Content: r.result.ForUser,
				}); err != nil {
					logger.WarnCF("agent", "Failed to publish tool ForUser content",
						map[string]any{
							"tool":    r.tc.Name,
							"channel": opts.Channel,
							"error":   err.Error(),
						})
				} else {
					logger.DebugCF("agent", "Sent tool result to user",
						map[string]any{
							"tool":        r.tc.Name,
							"content_len": len(r.result.ForUser),
						})
				}
			}

			// If tool returned media refs, publish them as outbound media
			if len(r.result.Media) > 0 {
				parts := make([]bus.MediaPart, 0, len(r.result.Media))
				for _, ref := range r.result.Media {
					part := bus.MediaPart{Ref: ref}
					if al.mediaStore != nil {
						if _, meta, err := al.mediaStore.ResolveWithMeta(ref); err == nil {
							part.Filename = meta.Filename
							part.ContentType = meta.ContentType
							part.Type = inferMediaType(meta.Filename, meta.ContentType)
						}
					}
					parts = append(parts, part)
				}
				if err := al.bus.PublishOutboundMedia(ctx, bus.OutboundMediaMessage{
					Channel: opts.Channel,
					ChatID:  opts.ChatID,
					Parts:   parts,
				}); err != nil {
					logger.WarnCF("agent", "Failed to publish tool media",
						map[string]any{
							"tool":    r.tc.Name,
							"channel": opts.Channel,
							"error":   err.Error(),
						})
				}
			}

			// Determine content for LLM based on tool result
			contentForLLM := r.result.ForLLM
			if contentForLLM == "" && r.result.Err != nil {
				contentForLLM = r.result.Err.Error()
			}

			toolResultMsg := providers.Message{
				Role:       "tool",
				Content:    contentForLLM,
				ToolCallID: r.tc.ID,
			}
			// Mark failed tool results so the eviction sweep never treats a failed
			// write (file unchanged) as superseding the read it needs to correct it.
			if r.result.IsError {
				toolResultMsg.Type = providers.MessageTypeToolError
			}
			switch visionMode {
			case config.VisionToolResponse:
				// Attach images to the tool result itself (Responses API).
				toolResultMsg.Media = r.result.Images
			case config.VisionUserMessage:
				// Defer to a follow-up user turn (Chat Completions, where tool
				// messages are text-only).
				toolImages = append(toolImages, r.result.Images...)
			default:
				// VisionOff: the model can't see images. Accumulate them for the
				// vision side-model (Flow A) instead of dropping them silently.
				if len(r.result.Images) > 0 {
					offImages = append(offImages, r.result.Images...)
					if f := strings.TrimSpace(r.result.ForLLM); f != "" {
						offFocus = append(offFocus, f)
					}
				}
			}
			messages = append(messages, toolResultMsg)

			// Save tool result message through the context manager so that
			// msgCount is incremented and the message is written to the archive.
			if seq, err := cm.AddToolResult(ctx, toolResultMsg); err != nil {
				logger.WarnCF("agent", "AddToolResult failed", map[string]any{
					"agent_id": agent.ID,
					"error":    err.Error(),
				})
			} else {
				mem.Observe(ctx, seq, toolResultMsg)
			}
		}

		// VisionUserMessage mode: images can't ride on a tool message on Chat
		// Completions, so hand them to the model as one follow-up user turn.
		if len(toolImages) > 0 {
			imgMsg := providers.Message{
				Role:    "user",
				Content: "Image(s) returned by the tool call(s) above:",
				Media:   toolImages,
			}
			messages = append(messages, imgMsg)
			if seq, err := cm.AddUserMessage(ctx, imgMsg); err != nil {
				logger.WarnCF("agent", "failed to persist tool image message",
					map[string]any{"agent_id": agent.ID, "error": err.Error()})
			} else {
				mem.Observe(ctx, seq, imgMsg)
			}
			logger.InfoCF("agent", "passed tool image(s) to vision model (user message)",
				map[string]any{"agent_id": agent.ID, "model": activeModel, "images": len(toolImages)})
		}

		// Flow A: the active model is text-only and a vision side-model is
		// configured — describe the tool image(s) that would otherwise be dropped
		// and inject the description as a follow-up user turn so the model can use
		// it. Gated on VisionClients so an unconfigured deployment gets no
		// description — just the hidden-attachment note from messagesForModel.
		if visionMode == config.VisionOff && len(agent.VisionClients) > 0 && len(offImages) > 0 {
			focusParts := offFocus
			if lu := strings.TrimSpace(opts.UserMessage); lu != "" {
				focusParts = append(focusParts, "User's request: "+lu)
			}
			desc, ok := al.describeImages(ctx, agent, offImages, strings.Join(focusParts, "\n"))
			content := "An image was returned by a tool, but this model cannot view images and no description could be produced."
			if ok {
				content = "Image(s) described for you (this model cannot view images):\n" + desc
			}
			descMsg := providers.Message{Role: "user", Content: content}
			messages = append(messages, descMsg)
			if seq, err := cm.AddUserMessage(ctx, descMsg); err != nil {
				logger.WarnCF("agent", "failed to persist vision-describe message",
					map[string]any{"agent_id": agent.ID, "error": err.Error()})
			} else {
				mem.Observe(ctx, seq, descMsg)
			}
			logger.InfoCF("agent", "injected vision description for non-vision model",
				map[string]any{"agent_id": agent.ID, "model": activeModel, "images": len(offImages), "described": ok})
		}

		// If the model just repeated an identical tool-call batch (it isn't
		// working), steer it before the next iteration: a generic message that
		// applies to ANY tool, with a file_edit-specific hint only when that tool
		// is the one being repeated. identicalToolBatches > 0 means this batch
		// matched the previous one.
		if identicalToolBatches > 0 {
			n := identicalToolBatches + 1
			guidance := fmt.Sprintf(
				"⚠️ You have made the exact same tool call (%s) %d times in a row and it is not working — stop repeating the identical call. Change your approach: adjust the arguments, try a different tool, or explain the problem to the user instead of retrying the same thing.",
				strings.Join(toolNames, ", "), n)
			if slices.Contains(toolNames, "file_edit") {
				guidance += " For file_edit specifically: the file may have changed since you last read it, or your old_text does not match exactly (e.g. whitespace/indentation) — re-read it with file_read_lines and correct the old_text."
			}
			messages = append(messages, providers.Message{Role: "user", Content: guidance})
			logger.InfoCF("agent", "steering model away from repeated identical tool call",
				map[string]any{"agent_id": agent.ID, "iteration": iteration, "repeats": n, "tools": toolNames})
		}

		// Tick down TTL of discovered tools after processing tool results.
		// Only reached when tool calls were made (the loop continues);
		// the break on no-tool-call responses skips this.
		// NOTE: This is safe because processMessage is sequential per agent.
		// If per-agent concurrency is added, TTL consistency between
		// ToProviderDefs and Get must be re-evaluated.
		agent.Tools.TickTTL()
		logger.DebugCF("agent", "TTL tick after tool execution", map[string]any{
			"agent_id": agent.ID, "iteration": iteration,
		})
	}

	return finalContent, lastNormal, degenerate, lastFinishReason, iteration, nil
}

// resolveRunProvider returns the LLMProvider (and matching model id) that will
// serve this turn's primary chat dispatch. When activeCandidates has at least
// one entry the first candidate's (protocol, model) pair is resolved through
// the per-model dispatcher; otherwise the agent's primary model is resolved
// through the dispatcher in the same way buildDefaultCompressLLMClient does.
//
// Falls back to (agent.Provider, activeModel) only when the dispatcher cannot
// satisfy the request — mirroring the compress-empty-fallback safety net in
// context_manager.go. This prevents the type-assertion + dispatch sites from
// silently consulting the shared agent.Provider (which on the shipped default
// config is claude-cli for every agent) for non-claude-cli primaries.
func (al *AgentLoop) resolveRunProvider(
	agent *AgentInstance,
	activeCandidates []providers.FallbackCandidate,
	activeModel string,
) (providers.LLMProvider, string) {
	if al.dispatcher != nil {
		var alias, modelID string
		if len(activeCandidates) > 0 {
			alias = strings.TrimSpace(activeCandidates[0].Alias)
			modelID = strings.TrimSpace(activeCandidates[0].Model)
		} else if a, m, ok := resolveCompressModelTarget(al.GetConfig(), strings.TrimSpace(agent.Model)); ok {
			alias, modelID = a, m
		}
		if alias != "" {
			if p, err := al.dispatcher.Get(alias); err == nil {
				return p, modelID
			}
		}
	}
	return agent.Provider, activeModel
}

// selectCandidates returns the model candidates and resolved model name to use
// for a conversation turn, honouring the session's active model selection.
//
// The active model (by per-session index, default 0) is moved to the front of a
// copy of the agent's candidate list; the remaining candidates keep their
// original order so the fallback chain still applies. agent.Candidates is never
// mutated.
//
// The returned (candidates, model) pair is used for all LLM calls within one
// turn so that a multi-step tool chain doesn't switch models mid-way.
func (al *AgentLoop) selectCandidates(
	agent *AgentInstance,
	sessionKey string,
) (candidates []providers.FallbackCandidate, model string) {
	if len(agent.Candidates) == 0 {
		return agent.Candidates, agent.Model
	}

	idx := al.getActiveModelIndex(agent, sessionKey)

	// Move-to-front of idx: selected first, then the rest in original order.
	reordered := make([]providers.FallbackCandidate, 0, len(agent.Candidates))
	reordered = append(reordered, agent.Candidates[idx])
	for i := range agent.Candidates {
		if i == idx {
			continue
		}
		reordered = append(reordered, agent.Candidates[i])
	}

	model = reordered[0].Alias
	if model == "" {
		model = agent.Model
	}
	return reordered, model
}

// messagesForModel returns messages suitable for the named model (keyed by
// alias / model_name). Vision-capable models get the slice unchanged; models
// with vision off or unset get a shallow copy with image Media removed. Some
// providers (e.g. deepseek via OpenRouter) reject the ENTIRE request with a 404
// when an image part is present, so a screenshot left in conversation history
// must not be replayed to a non-vision model. The persisted originals are never
// mutated, so a later turn on a vision model still surfaces the image. Unknown
// model is treated as no-vision, matching the tool-image injection default.
// reasoningPlaceholder is what backfillReasoningContent writes into an empty
// reasoning_content. A single space, deliberately: DeepSeek V4 Pro rejects the
// empty string as "not passed back", so the value must be non-empty, and
// anything longer would be fabricated reasoning the model never produced.
const reasoningPlaceholder = " "

// backfillReasoningContent gives every assistant message a non-empty
// reasoning_content when the target provider demands one.
//
// DeepSeek V4 thinking mode requires the field on every assistant message in
// history whenever the request carries tools — including turns that made no tool
// call — and answers 400 without it. Messages written by a model that records no
// reasoning (any CLI provider, or the same model with thinking off) therefore
// wedge a session the moment it is pointed at DeepSeek: the same history replays
// every turn and is rejected every turn, and the fallback chain cannot rescue it
// because a 400 is not retriable.
//
// Three conditions, all necessary:
//   - the provider opts in via require_reasoning_content;
//   - the request carries tools, since without them DeepSeek ignores the field
//     entirely and there is nothing to satisfy;
//   - the message is an assistant turn whose reasoning_content is empty. Real
//     reasoning is never touched, so a thinking model's own output round-trips
//     unchanged.
//
// The input slice is not modified: callers share `messages` across fallback
// candidates, and one candidate's wire quirk must not follow the history to the
// next.
func (al *AgentLoop) backfillReasoningContent(
	messages []providers.Message,
	mc *config.ModelConfig,
	mcErr error,
	hasTools bool,
) []providers.Message {
	if !hasTools || mcErr != nil || mc == nil || mc.Provider == "" {
		return messages
	}
	prov, err := al.GetConfig().GetProvider(mc.Provider)
	if err != nil || prov == nil || !prov.RequireReasoningContent {
		return messages
	}

	// Copy lazily: most turns need no backfill at all once a thinking model has
	// been running, and copying the whole slice per dispatch is wasted work.
	out := messages
	copied := false
	for i := range messages {
		if messages[i].Role != "assistant" || messages[i].ReasoningContent != "" {
			continue
		}
		if !copied {
			out = make([]providers.Message, len(messages))
			copy(out, messages)
			copied = true
		}
		out[i].ReasoningContent = reasoningPlaceholder
	}
	return out
}

func (al *AgentLoop) messagesForModel(messages []providers.Message, modelKey string, hasTools bool) []providers.Message {
	mc, mcErr := al.GetConfig().GetModelConfig(modelKey)

	// Reasoning backfill runs first and independently of the media rules below:
	// it is a wire-contract requirement, not a capability downgrade.
	messages = al.backfillReasoningContent(messages, mc, mcErr, hasTools)

	if mcErr == nil && mc != nil {
		if mc.Vision == config.VisionUserMessage || mc.Vision == config.VisionToolResponse {
			return messages
		}
	}
	hasMedia := false
	for i := range messages {
		if len(messages[i].Media) > 0 {
			hasMedia = true
			break
		}
	}
	if !hasMedia {
		return messages
	}
	out := make([]providers.Message, len(messages))
	copy(out, messages)
	for i := range out {
		if len(out[i].Media) == 0 {
			continue
		}
		n := len(out[i].Media)
		out[i].Media = nil
		// Say so instead of stripping silently: the model should know the message
		// carried attachments it cannot see (any media:// refs remain in the text
		// via the attachment marker and can be delegated via agent_spawn).
		out[i].Content = strings.TrimRight(out[i].Content, "\n") +
			fmt.Sprintf("\n[%d attachment(s) on this message are hidden — the current model cannot view images]", n)
	}
	return out
}

// formatMessagesForLog formats messages for logging
func formatMessagesForLog(messages []providers.Message) string {
	if len(messages) == 0 {
		return "[]"
	}

	var sb strings.Builder
	sb.WriteString("[\n")
	for i, msg := range messages {
		fmt.Fprintf(&sb, "  [%d] Role: %s\n", i, msg.Role)
		if len(msg.ToolCalls) > 0 {
			sb.WriteString("  ToolCalls:\n")
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&sb, "    - ID: %s, Type: %s, Name: %s\n", tc.ID, tc.Type, tc.Name)
				if tc.Function != nil {
					fmt.Fprintf(
						&sb,
						"      Arguments: %s\n",
						utils.Truncate(tc.Function.Arguments, 200),
					)
				}
			}
		}
		if msg.Content != "" {
			content := utils.Truncate(msg.Content, 200)
			fmt.Fprintf(&sb, "  Content: %s\n", content)
		}
		if msg.ToolCallID != "" {
			fmt.Fprintf(&sb, "  ToolCallID: %s\n", msg.ToolCallID)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("]")
	return sb.String()
}

// formatToolsForLog formats tool definitions for logging
func formatToolsForLog(toolDefs []providers.ToolDefinition) string {
	if len(toolDefs) == 0 {
		return "[]"
	}

	var sb strings.Builder
	sb.WriteString("[\n")
	for i, tool := range toolDefs {
		fmt.Fprintf(&sb, "  [%d] Type: %s, Name: %s\n", i, tool.Type, tool.Function.Name)
		fmt.Fprintf(&sb, "      Description: %s\n", tool.Function.Description)
		if len(tool.Function.Parameters) > 0 {
			fmt.Fprintf(
				&sb,
				"      Parameters: %s\n",
				utils.Truncate(fmt.Sprintf("%v", tool.Function.Parameters), 200),
			)
		}
	}
	sb.WriteString("]")
	return sb.String()
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

// dumpRefusal writes a diagnostic file capturing the full LLM input and output
// when the provider returns finish_reason "refusal". Called only when
// cfg.Logging.DumpRefusals is true and dumpsDir is set.
func (al *AgentLoop) dumpRefusal(
	agent *AgentInstance,
	messages []providers.Message,
	response *providers.LLMResponse,
	opts processOptions,
	model string,
	iteration int,
) {
	inputBytes, _ := json.Marshal(messages)
	outputBytes, _ := json.Marshal(response)

	meta := map[string]any{
		"agent":     agent.ID,
		"model":     model,
		"session":   opts.SessionKey,
		"channel":   opts.Channel,
		"iteration": iteration,
		"timestamp": time.Now().Format(time.RFC3339),
	}

	basename, err := dump.Write(al.dumpsDir, "refusal", meta, json.RawMessage(inputBytes), json.RawMessage(outputBytes))
	if err != nil {
		logger.WarnCF("agent", "Failed to write refusal dump",
			map[string]any{"agent_id": agent.ID, "error": err.Error()})
		return
	}
	logger.WarnCF("agent", "LLM refusal detected — dump written",
		map[string]any{
			"agent_id":    agent.ID,
			"model":       model,
			"session_key": opts.SessionKey,
			"channel":     opts.Channel,
			"iteration":   iteration,
			"dump_base":   basename,
		})
}

// dumpAll writes a diagnostic file capturing the full LLM input and output
// for every response when cfg.Logging.DumpAll is true and dumpsDir is set.
func (al *AgentLoop) dumpAll(
	agent *AgentInstance,
	messages []providers.Message,
	response *providers.LLMResponse,
	opts processOptions,
	model string,
	iteration int,
) {
	inputBytes, _ := json.Marshal(messages)
	outputBytes, _ := json.Marshal(response)

	meta := map[string]any{
		"agent":         agent.ID,
		"model":         model,
		"session":       opts.SessionKey,
		"channel":       opts.Channel,
		"iteration":     iteration,
		"finish_reason": response.FinishReason,
		"timestamp":     time.Now().Format(time.RFC3339),
	}

	basename, err := dump.Write(al.dumpsDir, "dump_all", meta, json.RawMessage(inputBytes), json.RawMessage(outputBytes))
	if err != nil {
		logger.WarnCF("agent", "Failed to write dump_all file",
			map[string]any{"agent_id": agent.ID, "error": err.Error()})
		return
	}
	logger.DebugCF("agent", "LLM response dump written (dump_all)",
		map[string]any{
			"agent_id":      agent.ID,
			"model":         model,
			"session_key":   opts.SessionKey,
			"finish_reason": response.FinishReason,
			"iteration":     iteration,
			"dump_base":     basename,
		})
}
