// ClawEh - Personal AI Assistant
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
	toolsmsg "github.com/PivotLLM/ClawEh/tools/msg"
)

// registerRuntimeTools is the single tool-registration entry point: for every
// agent in the registry it builds the full ToolDeps (session closures, the
// sub-agent spawner, the shared message tool, dispatcher/fallback) and registers
// every allowed provider tool exactly once. It runs after the AgentLoop exists so
// the closures can capture al — at initial construction (NewAgentLoop) and again
// on config reload (ReloadProviderAndConfig). NewAgentInstance deliberately
// leaves the registry empty so tools are never double-registered.
func (al *AgentLoop) registerRuntimeTools(
	registry *AgentRegistry,
	provider providers.LLMProvider,
	dispatcher *providers.ProviderDispatcher,
	fallbackChain *providers.FallbackChain,
	cfg *config.Config,
) {
	// cfg is passed explicitly (not al.GetConfig()) because on reload
	// ReloadProviderAndConfig registers tools BEFORE swapping al.cfg, so
	// al.GetConfig() would return the stale pre-reload config here.

	// Build shared message tool for all agents.
	var sharedMessageTool tools.Tool
	if cfg.Tools.IsToolEnabled("msg_send") {
		mt := toolsmsg.NewMessageTool()
		mt.SetSendCallback(func(channel, chatID, content string) error {
			pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer pubCancel()
			return al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
				Channel: channel,
				ChatID:  chatID,
				Content: content,
			})
		})
		sharedMessageTool = mt
	}

	// Collect the per-agent spawn managers built below so the task supervisor can
	// scan/relaunch interrupted tasks against the current config.
	managers := make(map[string]*toolsagents.SubagentManager)

	for _, agentID := range registry.ListAgentIDs() {
		agentInst, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}
		currentAgent := agentInst
		agentCfg := currentAgent.Config

		// Wire the vision-describe side-model chain onto the instance (no-op when
		// no vision model is configured). cfg is passed explicitly because on
		// reload al.cfg is still the pre-reload config at this point.
		al.wireVisionClients(cfg, currentAgent)

		// Build candidate resolver for spawn.
		candidateResolver := func(targetAgentID string) ([]providers.FallbackCandidate, bool) {
			target, ok := registry.GetAgent(targetAgentID)
			if !ok {
				return nil, false
			}
			if len(target.Candidates) == 0 {
				return nil, false
			}
			return target.Candidates, true
		}

		// Build compact closure. Returns the compaction report and the resulting
		// rendered summary alongside the error.
		compactFn := func(ctx context.Context, sessionKey string) (string, string, error) {
			ctx = providers.WithAgentID(ctx, currentAgent.ID)
			cm, release := al.getContextManager(currentAgent, sessionKey)
			defer release()
			err := cm.Compact(ctx)
			report := ""
			if r := cm.LastCompactionReport(); r != nil {
				report = r.String()
			}
			return report, cm.RenderedSummary(), err
		}

		// Build clear closure for session_clear: rate-limited; publishes a
		// reset-tagged self-handoff inbound that resets the session and restarts
		// the turn at a clean boundary (never wipes history mid-turn).
		clearFn := func(ctx context.Context, sessionKey, message string) error {
			if !al.allowSelfClear(sessionKey) {
				return fmt.Errorf("session_clear is rate-limited; wait a few seconds before clearing again")
			}
			inbound := bus.InboundMessage{
				Channel:    tools.ToolChannel(ctx),
				ChatID:     tools.ToolChatID(ctx),
				SenderID:   "system",
				SessionKey: sessionKey,
				Content:    wrapClearNotice(message),
				Metadata:   map[string]string{metaSessionReset: "true"},
			}
			if inbound.ChatID != "" && inbound.ChatID != "direct" {
				inbound.Peer = bus.Peer{Kind: "channel", ID: inbound.ChatID}
			}
			pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return al.bus.PublishInbound(pubCtx, inbound)
		}

		// Build session info closure.
		infoFn := func(ctx context.Context, sessionKey string) (*tools.SessionInfo, error) {
			return buildSessionInfo(al, currentAgent, sessionKey)
		}

		// Determine spawn allowlist.
		currentAgentID := agentID
		spawnAllowlist := func(callerID, targetID string) bool {
			return registry.CanSpawnSubagent(currentAgentID, targetID)
		}

		// Robust sub-agent launcher, injected via Deps.Spawn so the internal spawn
		// tool and any external/MCP tool launch workers through the same path.
		spawnMgr := toolsagents.NewSubagentManager(toolsagents.SubagentManagerConfig{
			Provider:          provider,
			Workspace:         currentAgent.Workspace,
			Live:              al.taskLive,
			Dispatcher:        dispatcher,
			Fallback:          fallbackChain,
			SelfCandidates:    currentAgent.Candidates,
			CallerAgentID:     agentID,
			CandidateResolver: candidateResolver,
			RunFull:           al.runSubagentTask,
		})
		managers[agentID] = spawnMgr
		spawner := toolsagents.NewSpawner(spawnMgr)
		spawner.SetMaxDepth(cfg.Agents.Defaults.GetMaxSubagentDepth())
		spawner.SetAllowlistChecker(func(targetID string) bool {
			return spawnAllowlist(currentAgentID, targetID)
		})

		deps := tools.ToolDeps{
			Cfg:               cfg,
			AgentCfg:          agentCfg,
			AgentID:           agentID,
			Workspace:         currentAgent.Workspace,
			Provider:          provider,
			Dispatcher:        dispatcher,
			Fallback:          fallbackChain,
			Candidates:        currentAgent.Candidates,
			SpawnAllowlist:    spawnAllowlist,
			CandidateResolver: candidateResolver,
			Spawn:             spawner,
			CompactFn:         compactFn,
			SessionInfoFn:     infoFn,
			ClearFn:           clearFn,
			MessageTool:       sharedMessageTool,
		}

		// Progressive discovery is a single global switch (default off). When on,
		// native tools and the cogmem suite stay always-on; the fusion/maestro suites
		// (and MCP tools, in loop_mcp) are hidden behind search_tools.
		discovery := cfg.Tools.Discovery.Enabled
		currentAgent.DiscoveryActive = discovery
		currentAgent.AlwaysShownNamespaces = cfg.AlwaysShownNamespaces()
		if currentAgent.ContextBuilder != nil {
			currentAgent.ContextBuilder.WithToolDiscovery(discovery)
		}

		for _, p := range tools.GetProviders() {
			if ok, _ := p.Available(cfg); !ok {
				continue
			}
			suite := ""
			if sp, ok := p.(tools.SuiteProvider); ok {
				suite = sp.Suite()
			}
			for _, t := range p.Build(deps) {
				switch {
				case suite == "":
					// Native: always visible, subject to the per-tool allowlist.
					if agentCfg == nil || agentCfg.IsToolAllowed(t.Name()) {
						currentAgent.Tools.Register(t)
					}
				case suite == suiteCogmem:
					currentAgent.Tools.RegisterSuite(t) // cogmem: always-on, never hidden
				case discoveryHidesTool(discovery, currentAgent.AlwaysShownNamespaces, t.Name()):
					// fusion/maestro behind discovery; carry any reveal-together group
					// (e.g. a fusion service) so the whole set unlocks in one search.
					// A namespace pinned via always_shown_namespaces skips this and
					// falls through to RegisterSuite (always visible to the model).
					if g, ok := t.(tools.DiscoveryGrouped); ok {
						group, revealTogether := g.DiscoveryGroup()
						currentAgent.Tools.RegisterSuiteHiddenGroup(t, group, revealTogether)
					} else {
						currentAgent.Tools.RegisterSuiteHidden(t)
					}
				default:
					// Non-discovery suites, plus discovery-pinned always-shown suites.
					currentAgent.Tools.RegisterSuite(t)
				}
			}
		}

		if discovery {
			al.registerDiscoveryMetaTools(currentAgent, cfg)
		}
	}

	al.spawnMu.Lock()
	al.spawnManagers = managers
	al.spawnMu.Unlock()
}

// suiteCogmem is the one suite that is never subject to progressive discovery —
// cognitive memory is fundamental to how the agent works, so it stays always-on.
const suiteCogmem = "cogmem"

// discoveryHidesTool reports whether a discovery-eligible tool (a fusion/maestro
// suite tool or an upstream MCP tool) should be hidden from the in-loop model: it
// is hidden only when discovery is active AND the tool's namespace is not pinned
// via always_shown_namespaces. A pinned namespace keeps the tool always visible.
func discoveryHidesTool(active bool, alwaysShown []string, toolName string) bool {
	return active && !config.MatchVisibility(alwaysShown, toolName)
}

// registerDiscoveryMetaTools registers the search_tools / get_tool_details entry
// points. They are suite-exempt (gated by the discovery decision, not the per-tool
// allowlist) and present only when discovery is active for the agent.
func (al *AgentLoop) registerDiscoveryMetaTools(agent *AgentInstance, cfg *config.Config) {
	ttlMax := cfg.DiscoveryTTLMax()
	visibleBudget := cfg.DiscoveryVisibleBudget()
	maxHits := cfg.Tools.Discovery.MaxSearchResults
	if maxHits <= 0 {
		maxHits = config.DefaultDiscoveryMaxSearchHits
	}
	agent.Tools.RegisterSuite(tools.NewSearchTool(agent.Tools, maxHits))
	agent.Tools.RegisterSuite(tools.NewToolDetailsTool(agent.Tools, ttlMax, visibleBudget))
}

func (al *AgentLoop) RegisterTool(tool tools.Tool) {
	registry := al.GetRegistry()
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}
		// Per-agent allowlist check: skip registration if the agent config
		// explicitly denies this tool. Config is always non-nil after construction.
		if !agent.Config.IsToolAllowed(tool.Name()) {
			logger.DebugCF("agent", "Skipping tool registration: not allowed by agent config",
				map[string]any{
					"agent_id": agentID,
					"tool":     tool.Name(),
				})
			continue
		}
		agent.Tools.Register(tool)
	}
}

// stopTyping clears the typing indicator for a channel/chatID when a turn ends
// without sending a reply (the send path stops typing on its own). No-op when
// the channel manager is unset or the target is empty.
func (al *AgentLoop) stopTyping(channel, chatID string) {
	if al.channelManager != nil && channel != "" && chatID != "" {
		al.channelManager.StopTyping(channel, chatID)
	}
}

// startProgressUpdates edits the turn's "Thinking…" placeholder every interval
// so a long-running turn (many tool calls, slow model) never looks dead. The
// returned function stops the updater and must be deferred. It is a no-op (and
// returns a no-op stopper) for non-user turns or when progress is disabled.
// completed is the live count of finished tool calls, shared with the LLM loop.
func (al *AgentLoop) startProgressUpdates(channel, chatID string, interval time.Duration, completed *atomic.Int64) func() {
	if interval <= 0 || al.channelManager == nil || channel == "" || channel == "system" || chatID == "" {
		return func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				n := completed.Load()
				var text string
				if n == 0 {
					text = "⏳ Still working…"
				} else {
					text = fmt.Sprintf("⏳ Still working… %d tool call(s) completed so far.", n)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				al.channelManager.UpdatePlaceholder(ctx, channel, chatID, text)
				cancel()
			}
		}
	}()
	// Block until the updater has fully exited so no placeholder edit can land
	// after the turn's final reply (which consumes the placeholder).
	return func() {
		close(stop)
		<-done
	}
}

// superviseTasks periodically relaunches interrupted callback tasks across every
// agent's workspace. It exits on ctx cancellation or Close().
func (al *AgentLoop) superviseTasks(ctx context.Context) {
	ticker := time.NewTicker(global.TaskSupervisorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-al.superStop:
			return
		case <-ticker.C:
			al.runTaskSupervision()
		}
	}
}

// runTaskSupervision runs one supervision pass over all current spawn managers.
func (al *AgentLoop) runTaskSupervision() {
	al.spawnMu.Lock()
	managers := make([]*toolsagents.SubagentManager, 0, len(al.spawnManagers))
	for _, m := range al.spawnManagers {
		managers = append(managers, m)
	}
	al.spawnMu.Unlock()

	now := time.Now().Unix()
	for _, m := range managers {
		m.SuperviseOnce(now, func(rec *toolsagents.TaskRecord) tools.AsyncCallback {
			return al.taskPointerCallback(rec.Channel, rec.ChatID)
		})
	}
}

// taskPointerCallback builds the completion callback for a relaunched task: it
// publishes the compact completion pointer to the task's origin channel (the
// agent reads the referenced result file). Mirrors the inline async-tool callback
// used for the initial in-turn spawn.
func (al *AgentLoop) taskPointerCallback(channel, chatID string) tools.AsyncCallback {
	return func(_ context.Context, result *tools.ToolResult) {
		if result == nil {
			return
		}
		if !result.Silent && result.ForUser != "" {
			outCtx, outCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = al.bus.PublishOutbound(outCtx, bus.OutboundMessage{
				Channel: channel,
				ChatID:  chatID,
				Content: result.ForUser,
			})
			outCancel()
		}
		content := result.ForLLM
		if content == "" && result.Err != nil {
			content = result.Err.Error()
		}
		if content == "" {
			return
		}
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = al.bus.PublishInbound(pubCtx, bus.InboundMessage{
			Channel:  "system",
			SenderID: "async:agent_spawn",
			ChatID:   fmt.Sprintf("%s:%s", channel, chatID),
			Content:  content,
		})
		pubCancel()
	}
}

// Helper to extract provider from registry for cleanup
func extractProvider(registry *AgentRegistry) (providers.LLMProvider, bool) {
	if registry == nil {
		return nil, false
	}
	// Get any agent to access the provider
	defaultAgent := registry.GetDefaultAgent()
	if defaultAgent == nil {
		return nil, false
	}
	return defaultAgent.Provider, true
}
