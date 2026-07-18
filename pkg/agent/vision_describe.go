// ClawEh
// License: MIT

package agent

import (
	"context"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// resolveVisionModelChain returns the ordered, de-duplicated vision-describe
// model chain: the agent-defaults VisionModel first, then VisionModelFallbacks.
// Blank entries are skipped; the first occurrence of each name wins. There is no
// global-models merge — vision is purely the agent-defaults side-model. Reuses
// resolveCompressModelChain's dedup semantics.
func resolveVisionModelChain(model string, fallbacks []string) []string {
	return resolveCompressModelChain([]string{model}, fallbacks)
}

// buildVisionLLMClient resolves a configured vision model reference into an
// LLMClient dispatched through the per-model dispatcher, mirroring
// buildCompressLLMClient. Unlike compression there is NO agent-primary
// last-resort fallback: the whole point is that the primary is text-only, so an
// unresolvable vision model is skipped (returns ok=false) rather than routed
// through the non-vision provider. cfg is passed explicitly because this runs
// during registerRuntimeTools, before al.cfg is swapped on reload.
func (al *AgentLoop) buildVisionLLMClient(cfg *config.Config, agent *AgentInstance, visionModelName string) (llmcontext.LLMClient, bool) {
	alias, modelID, ok := resolveCompressModelTarget(cfg, visionModelName)
	if !ok || al.dispatcher == nil {
		logger.WarnCF("agent", "vision model not found in enabled models; skipping", map[string]any{
			"agent_id":  agent.ID,
			"requested": visionModelName,
		})
		return nil, false
	}
	p, err := al.dispatcher.Get(alias)
	if err != nil {
		logger.WarnCF("agent", "vision model dispatch failed; skipping", map[string]any{
			"agent_id":  agent.ID,
			"requested": visionModelName,
			"alias":     alias,
			"model":     modelID,
			"error":     err.Error(),
		})
		return nil, false
	}
	logger.DebugCF("agent", "vision model resolved", map[string]any{
		"agent_id":  agent.ID,
		"requested": visionModelName,
		"alias":     alias,
		"model":     modelID,
	})
	// requestJSONObject stays false: a describe call wants free-form prose, not a
	// JSON object.
	return &providerLLMClient{provider: p, model: modelID, providerName: compressProviderName(cfg, alias)}, true
}

// wireVisionClients builds the agent's vision-describe chain from the global
// AgentDefaults.VisionModel + VisionModelFallbacks and stores it on the
// instance. Called once per agent from registerRuntimeTools (initial construction
// and config reload). No configured vision model leaves VisionClients empty,
// which keeps the feature off (images are dropped, as before).
func (al *AgentLoop) wireVisionClients(cfg *config.Config, agent *AgentInstance) {
	names := resolveVisionModelChain(cfg.Agents.Defaults.VisionModel, cfg.Agents.Defaults.VisionModelFallbacks)
	var clients []llmcontext.LLMClient
	effective := ""
	for _, name := range names {
		client, ok := al.buildVisionLLMClient(cfg, agent, name)
		if !ok {
			continue
		}
		if effective == "" {
			effective = name
		}
		clients = append(clients, client)
	}
	agent.VisionClients = clients
	agent.EffectiveVisionModel = effective
	if len(clients) > 0 {
		logger.InfoCF("agent", "vision describe model chain", map[string]any{
			"agent_id": agent.ID,
			"chain":    names,
		})
	}
}

// modelHasVision reports whether the named model (by alias / model_name) is
// vision-capable. Unknown models and lookup errors are treated as text-only,
// matching messagesForModel's default.
func (al *AgentLoop) modelHasVision(modelKey string) bool {
	mc, err := al.GetConfig().GetModelConfig(modelKey)
	if err != nil || mc == nil {
		return false
	}
	return mc.Vision == config.VisionUserMessage || mc.Vision == config.VisionToolResponse
}

// visionDescribeSystemPrompt instructs the side-model to produce a description a
// text-only model can consume.
const visionDescribeSystemPrompt = "You are a vision assistant for a text-only model. " +
	"Describe the following image(s) accurately and concisely. " +
	"If a focus is given, prioritize it."

// describeImages dispatches the given image data URIs to the agent's vision
// side-model chain for a one-shot text description. focus (optional) steers what
// to describe. Returns (description, true) on the first client that yields
// non-empty text; (\"\", false) when no vision model is configured or every
// client fails — the caller then degrades gracefully. A describe failure never
// breaks the turn.
func (al *AgentLoop) describeImages(ctx context.Context, agent *AgentInstance, images []string, focus string) (string, bool) {
	if agent == nil || len(agent.VisionClients) == 0 || len(images) == 0 {
		return "", false
	}

	userContent := "Describe the image(s)."
	if f := strings.TrimSpace(focus); f != "" {
		userContent = f
	}
	msgs := []providers.Message{
		{Role: "system", Content: visionDescribeSystemPrompt},
		{Role: "user", Content: userContent, Media: images},
	}

	for _, client := range agent.VisionClients {
		model := ""
		if m, ok := client.(interface{ Model() string }); ok {
			model = m.Model()
		}
		logger.InfoCF("agent", "vision describe: dispatching", map[string]any{
			"agent_id": agent.ID,
			"model":    model,
			"images":   len(images),
		})
		reply, err := client.Complete(ctx, msgs)
		if err != nil {
			logger.WarnCF("agent", "vision describe failed; trying next", map[string]any{
				"agent_id": agent.ID,
				"model":    model,
				"error":    err.Error(),
			})
			continue
		}
		desc := strings.TrimSpace(reply.Content)
		if desc == "" {
			logger.WarnCF("agent", "vision describe returned empty; trying next", map[string]any{
				"agent_id": agent.ID,
				"model":    model,
			})
			continue
		}
		logger.InfoCF("agent", "vision describe: ok", map[string]any{
			"agent_id": agent.ID,
			"model":    model,
			"chars":    len(desc),
		})
		return desc, true
	}
	return "", false
}

// describeInboundMedia implements Flow B: when the agent's primary model is
// text-only and a vision side-model is configured, an inbound image on the user
// message is described ONCE at ingest — the description is folded into the
// message text and the image Media is cleared, so the stored message (and every
// dispatch iteration) carries usable text instead of an image the model cannot
// see. Vision-capable primaries are left untouched (the normal path handles the
// image). No-op when vision is not configured or there is no image to describe.
func (al *AgentLoop) describeInboundMedia(ctx context.Context, agent *AgentInstance, userMsg *providers.Message) {
	if agent == nil || len(agent.VisionClients) == 0 || len(userMsg.Media) == 0 {
		return
	}
	if al.modelHasVision(agent.Model) {
		return
	}
	dataURIs := al.resolveImageDataURIs(userMsg.Media)
	if len(dataURIs) == 0 {
		return
	}
	desc, ok := al.describeImages(ctx, agent, dataURIs, "")
	if !ok {
		return
	}
	base := strings.TrimSpace(userMsg.Content)
	if base != "" {
		base += "\n\n"
	}
	userMsg.Content = base + "[Image(s) the user shared, described for you: " + desc + "]"
	userMsg.Media = nil
	logger.InfoCF("agent", "described inbound image(s) for non-vision model", map[string]any{
		"agent_id": agent.ID,
		"model":    agent.Model,
		"images":   len(dataURIs),
	})
}
