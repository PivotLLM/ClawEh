// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/PivotLLM/cogmem"
	"github.com/PivotLLM/ctxengine"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

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

// llmTurn is one turn's model loop: what the turn was started with and the
// state carried from one iteration to the next.
type llmTurn struct {
	al    *AgentLoop
	agent *AgentInstance
	opts  processOptions
	cm    ctxengine.ContextManager
	mem   *cogmem.Session

	// messages is the request as sent: stored history plus this turn's
	// unsaved steering messages, with media already resolved.
	messages []providers.Message
	// candidates and model are the turn's model choice. It is sticky for every
	// iteration, so a multi-step tool chain never switches model mid-way.
	candidates []providers.FallbackCandidate
	model      string
	// notifier spans the whole turn so its de-dup memory covers all
	// iterations: a primary that fails over on every iteration posts its
	// heads-up once.
	notifier providers.FallbackNotify
	// showToolActivity posts a breadcrumb per tool call (/tools on).
	showToolActivity bool
	// stream forwards partial text to a streaming channel; nil otherwise.
	stream *streamCoalescer
	// completedTools feeds the progress heartbeat.
	completedTools atomic.Int64
	// evicted collects the turn's context evictions for one notice at the end.
	evicted []ctxengine.EvictionEvent
	// releases are the context managers taken for compression, held until
	// the turn ends.
	releases []func()

	iteration    int
	finalContent string
	lastNormal   bool
	finishReason string
	// degenerate marks a reply that stayed empty after every poke.
	degenerate   bool
	emptyRetries int
	// lastToolSig and identicalToolBatches detect a model repeating the
	// exact same tool-call batch.
	lastToolSig          string
	identicalToolBatches int
}

// runLLMIteration executes the LLM call loop with tool handling. It returns
// the final reply, whether the last response ended normally, whether it was a
// degenerate empty reply, the finish reason and the number of iterations.
func (al *AgentLoop) runLLMIteration(
	ctx context.Context,
	agent *AgentInstance,
	messages []providers.Message,
	opts processOptions,
	cm ctxengine.ContextManager,
	mem *cogmem.Session,
) (string, bool, bool, string, int, error) {
	// Provider error logs name the agent without correlating timestamps.
	ctx = providers.WithAgentID(ctx, agent.ID)
	t := al.newLLMTurn(ctx, agent, messages, opts, cm, mem)

	// Progress heartbeat: keep a long turn visibly alive by editing its
	// placeholder every progress_interval with a running tool-call count.
	stopProgress := al.startProgressUpdates(ctx, opts.Channel, opts.ChatID,
		al.GetConfig().Agents.Defaults.GetProgressInterval(), &t.completedTools)
	defer stopProgress()
	defer t.publishEvictionNotice(ctx)
	if t.stream != nil {
		// Flush any buffered remainder that never hit a boundary before the
		// turn's terminal reply is published, so no trailing partial text is
		// lost.
		defer t.stream.Flush()
	}
	defer t.releaseCompressionManagers()

	t.logDispatchTarget()
	for t.iteration < agent.MaxIterations {
		t.iteration++
		done, err := t.iterate(ctx)
		if err != nil {
			return "", false, false, "", t.iteration, err
		}
		if done {
			break
		}
	}
	return t.finalContent, t.lastNormal, t.degenerate, t.finishReason, t.iteration, nil
}

// newLLMTurn prepares the model loop of one turn.
func (al *AgentLoop) newLLMTurn(
	ctx context.Context,
	agent *AgentInstance,
	messages []providers.Message,
	opts processOptions,
	cm ctxengine.ContextManager,
	mem *cogmem.Session,
) *llmTurn {
	t := &llmTurn{
		al:           al,
		agent:        agent,
		opts:         opts,
		cm:           cm,
		mem:          mem,
		messages:     messages,
		finishReason: "max_iterations",
	}
	t.candidates, t.model = al.selectCandidates(agent, opts.SessionKey)
	t.notifier = al.fallbackNotifier(ctx, opts)
	// Breadcrumbs go to a user chat, never the internal "system" channel.
	t.showToolActivity = opts.Channel != "" && opts.Channel != "system" && opts.ChatID != "" &&
		al.getShowToolActivity(agent, opts.SessionKey)

	// Partial-text streaming: a channel that opts in gets the model's text as
	// it is generated, coalesced into sentence/length chunks. One forwarder
	// serves every model call in the turn.
	if streamToolNarration && al.channelManager != nil && al.channelManager.SupportsStreaming(opts.Channel) {
		channel, chatID := opts.Channel, opts.ChatID
		t.stream = newStreamCoalescer(func(batch string) {
			al.channelManager.StreamDelta(ctx, channel, chatID, batch)
		})
	}
	return t
}

// releaseCompressionManagers releases the context managers taken to compress
// after a context-window error, last taken first.
func (t *llmTurn) releaseCompressionManagers() {
	for _, release := range slices.Backward(t.releases) {
		release()
	}
}

// publishEvictionNotice posts the turn's context evictions as one line when
// the agent's policy has notify_user on. An agent that re-reads the same file
// every iteration would otherwise flood the chat; the per-eviction detail is
// always in the DEBUG log.
func (t *llmTurn) publishEvictionNotice(ctx context.Context) {
	if len(t.evicted) == 0 || t.opts.Channel == "" || !t.al.evictionNotifyUser(t.agent) {
		return
	}
	if err := t.al.bus.PublishOutbound(ctx, bus.OutboundMessage{
		Channel: t.opts.Channel,
		ChatID:  t.opts.ChatID,
		Content: summarizeEvictions(t.evicted),
	}); err != nil {
		logger.WarnCF("agent", "Failed to publish eviction notice",
			map[string]any{"error": err.Error(), "channel": t.opts.Channel})
	}
}

// logDispatchTarget logs which model and provider the turn is routed to.
func (t *llmTurn) logDispatchTarget() {
	model := t.model
	provider := ""
	if len(t.candidates) > 0 {
		provider = t.candidates[0].Provider
		model = t.candidates[0].Model
	}
	fields := map[string]any{"agent_id": t.agent.ID, "model": model}
	if provider != "" {
		fields["provider"] = provider
	}
	logger.InfoCF("agent", "Dispatching to model", fields)
}

// iterate runs one model request and, when the model asked for tools, runs
// them. It reports done once the turn has its final answer.
func (t *llmTurn) iterate(ctx context.Context) (bool, error) {
	logger.DebugCF("agent", "LLM iteration",
		map[string]any{
			"agent_id":  t.agent.ID,
			"iteration": t.iteration,
			"max":       t.agent.MaxIterations,
		})

	defs := t.toolDefinitions()
	promptEstimate := t.assemble(ctx, defs)
	t.logRequest(defs)

	response, err := t.requestModel(ctx, t.newDispatch(defs, promptEstimate))
	if err != nil {
		return false, err
	}
	t.recordResponse(ctx, response)
	if len(response.ToolCalls) == 0 {
		return t.directAnswer(response), nil
	}
	return t.runToolCalls(ctx, response)
}

// toolDefinitions are the tools offered to the model in this dispatch. A
// sub-agent is an instance of its parent and is offered the parent's full
// toolset: recursion is bounded by the spawner's depth limit, not by
// withholding tools.
func (t *llmTurn) toolDefinitions() []providers.ToolDefinition {
	if t.agent.NoTools {
		return nil
	}
	return t.agent.Tools.ToProviderDefs()
}

// assemble prepares the context for this dispatch: the LLM-free eviction
// sweep first (cheap relief, so summarization fires far less often), then the
// safety-net compaction checks, then memory placement. The slice in hand is
// kept unless stored history was rewritten, because it carries this turn's
// unsaved steering messages and already-resolved media. The tool schemas'
// cost rides on the request so every trigger measures the real request.
//
// It returns the engine's estimate of the request's prompt tokens, which the
// provider's reported count calibrates after the call; 0 when assembly failed.
func (t *llmTurn) assemble(ctx context.Context, defs []providers.ToolDefinition) int {
	asm, err := t.cm.Assemble(ctx, t.al.assembleRequestWithDefs(ctx, t.agent, t.mem, t.opts, defs))
	if err != nil {
		logger.WarnCF("agent", "assemble failed (continuing with current slice)", map[string]any{
			"agent_id": t.agent.ID,
			"error":    err.Error(),
		})
		return 0
	}
	t.evicted = append(t.evicted, asm.Evictions...)
	if asm.Changed() {
		t.messages = asm.Messages
	}
	return asm.PromptTokenEstimate
}

// logRequest logs the session status and the request about to be sent; the
// full messages only when log_message_content allows it.
func (t *llmTurn) logRequest(defs []providers.ToolDefinition) {
	sessionSummary := t.agent.Sessions.GetSummary(t.opts.SessionKey)
	logger.InfoCF("agent", "Session status",
		map[string]any{
			"agent_id":    t.agent.ID,
			"messages":    len(t.messages),
			"compacted":   sessionSummary != "",
			"summary_len": len(sessionSummary),
			"tools":       len(defs),
			"model":       t.model,
		})

	systemPromptLen := 0
	if len(t.messages) > 0 {
		systemPromptLen = len(t.messages[0].Content)
	}
	logger.DebugCF("agent", "LLM request",
		map[string]any{
			"agent_id":          t.agent.ID,
			"iteration":         t.iteration,
			"model":             t.model,
			"messages_count":    len(t.messages),
			"tools_count":       len(defs),
			"max_tokens":        t.agent.MaxTokens,
			"temperature":       t.agent.Temperature,
			"system_prompt_len": systemPromptLen,
		})

	if logger.GetLogMessageContent() {
		logger.DebugCF("agent", "Full LLM request",
			map[string]any{
				"iteration":     t.iteration,
				"messages_json": formatMessagesForLog(t.messages),
				"tools_json":    formatToolsForLog(defs),
			})
	}
}

// recordResponse writes the diagnostic dumps, delivers the reasoning when
// the session asked for it, and logs the response.
func (t *llmTurn) recordResponse(ctx context.Context, response *providers.LLMResponse) {
	al := t.al
	if al.dumpsDir != "" {
		isRefusal := response.FinishReason == "refusal"
		if isRefusal && al.cfg.Logging.DumpRefusals {
			al.dumpRefusal(t.agent, t.messages, response, t.opts, t.model, t.iteration)
		} else if al.cfg.Logging.DumpAll {
			al.dumpAll(t.agent, t.messages, response, t.opts, t.model, t.iteration)
		}
	}

	// Reasoning is delivered only when the session opted in (/reasoning on):
	// to the configured reasoning channel if one exists, else inline in the
	// main chat so the toggle works on any channel.
	if al.getExposeReasoning(t.agent, t.opts.SessionKey) {
		reasoningTarget := al.targetReasoningChannelID(t.opts.Channel)
		inline := reasoningTarget == ""
		if inline {
			reasoningTarget = t.opts.ChatID
		}
		go al.handleReasoning(ctx, response.Reasoning, t.opts.Channel, reasoningTarget, inline)
	}

	respFields := map[string]any{
		"agent_id":        t.agent.ID,
		"iteration":       t.iteration,
		"content_chars":   len(response.Content),
		"tool_calls":      len(response.ToolCalls),
		"reasoning_chars": len(response.Reasoning),
		"target_channel":  al.targetReasoningChannelID(t.opts.Channel),
		"channel":         t.opts.Channel,
	}
	// Reasoning text is response body content.
	if logger.GetLogMessageContent() {
		respFields["reasoning"] = response.Reasoning
	}
	logger.DebugCF("agent", "LLM response", respFields)
}

// directAnswer takes a response without tool calls as the turn's answer. A
// response with reasoning but no visible content gets the model poked to
// reply, up to maxEmptyRetries times; it reports done=false while poking.
func (t *llmTurn) directAnswer(response *providers.LLMResponse) bool {
	t.finalContent = response.Content
	// "Thinking" may arrive as reasoning_content (OpenAI-style) or reasoning
	// (OpenRouter-style); either is the model's reasoning.
	reasoningText := response.ReasoningContent
	if reasoningText == "" {
		reasoningText = response.Reasoning
	}
	// Reasoning stands in for an empty reply only when the operator opts in: a
	// model degenerating into reasoning-only output must not leak its raw
	// chain-of-thought to the user, so empty content takes the graceful
	// empty-response path instead.
	if t.finalContent == "" && reasoningText != "" &&
		t.al.cfg != nil && t.al.cfg.Agents.Defaults.ShowReasoningAsContent {
		t.finalContent = reasoningText
	}
	t.lastNormal = response.Normal
	t.finishReason = response.FinishReason

	if t.finalContent == "" && reasoningText != "" {
		if t.emptyRetries < maxEmptyRetries {
			t.emptyRetries++
			t.messages = append(t.messages, providers.Message{
				Role:    "user",
				Content: "Your previous turn produced no reply. Make sure you have completed the user's request, then respond to the user now.",
			})
			logger.InfoCF("agent", "empty response with reasoning; poking model to reply",
				map[string]any{
					"agent_id": t.agent.ID, "iteration": t.iteration, "retry": t.emptyRetries,
				})
			return false
		}
		// Retries exhausted: the caller sends a fallback instead of going
		// silent.
		t.degenerate = true
	}

	logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
		map[string]any{
			"agent_id":      t.agent.ID,
			"iteration":     t.iteration,
			"content_chars": len(t.finalContent),
		})
	return true
}

// runToolCalls records the model's tool calls, runs them, and adds their
// results to the conversation. It reports done when the model kept repeating
// the same batch and the turn was stopped.
func (t *llmTurn) runToolCalls(ctx context.Context, response *providers.LLMResponse) (bool, error) {
	calls := make([]providers.ToolCall, 0, len(response.ToolCalls))
	for _, tc := range response.ToolCalls {
		calls = append(calls, providers.NormalizeToolCall(tc))
	}
	toolNames := make([]string, 0, len(calls))
	for _, tc := range calls {
		toolNames = append(toolNames, tc.Name)
	}
	if t.stopRepeatedBatch(calls, toolNames) {
		return true, nil
	}

	// Cognitive memory auto-loads domains whose triggers match a tool the
	// agent just used; the next assembly in this turn picks them up.
	t.mem.RecordToolUse(toolNames...)
	logger.InfoCF("agent", "LLM requested tool calls",
		map[string]any{
			"agent_id":  t.agent.ID,
			"tools":     toolNames,
			"count":     len(calls),
			"iteration": t.iteration,
		})
	t.publishNarration(ctx, response.Content)

	if err := t.keep(ctx, t.cm.AddToolCallMessage, assistantToolCallMessage(t.agent.ID, response, calls), "tool call message"); err != nil {
		return false, err
	}
	results := t.executeToolCalls(ctx, calls)
	t.completedTools.Add(int64(len(calls)))
	if err := t.addToolResults(ctx, results); err != nil {
		return false, err
	}
	t.steerFromRepeatedBatch(toolNames)

	// Tick down the TTL of discovered tools once tool results are in. Safe
	// because a session's turns run one at a time; per-agent concurrency would
	// need the TTL consistency between ToProviderDefs and Get re-evaluated.
	t.agent.Tools.TickTTL()
	logger.DebugCF("agent", "TTL tick after tool execution", map[string]any{
		"agent_id": t.agent.ID, "iteration": t.iteration,
	})
	return false, nil
}

// stopRepeatedBatch counts consecutive identical tool-call batches and, once
// maxIdenticalToolBatches is reached, ends the turn with a reply saying so
// rather than letting the model spin (e.g. re-writing the same memory).
func (t *llmTurn) stopRepeatedBatch(calls []providers.ToolCall, toolNames []string) bool {
	sig := toolCallSignature(calls)
	if sig == "" {
		return false
	}
	if sig == t.lastToolSig {
		t.identicalToolBatches++
	} else {
		t.identicalToolBatches = 0
		t.lastToolSig = sig
	}
	if t.identicalToolBatches+1 < maxIdenticalToolBatches {
		return false
	}
	logger.WarnCF("agent", "aborting: identical tool call repeated", map[string]any{
		"agent_id":  t.agent.ID,
		"iteration": t.iteration,
		"tools":     toolNames,
		"repeats":   t.identicalToolBatches + 1,
	})
	t.finalContent = fmt.Sprintf(
		"Stopped: I kept making the same %s call (%d times) without success, so I'm aborting to avoid a loop. Please check the request or rephrase it.",
		strings.Join(toolNames, ", "), t.identicalToolBatches+1)
	t.finishReason = "loop_detected"
	return true
}

// steerFromRepeatedBatch tells a model that just repeated its previous
// tool-call batch to change approach, before the next iteration. The message
// applies to any tool, with a file_edit hint when that is the tool repeated.
func (t *llmTurn) steerFromRepeatedBatch(toolNames []string) {
	if t.identicalToolBatches == 0 {
		return
	}
	n := t.identicalToolBatches + 1
	guidance := fmt.Sprintf(
		"⚠️ You have made the exact same tool call (%s) %d times in a row and it is not working — stop repeating the identical call. Change your approach: adjust the arguments, try a different tool, or explain the problem to the user instead of retrying the same thing.",
		strings.Join(toolNames, ", "), n)
	if slices.Contains(toolNames, "file_edit") {
		guidance += " For file_edit specifically: the file may have changed since you last read it, or your old_text does not match exactly (e.g. whitespace/indentation) — re-read it with file_read_lines and correct the old_text."
	}
	t.messages = append(t.messages, providers.Message{Role: "user", Content: guidance})
	logger.InfoCF("agent", "steering model away from repeated identical tool call",
		map[string]any{"agent_id": t.agent.ID, "iteration": t.iteration, "repeats": n, "tools": toolNames})
}

// publishNarration posts the text a model sent alongside its tool calls, only
// when stream_tool_activity is on: by default the user sees the final answer,
// not the model's "let me also check…" play-by-play.
func (t *llmTurn) publishNarration(ctx context.Context, content string) {
	if content == "" || t.opts.Channel == "" || !t.al.GetConfig().Agents.Defaults.StreamToolActivity {
		return
	}
	pubCtx, pubCancel := context.WithTimeout(ctx, publishTimeout)
	defer pubCancel()
	if err := t.al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: t.opts.Channel,
		ChatID:  t.opts.ChatID,
		Content: content,
	}); err != nil {
		logger.WarnCF("agent", "Failed to publish inter-tool narration",
			map[string]any{"error": err.Error(), "channel": t.opts.Channel})
	}
}

// keep adds msg to the request and records it through add (one of the
// context manager's Add methods), observing it into cognitive memory. A
// message the store refuses ends the turn; what names it in the error.
func (t *llmTurn) keep(
	ctx context.Context,
	add func(context.Context, providers.Message) (int64, error),
	msg providers.Message,
	what string,
) error {
	t.messages = append(t.messages, msg)
	seq, err := add(ctx, msg)
	if err != nil {
		return t.al.sessionStoreWriteFailed(t.agent, t.opts.SessionKey, what, err)
	}
	t.mem.Observe(ctx, seq, msg.Role, msg.Content)
	return nil
}

// assistantToolCallMessage is the assistant message carrying a response's
// tool calls, in the wire form stored in history.
func assistantToolCallMessage(agentID string, response *providers.LLMResponse, calls []providers.ToolCall) providers.Message {
	msg := providers.Message{
		Role:             "assistant",
		Content:          response.Content,
		ReasoningContent: response.ReasoningContent,
		// Responses reasoning items are replayed before this turn's
		// function_call on the next request (reasoning models with tools).
		ResponsesReasoning: response.ResponsesReasoning,
	}
	for _, tc := range calls {
		argumentsJSON, err := json.Marshal(tc.Arguments)
		if err != nil {
			logger.ErrorCF("agent", "Failed to marshal tool call arguments",
				map[string]any{
					"agent_id": agentID,
					"tool":     tc.Name,
					"error":    err.Error(),
				})
			argumentsJSON = []byte("{}")
		}
		// ExtraContent carries Gemini 3's thought_signature, which must be
		// persisted.
		thoughtSignature := ""
		if tc.Function != nil {
			thoughtSignature = tc.Function.ThoughtSignature
		}
		msg.ToolCalls = append(msg.ToolCalls, providers.ToolCall{
			ID:   tc.ID,
			Type: "function",
			Name: tc.Name,
			Function: &providers.FunctionCall{
				Name:             tc.Name,
				Arguments:        string(argumentsJSON),
				ThoughtSignature: thoughtSignature,
			},
			ExtraContent:     tc.ExtraContent,
			ThoughtSignature: thoughtSignature,
		})
	}
	return msg
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
	pubCtx, pubCancel := context.WithTimeout(ctx, publishTimeout)
	defer pubCancel()

	if err := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: channelName,
		ChatID:  channelID,
		Content: reasoningContent,
	}); err != nil {
		// Treat context.DeadlineExceeded / context.Canceled as expected
		// (bus full under load, or parent canceled).  Check the error
		// itself rather than ctx.Err(), because pubCtx may time out
		// while the parent ctx is still active.
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
