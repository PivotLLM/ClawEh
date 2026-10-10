// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

// llmMaxRetries is how many times one dispatch is retried after a timeout or
// a context-window overflow.
const llmMaxRetries = 2

// timeoutRetryBackoff is the step of the backoff before retrying a timed-out
// dispatch: the n-th retry waits n steps, jittered.
const timeoutRetryBackoff = 5 * time.Second

// llmDispatch is one iteration's model request.
type llmDispatch struct {
	defs []providers.ToolDefinition
	// llmOpts are the request options; a context-window retry may lower
	// max_tokens.
	llmOpts map[string]any
	// runProvider and runModel serve the request when no fallback chain runs.
	runProvider providers.LLMProvider
	runModel    string
	// provider is the protocol named in the finish event, updated when the
	// fallback chain lands on another candidate.
	provider string
	// promptEstimate is the engine's prompt-token estimate for the request.
	promptEstimate int
}

// newDispatch builds the request for this iteration. The provider that will
// actually serve it decides which options apply, rather than the shared
// agent.Provider (claude-cli for every agent on the default config).
func (t *llmTurn) newDispatch(defs []providers.ToolDefinition, promptEstimate int) *llmDispatch {
	agent := t.agent
	runProvider, runModel := t.al.resolveRunProvider(agent, t.candidates, t.model)

	llmOpts := map[string]any{
		"max_tokens":       agent.MaxTokens,
		"prompt_cache_key": agent.ID,
	}
	// CLI providers run a subprocess and take no HTTP request parameters.
	if _, isCLI := runProvider.(providers.CLIProvider); !isCLI {
		llmOpts["temperature"] = agent.Temperature
	}
	// parseThinkingLevel guarantees ThinkingOff for empty/unknown values.
	if agent.ThinkingLevel != ThinkingOff {
		if tc, ok := runProvider.(providers.ThinkingCapable); ok && tc.SupportsThinking() {
			llmOpts["thinking_level"] = string(agent.ThinkingLevel)
		} else {
			logger.WarnCF("agent", "thinking_level is set but current provider does not support it, ignoring",
				map[string]any{"agent_id": agent.ID, "thinking_level": string(agent.ThinkingLevel)})
		}
	}
	// Providers that cannot stream text deltas (CLI, Anthropic) ignore this.
	if t.stream != nil {
		llmOpts[providers.TextDeltaOption] = t.stream.Add
	}

	provider := ""
	if len(t.candidates) > 0 {
		provider = t.candidates[0].Provider
	}
	return &llmDispatch{
		defs:           defs,
		llmOpts:        llmOpts,
		runProvider:    runProvider,
		runModel:       runModel,
		provider:       provider,
		promptEstimate: promptEstimate,
	}
}

// requestModel sends the dispatch, retrying a timeout after a backoff and a
// context-window overflow after compressing, up to llmMaxRetries times. An
// error it returns ends the turn.
func (t *llmTurn) requestModel(ctx context.Context, d *llmDispatch) (*providers.LLMResponse, error) {
	var err error
	for retry := 0; retry <= llmMaxRetries; retry++ {
		logger.InfoCF("agent", "LLM dispatch", map[string]any{
			"agent_id":     t.agent.ID,
			"iteration":    t.iteration,
			"provider":     d.provider,
			"model":        t.model,
			"num_messages": len(t.messages),
			"num_tools":    len(d.defs),
			"max_tokens":   t.agent.MaxTokens,
		})
		dispatchStart := time.Now()
		var response *providers.LLMResponse
		response, err = t.callModel(ctx, d)
		emitLLMFinishEvent(t.agent.ID, t.iteration, d.provider, t.model, dispatchStart, response, err)
		if err == nil {
			addTurnUsage(t.opts.UsageOut, response, d.provider, t.model)
			if d.promptEstimate > 0 && response != nil && response.Usage != nil && response.Usage.PromptTokens > 0 {
				t.cm.ObserveUsage(d.promptEstimate, response.Usage.PromptTokens)
			}
			return response, nil
		}

		isTimeoutError, isContextError := classifyLLMError(err, d.provider, t.model)
		if isTimeoutError && retry < llmMaxRetries {
			if waitErr := waitToRetry(ctx, err, retry); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		if isContextError && retry < llmMaxRetries {
			t.compressForRetry(ctx, d, err, retry)
			continue
		}
		break
	}
	return nil, t.dispatchFailed(ctx, err)
}

// waitToRetry waits out the backoff before retrying a timed-out dispatch. It
// returns the context's error when the turn's budget ran out first; such a
// turn is not retried, so no retry is logged.
func waitToRetry(ctx context.Context, err error, retry int) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	backoff := withJitter(time.Duration(retry+1) * timeoutRetryBackoff)
	logger.WarnCF("agent", "Timeout error, retrying after backoff", map[string]any{
		"error":   err.Error(),
		"retry":   retry,
		"backoff": backoff.String(),
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(backoff):
		return nil
	}
}

// compressForRetry answers a context-window error: the user is told once,
// the session is force-compressed and the request reassembled. When that
// does not shrink the history, max_tokens is halved instead, since the
// provider's per-request limit may be below the model's stated maximum
// (e.g. Groq's free tier).
func (t *llmTurn) compressForRetry(ctx context.Context, d *llmDispatch, err error, retry int) {
	logger.WarnCF(
		"agent",
		"Context window error detected, attempting compression",
		map[string]any{
			"error": err.Error(),
			"retry": retry,
		},
	)

	if retry == 0 && !constants.IsInternalChannel(t.opts.Channel) {
		if pubErr := t.al.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Channel: t.opts.Channel,
			ChatID:  t.opts.ChatID,
			Content: "Context window exceeded. Compressing history and retrying...",
		}); pubErr != nil {
			logger.WarnCF("agent", "Failed to publish compression notice",
				map[string]any{"error": pubErr.Error(), "channel": t.opts.Channel})
		}
	}

	prevMsgCount := len(t.messages)
	comprMgr, releaseComprMgr := t.al.getContextManager(t.agent, t.opts.SessionKey) //nolint:contextcheck // compaction reporter: ctxengine's callback has no context, so it publishes on its own
	t.releases = append(t.releases, releaseComprMgr)
	if ferr := comprMgr.ForceCompress(ctx); ferr != nil {
		logger.WarnCF("agent", "force compression failed",
			map[string]any{"error": ferr.Error(), "session": t.opts.SessionKey})
	}
	if asm, berr := comprMgr.Assemble(ctx, t.al.assembleRequestWithDefs(ctx, t.agent, t.mem, t.opts, d.defs)); berr == nil {
		t.messages = asm.Messages
		d.promptEstimate = asm.PromptTokenEstimate
	}

	if len(t.messages) < prevMsgCount {
		return
	}
	if current, ok := d.llmOpts["max_tokens"].(int); ok && current > 512 {
		reduced := current / 2
		d.llmOpts["max_tokens"] = reduced
		logger.WarnCF("agent", "History already minimal; reducing max_tokens for retry",
			map[string]any{
				"agent_id":       t.agent.ID,
				"old_max_tokens": current,
				"new_max_tokens": reduced,
			})
	}
}

// dispatchFailed is the error a dispatch that failed for good ends the turn
// with: an interruption by shutdown, a deliberate stop (/cancel, or an asker
// that stopped waiting), or a failure.
func (t *llmTurn) dispatchFailed(ctx context.Context, err error) error {
	if shuttingDown(ctx) {
		return fmt.Errorf("LLM call interrupted: %w", context.Cause(ctx))
	}
	if stoppedOnPurpose(ctx) {
		logger.InfoCF("agent", "LLM call stopped: the turn was cancelled",
			map[string]any{
				"agent_id":  t.agent.ID,
				"iteration": t.iteration,
				"model":     t.model,
				"cause":     context.Cause(ctx).Error(),
			})
		return fmt.Errorf("LLM call stopped: %w: %w", context.Cause(ctx), err)
	}
	logger.ErrorCF("agent", "LLM call failed",
		map[string]any{
			"agent_id":  t.agent.ID,
			"iteration": t.iteration,
			"model":     t.model,
			"error":     err.Error(),
		})
	return fmt.Errorf("LLM call failed after retries: %w", err)
}

// callModel makes one call for the dispatch: through the fallback chain when
// the turn has candidates, else straight to the provider resolved for the
// agent's primary model, so a non-default protocol is never routed through
// the shared agent.Provider.
func (t *llmTurn) callModel(ctx context.Context, d *llmDispatch) (*providers.LLMResponse, error) {
	al := t.al
	al.activeRequests.Add(1)
	defer al.activeRequests.Done()

	hasTools := len(d.defs) > 0
	if len(t.candidates) == 0 || al.fallback == nil {
		return d.runProvider.Chat(ctx, al.messagesForModel(t.messages, d.runModel, hasTools), d.defs, d.runModel, d.llmOpts)
	}
	fbResult, err := al.fallback.ExecuteWithNotify(
		ctx,
		t.candidates,
		func(ctx context.Context, c providers.FallbackCandidate) (*providers.LLMResponse, error) {
			// Image parts are dropped for a candidate whose model cannot take
			// them: a screenshot in history would otherwise fail every turn on
			// a non-vision candidate. Keyed by alias like GetModelConfig.
			modelKey := c.Alias
			if modelKey == "" {
				modelKey = c.Model
			}
			msgs := al.messagesForModel(t.messages, modelKey, hasTools)
			if al.dispatcher != nil {
				key := c.Alias
				if key == "" {
					key = c.Provider + "/" + c.Model
				}
				if p, getErr := al.dispatchProvider(t.agent, key); getErr == nil {
					return p.Chat(ctx, msgs, d.defs, c.Model, d.llmOpts)
				}
			}
			return t.agent.Provider.Chat(ctx, msgs, d.defs, c.Model, d.llmOpts)
		},
		t.notifier,
	)
	if err != nil {
		return nil, err
	}
	if fbResult.Provider != "" {
		d.provider = fbResult.Provider
		if len(fbResult.Attempts) > 0 {
			t.logFallbackAttempts(fbResult)
		}
	}
	return fbResult.Response, nil
}

// logFallbackAttempts logs a call the fallback chain answered after passing
// over candidates: the one that succeeded, then each one skipped or failed.
func (t *llmTurn) logFallbackAttempts(fbResult *providers.FallbackResult) {
	logger.InfoCF(
		"agent",
		fmt.Sprintf("Fallback: succeeded with %s/%s after %d attempts",
			fbResult.Provider, fbResult.Model, len(fbResult.Attempts)+1),
		map[string]any{"agent_id": t.agent.ID, "iteration": t.iteration},
	)
	for _, attempt := range fbResult.Attempts {
		if attempt.Skipped {
			logger.WarnCF("agent", "Fallback: skipped candidate (cooldown)",
				map[string]any{
					"agent_id":  t.agent.ID,
					"provider":  attempt.Provider,
					"model":     attempt.Model,
					"reason":    attempt.Reason,
					"remaining": attempt.Remaining.Round(time.Second),
					"error":     attempt.Error,
				})
			continue
		}
		logger.WarnCF("agent", "Fallback: candidate failed",
			map[string]any{
				"agent_id": t.agent.ID,
				"provider": attempt.Provider,
				"model":    attempt.Model,
				"reason":   attempt.Reason,
				"duration": attempt.Duration.Round(time.Millisecond),
				"error":    attempt.Error,
			})
	}
}

// dispatchProvider resolves the model alias to a provider for agent through
// the dispatcher. A fresh temporary agent gets an isolated provider: a CLI
// model runs in its own workspace without bypass flags (GetIsolated).
func (al *AgentLoop) dispatchProvider(agent *AgentInstance, alias string) (providers.LLMProvider, error) {
	if agent != nil && agent.Spec.Fresh {
		return al.dispatcher.GetIsolated(alias, agent.Workspace)
	}
	return al.dispatcher.Get(alias)
}

// resolveRunProvider returns the LLMProvider (and matching model id) that will
// serve this turn's primary chat dispatch. When activeCandidates has at least
// one entry the first candidate's (protocol, model) pair is resolved through
// the per-model dispatcher; otherwise the agent's primary model is resolved
// through the dispatcher in the same way resolveDefaultCompressClient does.
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
			if p, err := al.dispatchProvider(agent, alias); err == nil {
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

// messagesForModel returns messages suitable for the named model (keyed by
// alias / model_name). Vision-capable models get the slice unchanged; models
// with vision off or unset get a shallow copy with image Media removed. Some
// providers (e.g. deepseek via OpenRouter) reject the ENTIRE request with a 404
// when an image part is present, so a screenshot left in conversation history
// must not be replayed to a non-vision model. The persisted originals are never
// mutated, so a later turn on a vision model still surfaces the image. Unknown
// model is treated as no-vision, matching the tool-image injection default.
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

// backoffJitterFraction spreads a retry backoff by ±20% so concurrent turns
// retrying the same provider do not land on it in lockstep.
const backoffJitterFraction = 0.2

// withJitter returns d scaled by a random factor in [1-jitter, 1+jitter].
func withJitter(d time.Duration) time.Duration {
	f := 1 + backoffJitterFraction*(2*rand.Float64()-1) //nolint:gosec // retry spread, not security-relevant
	return time.Duration(float64(d) * f)
}

// localContextLimitPatterns are context-window/token-limit markers that
// spawnllm's ClassifyError does not recognise. Candidates for upstreaming;
// remove each one here once spawnllm classifies it.
var localContextLimitPatterns = []string{
	"context window",
	"token limit",
	"max_tokens",
	"invalidparameter",
}

// classifyLLMError sorts a dispatch error into the two retry buckets the turn
// acts on: a transient timeout (retry after backoff) or a context-window
// overflow (compress and retry). spawnllm's classifier is the source of
// truth; the local patterns cover only what it does not classify.
func classifyLLMError(err error, provider, model string) (isTimeout, isContext bool) {
	cls := providers.ClassifyError(err, provider, model)
	if errors.Is(err, context.DeadlineExceeded) || (cls != nil && cls.Reason == providers.FailoverTimeout) {
		return true, false
	}
	if cls != nil && cls.Reason == providers.FailoverContextLimit {
		return false, true
	}
	msg := strings.ToLower(err.Error())
	for _, p := range localContextLimitPatterns {
		if strings.Contains(msg, p) {
			return false, true
		}
	}
	var exhausted *providers.FallbackExhaustedError
	if errors.As(err, &exhausted) && exhausted.AllContextLimit() {
		return false, true
	}
	return false, false
}
