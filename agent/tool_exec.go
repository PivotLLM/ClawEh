// ClawEh
// License: MIT

package agent

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// toolCallResult is one tool call of a batch and what it returned.
type toolCallResult struct {
	tc     providers.ToolCall
	result *tools.ToolResult
}

// executeToolCalls runs a batch of tool calls in parallel and returns their
// results in call order. With /tools on, a breadcrumb per call is posted, in
// call order, before the call starts.
func (t *llmTurn) executeToolCalls(ctx context.Context, calls []providers.ToolCall) []toolCallResult {
	results := make([]toolCallResult, len(calls))
	// Per-tool budget: a tool's context is cancelled at the deadline so a
	// well-behaved tool returns a timeout error and the model can continue the
	// turn. The turn budget on ctx is the backstop for a tool that ignores
	// cancellation.
	toolTimeout := t.al.GetConfig().Agents.Defaults.GetToolTimeout()
	var wg sync.WaitGroup
	for i, tc := range calls {
		results[i].tc = tc
		if t.showToolActivity {
			t.publishBreadcrumb(ctx, tc)
		}
		wg.Go(func() { t.runToolCall(ctx, &results[i], toolTimeout) })
	}
	wg.Wait()
	return results
}

// publishBreadcrumb posts the one-line, privacy-safe note for a tool call.
func (t *llmTurn) publishBreadcrumb(ctx context.Context, tc providers.ToolCall) {
	line := toolCallBreadcrumb(tc)
	if line == "" {
		return
	}
	bcCtx, bcCancel := context.WithTimeout(ctx, publishTimeout)
	defer bcCancel()
	if err := t.al.bus.PublishOutbound(bcCtx, bus.OutboundMessage{
		Channel: t.opts.Channel,
		ChatID:  t.opts.ChatID,
		Content: line,
	}); err != nil {
		logger.WarnCF("agent", "Failed to publish tool breadcrumb",
			map[string]any{"error": err.Error(), "channel": t.opts.Channel})
	}
}

// runToolCall runs one tool call and stores its result in slot. A panicking
// tool becomes an error result, so it neither crashes the process nor
// wedges the batch.
func (t *llmTurn) runToolCall(ctx context.Context, slot *toolCallResult, toolTimeout time.Duration) {
	tc := slot.tc
	recorded := false // the sub-agent tool tally already has this call
	defer func() {
		if r := recover(); r != nil {
			logger.ErrorCF("agent", "panic in tool call",
				map[string]any{
					"tool":  tc.Name,
					"panic": fmt.Sprintf("%v", r),
					"stack": string(debug.Stack()),
				})
			slot.result = &tools.ToolResult{
				Err:    fmt.Errorf("tool %s panicked: %v", tc.Name, r),
				ForLLM: fmt.Sprintf("Tool %s failed with an internal error.", tc.Name),
			}
			if !recorded {
				tools.RecordToolResult(t.opts.SessionKey, tc.Name, tools.ErrorResult(slot.result.ForLLM))
			}
		}
	}()

	// Arguments are not logged: they routinely carry memory content, file
	// contents and other user data.
	logger.InfoCF("agent", "Tool call dispatched",
		turnFields(ctx, map[string]any{
			"agent_id":  t.agent.ID,
			"tool":      tc.Name,
			"iteration": t.iteration,
		}))

	asyncCallback := t.al.asyncToolCallback(ctx, t.agent, t.opts, tc.Name)

	// The agent's config is the allow checker, so execution enforces the
	// tool allowlist again (defense in depth).
	execCtx := tools.WithToolAllowChecker(ctx, t.agent.Config)
	execCtx = tools.WithSessionKey(execCtx, t.opts.SessionKey)
	if toolTimeout > 0 {
		var toolCancel context.CancelFunc
		execCtx, toolCancel = context.WithTimeout(execCtx, toolTimeout)
		defer toolCancel()
	}

	toolStart := time.Now()
	toolResult := t.agent.Tools.ExecuteWithContext(
		execCtx,
		tc.Name,
		tc.Arguments,
		t.opts.Channel,
		t.opts.ChatID,
		asyncCallback,
	)
	slot.result = toolResult
	tools.RecordToolResult(t.opts.SessionKey, tc.Name, toolResult)
	recorded = true
	recordToolCallAudit(ctx, t.agent, t.opts, tc, toolResult, time.Since(toolStart))
}

// asyncToolCallback is the completion callback of an AsyncExecutor tool. Its
// ForUser output goes straight to the user, as on the synchronous path; its
// result re-enters the agent loop as a "system" message so
// processSystemMessage routes it back through a normal turn. Where the result
// goes is fixed now, while the agent exists: a clone's late result goes to
// its source's main conversation, as a sub-agent's always has.
func (al *AgentLoop) asyncToolCallback(ctx context.Context, agent *AgentInstance, opts processOptions, toolName string) tools.AsyncCallback {
	resultAgentID, resultSessionKey := asyncResultTarget(agent)
	turnDepth := toolsagents.SpawnDepth(ctx)
	return func(cbCtx context.Context, result *tools.ToolResult) {
		// An asked turn has no chat: its user-facing output is dropped.
		if !result.Silent && result.ForUser != "" && opts.Channel != constants.AgentMessageChannel {
			al.deliverAsyncForUser(cbCtx, opts, toolName, result.ForUser)
		}

		content := result.ForLLM
		if content == "" && result.Err != nil {
			content = result.Err.Error()
		}
		if content == "" {
			return
		}
		content = capToolResult(content, result.IsError, agent.ContextWindow)

		logger.InfoCF("agent", "Async tool completed, publishing result",
			map[string]any{
				"tool":        toolName,
				"content_len": len(content),
				"channel":     opts.Channel,
			})

		pubCtx, pubCancel := context.WithTimeout(context.WithoutCancel(cbCtx), publishTimeout)
		defer pubCancel()
		if err := al.bus.PublishInbound(pubCtx, bus.InboundMessage{
			Channel:    "system",
			SenderID:   "async:" + toolName,
			Internal:   true,
			ChatID:     opts.ChatID,
			Content:    content,
			SessionKey: resultSessionKey,
			// The re-entered turn runs at this turn's depth, never lower.
			Metadata: bus.SetSpawnDepth(map[string]string{
				metadataKeyPreresolvedAgentID: resultAgentID, bus.MetaOriginChannel: opts.Channel,
			}, turnDepth),
		}); err != nil {
			logger.WarnCF("agent", "Failed to deliver async tool result to agent",
				map[string]any{"error": err.Error(), "tool": toolName, "session": opts.SessionKey})
		}
	}
}

// deliverAsyncForUser sends an async tool's ForUser output to the chat of
// the turn that started it.
func (al *AgentLoop) deliverAsyncForUser(cbCtx context.Context, opts processOptions, toolName, forUser string) {
	outCtx, outCancel := context.WithTimeout(context.WithoutCancel(cbCtx), publishTimeout)
	defer outCancel()
	logger.InfoCF("agent", "Async tool completed, delivering to user",
		map[string]any{
			"tool":        toolName,
			"channel":     opts.Channel,
			"chat_id":     opts.ChatID,
			"content_len": len(forUser),
		})
	if err := al.bus.PublishOutbound(outCtx, bus.OutboundMessage{
		Channel: opts.Channel,
		ChatID:  opts.ChatID,
		Content: forUser,
	}); err != nil {
		logger.WarnCF("agent", "Failed to deliver async tool result to user",
			map[string]any{"error": err.Error(), "tool": toolName, "channel": opts.Channel})
	}
}

// toolImages routes the images a batch of tools returned according to the
// active model's vision mode.
type toolImages struct {
	mode string
	// followUp is for VisionUserMessage: Chat Completions tool messages are
	// text-only, so the images go in one user message after the results.
	followUp []string
	// unseen and focus are for VisionOff: the images go to the vision
	// side-model instead of being dropped, steered by the image tools'
	// ForLLM text (which carries any file_view_image focus line).
	unseen []string
	focus  []string
}

// route attaches result's images to its tool message or sets them aside.
func (ti *toolImages) route(result *tools.ToolResult, msg *providers.Message) {
	switch ti.mode {
	case config.VisionToolResponse:
		// The Responses API takes images on the tool result itself.
		msg.Media = result.Images
	case config.VisionUserMessage:
		ti.followUp = append(ti.followUp, result.Images...)
	default:
		if len(result.Images) > 0 {
			ti.unseen = append(ti.unseen, result.Images...)
			if f := strings.TrimSpace(result.ForLLM); f != "" {
				ti.focus = append(ti.focus, f)
			}
		}
	}
}

// addToolResults delivers a batch's results in call order: user-facing
// output and media to the chat, and one tool message per call to the
// conversation, followed by any images the model is to see.
func (t *llmTurn) addToolResults(ctx context.Context, results []toolCallResult) error {
	streamActivity := t.al.GetConfig().Agents.Defaults.StreamToolActivity
	images := toolImages{mode: config.VisionOff}
	if mc, err := t.al.GetConfig().GetModelConfig(t.model); err == nil {
		images.mode = mc.Vision
	}
	for _, r := range results {
		// Each tool's own output reaches the user only with
		// stream_tool_activity; otherwise the user gets just the final answer.
		if streamActivity && !r.result.Silent && r.result.ForUser != "" {
			t.publishToolForUser(ctx, r)
		}
		if len(r.result.Media) > 0 {
			t.publishToolMedia(ctx, r)
		}
		msg := t.toolResultMessage(r)
		images.route(r.result, &msg)
		if err := t.keep(ctx, t.cm.AddToolResult, msg, "tool result"); err != nil {
			return err
		}
	}
	if err := t.addToolImages(ctx, images.followUp); err != nil {
		return err
	}
	return t.describeUnseenImages(ctx, images)
}

// publishToolForUser sends a tool's ForUser output to the chat.
func (t *llmTurn) publishToolForUser(ctx context.Context, r toolCallResult) {
	if err := t.al.bus.PublishOutbound(ctx, bus.OutboundMessage{
		Channel: t.opts.Channel,
		ChatID:  t.opts.ChatID,
		Content: r.result.ForUser,
	}); err != nil {
		logger.WarnCF("agent", "Failed to publish tool ForUser content",
			map[string]any{
				"tool":    r.tc.Name,
				"channel": t.opts.Channel,
				"error":   err.Error(),
			})
		return
	}
	logger.DebugCF("agent", "Sent tool result to user",
		map[string]any{
			"tool":        r.tc.Name,
			"content_len": len(r.result.ForUser),
		})
}

// publishToolMedia sends the media refs a tool returned to the chat.
func (t *llmTurn) publishToolMedia(ctx context.Context, r toolCallResult) {
	parts := make([]bus.MediaPart, 0, len(r.result.Media))
	for _, ref := range r.result.Media {
		part := bus.MediaPart{Ref: ref}
		if t.al.mediaStore != nil {
			if _, meta, err := t.al.mediaStore.ResolveWithMeta(ref); err == nil {
				part.Filename = meta.Filename
				part.ContentType = meta.ContentType
				part.Type = inferMediaType(meta.Filename, meta.ContentType)
			}
		}
		parts = append(parts, part)
	}
	if err := t.al.bus.PublishOutboundMedia(ctx, bus.OutboundMediaMessage{
		Channel: t.opts.Channel,
		ChatID:  t.opts.ChatID,
		Parts:   parts,
	}); err != nil {
		logger.WarnCF("agent", "Failed to publish tool media",
			map[string]any{
				"tool":    r.tc.Name,
				"channel": t.opts.Channel,
				"error":   err.Error(),
			})
	}
}

// toolResultMessage is the tool message for one result, its content capped
// to fit the context window.
func (t *llmTurn) toolResultMessage(r toolCallResult) providers.Message {
	contentForLLM := r.result.ForLLM
	if contentForLLM == "" && r.result.Err != nil {
		contentForLLM = r.result.Err.Error()
	}
	if capped := capToolResult(contentForLLM, r.result.IsError, t.agent.ContextWindow); len(capped) != len(contentForLLM) {
		logger.WarnCF("agent", "Tool result truncated for context", map[string]any{
			"agent_id":     t.agent.ID,
			"tool":         r.tc.Name,
			"original_len": len(contentForLLM),
			"kept_len":     len(capped),
		})
		contentForLLM = capped
	}
	msg := providers.Message{
		Role:       "tool",
		Content:    contentForLLM,
		ToolCallID: r.tc.ID,
	}
	// A failed write leaves the file unchanged, so the eviction sweep must
	// never treat it as superseding the read needed to correct it.
	if r.result.IsError {
		msg.Type = providers.MessageTypeToolError
	}
	return msg
}

// addToolImages hands the batch's images to a VisionUserMessage model as one
// follow-up user message.
func (t *llmTurn) addToolImages(ctx context.Context, images []string) error {
	if len(images) == 0 {
		return nil
	}
	imgMsg := providers.Message{
		Role:    "user",
		Content: "Image(s) returned by the tool call(s) above:",
		Media:   images,
	}
	if err := t.keep(ctx, t.cm.AddUserMessage, imgMsg, "tool image message"); err != nil {
		return err
	}
	logger.InfoCF("agent", "passed tool image(s) to vision model (user message)",
		map[string]any{"agent_id": t.agent.ID, "model": t.model, "images": len(images)})
	return nil
}

// describeUnseenImages gives a text-only model a description of the images
// its tools returned, from the vision side-model, as a follow-up user
// message. Without a vision side-model nothing is added: the model gets only
// the hidden-attachment note from messagesForModel.
func (t *llmTurn) describeUnseenImages(ctx context.Context, images toolImages) error {
	if images.mode != config.VisionOff || len(t.agent.VisionClients) == 0 || len(images.unseen) == 0 {
		return nil
	}
	focusParts := images.focus
	if lu := strings.TrimSpace(t.opts.UserMessage); lu != "" {
		focusParts = append(focusParts, "User's request: "+lu)
	}
	desc, ok := t.al.describeImages(ctx, t.agent, images.unseen, strings.Join(focusParts, "\n"))
	content := "An image was returned by a tool, but this model cannot view images and no description could be produced."
	if ok {
		content = "Image(s) described for you (this model cannot view images):\n" + desc
	}
	if err := t.keep(ctx, t.cm.AddUserMessage, providers.Message{Role: "user", Content: content}, "vision description message"); err != nil {
		return err
	}
	logger.InfoCF("agent", "injected vision description for non-vision model",
		map[string]any{"agent_id": t.agent.ID, "model": t.model, "images": len(images.unseen), "described": ok})
	return nil
}
