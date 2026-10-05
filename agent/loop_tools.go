// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
	toolsmsg "github.com/PivotLLM/ClawEh/tools/msg"
)

// agentBuilder returns the registry's BuildFunc: it builds the instance a spec
// describes and registers its tools, so every agent — config agents at start
// and on reload, temporary agents when they are created or rebuilt — gets its
// tools exactly once, from one place. cfg is the one being built against (on
// reload, al.cfg is still the old one at this point). A fresh temporary agent
// gets no tools at all.
func (al *AgentLoop) agentBuilder(
	provider providers.LLMProvider,
	dispatcher *providers.ProviderDispatcher,
	fallbackChain *providers.FallbackChain,
) agentreg.BuildFunc[*AgentInstance] {
	return func(cfg *config.Config, spec agentreg.Spec) (*AgentInstance, error) {
		inst, err := newAgentInstance(spec, &cfg.Agents.Defaults, cfg, provider)
		if err != nil {
			return nil, err
		}
		if inst.HumanModel != "" {
			// A human agent runs no model: no vision side-model, no tools.
			return inst, nil
		}
		// Wire the vision-describe side-model chain onto the instance (no-op when
		// no vision model is configured).
		al.wireVisionClients(cfg, inst)
		if spec.Fresh {
			return inst, nil
		}
		al.registerAgentTools(inst, provider, dispatcher, fallbackChain, cfg)
		if inst.IsTemp() {
			// A config agent gets these after the registry is built (the gateway
			// registers its tools, the media store is set at start); an agent
			// created later gets them now. Its MCP tools come once it is
			// registered (agentInserted).
			al.registerExtraTools(inst)
			al.applyMediaStore(inst)
		}
		return inst, nil
	}
}

// agentInserted is the registry's InsertedFunc: a temporary agent Create has
// just made visible gets the current MCP tool set. Done after insertion and
// under the MCP refresh lock, so a server that is replaced while the agent is
// being created is either seen here or re-registered onto it by the refresh.
func (al *AgentLoop) agentInserted(_ agentreg.Spec, inst *AgentInstance) {
	if inst.toolless() {
		return
	}
	al.mcp.refreshMu.Lock()
	defer al.mcp.refreshMu.Unlock()
	inst.Tools.RemoveByPrefix(tools.MCPToolPrefix)
	al.registerMCPToolsOn(inst)
}

// registerAgentTools builds the full ToolDeps for one agent (session closures,
// the sub-agent spawner, the message tool, dispatcher/fallback) and registers
// every allowed provider tool on it. A clone's tools act as its source (see
// toolIdentity) but are bound to the clone's own state directory and session.
func (al *AgentLoop) registerAgentTools(
	currentAgent *AgentInstance,
	provider providers.LLMProvider,
	dispatcher *providers.ProviderDispatcher,
	fallbackChain *providers.FallbackChain,
	cfg *config.Config,
) {
	{
		agentID := currentAgent.toolIdentity()
		agentCfg := currentAgent.Config

		// Build message tool for this agent instance.
		var messageTool tools.Tool
		if cfg.Tools.IsToolEnabled("msg_send") {
			mt := toolsmsg.NewMessageTool()
			mt.SetSendCallback(func(ctx context.Context, channel, chatID, content string) error {
				pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
				defer pubCancel()
				return al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
					Channel: channel,
					ChatID:  chatID,
					Content: content,
				})
			})
			messageTool = mt
		}

		// Build candidate resolver for spawn. The registry is looked up at call
		// time: this runs while the registry is still being built.
		candidateResolver := func(targetAgentID string) ([]providers.FallbackCandidate, bool) {
			target, ok := al.GetRegistry().Get(targetAgentID)
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
			cm, release := al.getContextManager(currentAgent, sessionKey) //nolint:contextcheck // compaction reporter: ctxengine's callback has no context, so it publishes on its own
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
				return errors.New("session_clear is rate-limited; wait a few seconds before clearing again")
			}
			meta := map[string]string{
				metaSessionReset:              "true",
				metadataKeyPreresolvedAgentID: currentAgent.ID,
			}
			inbound := bus.InboundMessage{
				Channel:    tools.ToolChannel(ctx),
				ChatID:     tools.ToolChatID(ctx),
				SenderID:   "system",
				SessionKey: sessionKey,
				Content:    wrapClearNotice(message),
				Metadata:   meta,
			}
			if inbound.ChatID != "" && inbound.ChatID != "direct" {
				inbound.Peer = bus.Peer{Kind: "channel", ID: inbound.ChatID}
			}
			pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return al.bus.PublishInbound(pubCtx, inbound)
		}

		// Build session info closure.
		infoFn := func(ctx context.Context, sessionKey string) (*tools.SessionInfo, error) {
			return buildSessionInfo(al, currentAgent, sessionKey) //nolint:contextcheck // compaction reporter: ctxengine's callback has no context, so it publishes on its own
		}

		// Determine spawn allowlist.
		currentAgentID := currentAgent.ID
		spawnAllowlist := func(callerID, targetID string) bool {
			return canSpawnSubagent(al.GetRegistry(), currentAgentID, targetID)
		}

		// Robust sub-agent launcher, injected via Deps.Spawn so the internal spawn
		// tool and any external/MCP tool launch workers through the same path.
		// A clone's tasks are its source's: same tasks directory, same owner, so a
		// background task it starts reports to the source's main conversation,
		// like every other late result of a clone (see asyncResultTarget).
		spawnMgr := toolsagents.NewSubagentManager(toolsagents.SubagentManagerConfig{
			Workspace:         currentAgent.Workspace,
			Live:              al.taskLive,
			SelfCandidates:    currentAgent.Candidates,
			CallerAgentID:     agentID,
			CandidateResolver: candidateResolver,
			RunFull:           al.runSubagentTask,
			Alerter:           al.Alerter(),
		})
		currentAgent.spawnMgr = spawnMgr
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
			StateDir:          currentAgent.StateDir,
			EphemeralMemory:   currentAgent.Spec.Ephemeral,
			TempAgent:         currentAgent.IsTemp(),
			Provider:          provider,
			Dispatcher:        dispatcher,
			Fallback:          fallbackChain,
			Candidates:        currentAgent.Candidates,
			SpawnAllowlist:    spawnAllowlist,
			Agents:            newAgentServices(al, currentAgentID),
			CandidateResolver: candidateResolver,
			Spawn:             spawner,
			CompactFn:         compactFn,
			SessionInfoFn:     infoFn,
			ClearFn:           clearFn,
			MessageTool:       messageTool,
		}

		// Progressive discovery is a single global switch (default off). When on,
		// native tools and the cogmem suite stay always-on; the fusion/maestro suites
		// (and MCP tools, in loop_mcp) are hidden behind search_tools.
		discovery := cfg.Tools.Discovery.Enabled
		currentAgent.DiscoveryActive = discovery
		currentAgent.AlwaysShownNamespaces = discoveryPins(cfg, agentCfg)
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
				case agentCfg.IsToolDenied(t.Name()):
					// Suite tools skip the allow list (gated as a unit by their
					// toggle) but never the agent's deny list. Mirrors the
					// execution-time check in tools.ToolRegistry.ExecuteWithContext.
					logger.DebugCF("agent", "Skipping suite tool registration: denied by agent deny_tools",
						map[string]any{"agent_id": agentID, "tool": t.Name(), "suite": suite})
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
}

// retireAgent is the registry's RetireFunc: before a temporary agent is
// closed and its directory removed, its one session's context manager is
// closed (refused while something holds it), its session tokens revoked and
// the per-session caches the loop keeps for it dropped.
func (al *AgentLoop) retireAgent(spec agentreg.Spec, inst *AgentInstance) error {
	sessionKey := routing.BuildAgentMainSessionKey(inst.ID)
	if !al.dropContextManager(context.Background(), inst, sessionKey, evictReasonTempDeleted) {
		return fmt.Errorf("session %s is still in use", sessionKey)
	}
	al.releaseSessionPins(sessionKey)
	al.mu.RLock()
	sti := al.sessionTokenIssuer
	al.mu.RUnlock()
	if sti != nil {
		sti.RevokeAgent(inst.ID)
	}
	al.forgetSessionCaches(inst, sessionKey)
	return nil
}

// registerExtraTools registers on agent the tools added with RegisterTool
// (the gateway's cron tool), honouring its allowlist.
func (al *AgentLoop) registerExtraTools(agent *AgentInstance) {
	if agent.toolless() {
		return
	}
	al.extraToolsMu.Lock()
	extra := slices.Clone(al.extraTools)
	al.extraToolsMu.Unlock()
	for _, t := range extra {
		if agent.Config.IsToolAllowed(t.Name()) {
			agent.Tools.Register(t)
		}
	}
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

// discoveryPins returns the visibility pins for an agent's in-loop tool list:
// the configured always_shown_namespaces plus, when the agent has Maestro and
// discovery is on, Maestro's entry-point tool, so the model can always reach the
// orientation guide and search for the rest. MatchVisibility matches by prefix,
// so a full tool name pins exactly that tool. This affects only the in-loop
// model; the MCP host always serves the full tool list.
func discoveryPins(cfg *config.Config, agentCfg *config.AgentConfig) []string {
	pins := append([]string(nil), cfg.AlwaysShownNamespaces()...)
	if cfg.Tools.Discovery.Enabled && agentCfg.MaestroEnabled() {
		pins = append(pins, global.MaestroEntryTool)
	}
	return pins
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
	// Suite-exempt from the allow list, but the agent's deny list still applies.
	for _, t := range []tools.Tool{
		tools.NewSearchTool(agent.Tools, maxHits),
		tools.NewToolDetailsTool(agent.Tools, ttlMax, visibleBudget),
	} {
		if agent.Config.IsToolDenied(t.Name()) {
			continue
		}
		agent.Tools.RegisterSuite(t)
	}
}

// RegisterTool registers a host-built tool (the gateway's cron tool) on every
// agent that allows it, and remembers it for the temporary agents created
// later. A fresh temporary agent gets no tools.
func (al *AgentLoop) RegisterTool(tool tools.Tool) {
	al.extraToolsMu.Lock()
	al.extraTools = slices.DeleteFunc(al.extraTools, func(t tools.Tool) bool { return t.Name() == tool.Name() })
	al.extraTools = append(al.extraTools, tool)
	al.extraToolsMu.Unlock()

	registry := al.GetRegistry()
	for _, agentID := range registry.All() {
		agent, ok := registry.Get(agentID)
		if !ok || agent.toolless() {
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
func (al *AgentLoop) startProgressUpdates(
	ctx context.Context, channel, chatID string, interval time.Duration, completed *atomic.Int64,
) func() {
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
				editCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				al.channelManager.UpdatePlaceholder(editCtx, channel, chatID, text)
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
			al.runTaskSupervision() //nolint:contextcheck // relaunched callback tasks run detached from the supervisor, like fresh spawns
		}
	}
}

// runTaskSupervision runs one supervision pass over the config agents' spawn
// managers. A clone shares its source's tasks directory, so the source's
// manager covers the tasks a clone started.
func (al *AgentLoop) runTaskSupervision() {
	registry := al.GetRegistry()
	var managers []*toolsagents.SubagentManager
	for _, id := range registry.List() {
		if a, ok := registry.Get(id); ok && a.spawnMgr != nil {
			managers = append(managers, a.spawnMgr)
		}
	}

	now := time.Now().Unix()
	for _, m := range managers {
		m.SuperviseOnce(now, func(rec *toolsagents.TaskRecord) tools.AsyncCallback {
			return al.taskPointerCallback(rec.Channel, rec.ChatID, rec.OwnerAgentID, rec.SpawnDepth)
		})
	}
}

// taskPointerCallback builds the completion callback for a relaunched task: it
// publishes the compact completion pointer to the task's origin channel (the
// agent reads the referenced result file). Mirrors the inline async-tool callback
// used for the initial in-turn spawn. spawnDepth is the spawning turn's depth,
// carried on the re-injected message so the re-entered turn is not lower.
func (al *AgentLoop) taskPointerCallback(channel, chatID, ownerAgentID string, spawnDepth int) tools.AsyncCallback {
	return func(cbCtx context.Context, result *tools.ToolResult) {
		if result == nil {
			return
		}
		if !result.Silent && result.ForUser != "" {
			outCtx, outCancel := context.WithTimeout(context.WithoutCancel(cbCtx), 5*time.Second)
			if err := al.bus.PublishOutbound(outCtx, bus.OutboundMessage{
				Channel: channel,
				ChatID:  chatID,
				Content: result.ForUser,
			}); err != nil {
				logger.WarnCF("agent", "Failed to publish async task result to user", map[string]any{
					"channel": channel,
					"error":   err.Error(),
				})
			}
			outCancel()
		}
		content := result.ForLLM
		if content == "" && result.Err != nil {
			content = result.Err.Error()
		}
		if content == "" {
			return
		}
		pubCtx, pubCancel := context.WithTimeout(context.WithoutCancel(cbCtx), 5*time.Second)
		msg := bus.InboundMessage{
			Channel:  "system",
			SenderID: "async:agent_spawn",
			ChatID:   fmt.Sprintf("%s:%s", channel, chatID),
			Content:  content,
		}
		if ownerAgentID != "" {
			msg.Metadata = map[string]string{metadataKeyPreresolvedAgentID: ownerAgentID}
			msg.SessionKey = routing.BuildAgentMainSessionKey(ownerAgentID)
		}
		msg.Metadata = bus.SetSpawnDepth(msg.Metadata, spawnDepth)
		if err := al.bus.PublishInbound(pubCtx, msg); err != nil {
			logger.WarnCF("agent", "Failed to publish async task result to agent", map[string]any{
				"channel": channel,
				"error":   err.Error(),
			})
		}
		pubCancel()
	}
}

// Helper to extract provider from registry for cleanup
func extractProvider(registry *AgentRegistry) (providers.LLMProvider, bool) {
	if registry == nil {
		return nil, false
	}
	// Get any agent to access the provider
	defaultAgent := registry.Default()
	if defaultAgent == nil {
		return nil, false
	}
	return defaultAgent.Provider, true
}
