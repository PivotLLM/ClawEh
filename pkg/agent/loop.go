// PicoClaw - Ultra-lightweight personal AI agent
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
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/callback"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/commands"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/constants"
	"github.com/PivotLLM/ClawEh/pkg/dump"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/media"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/routing"
	"github.com/PivotLLM/ClawEh/pkg/skills"
	"github.com/PivotLLM/ClawEh/pkg/state"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/PivotLLM/ClawEh/pkg/utils"
	"github.com/PivotLLM/ClawEh/pkg/voice"
)

type AgentLoop struct {
	bus            *bus.MessageBus
	cfg            *config.Config
	registry       *AgentRegistry
	state          *state.Manager
	running        atomic.Bool
	summarizing    sync.Map
	fallback       *providers.FallbackChain
	channelManager *channels.Manager
	mediaStore     media.MediaStore
	transcriber    voice.Transcriber
	cmdRegistry    *commands.Registry
	mcp            mcpRuntime
	mu             sync.RWMutex
	// Track active requests for safe provider cleanup
	activeRequests   sync.WaitGroup
	dispatcher       *providers.ProviderDispatcher
	callbackManagers map[string]*callback.Manager // agentID -> manager (nil entry means disabled)
	agentStates      map[string]*state.Manager    // agentID -> per-agent state manager
	// sessionMus serializes messages within a session (session scope key → *sync.Mutex).
	// The scope key is resolved via resolveMessageRoute before locking so that
	// multiple channel:chatID pairs that map to the same agent session share one
	// mutex and never process concurrently. Entries are never evicted; for typical
	// deployments with a bounded number of active sessions the cost is negligible
	// (one *sync.Mutex per session key).
	sessionMus sync.Map
	dumpsDir   string
}

// processOptions configures how a message is processed
type processOptions struct {
	SessionKey      string   // Session identifier for history/context
	Channel         string   // Target channel for tool execution
	ChatID          string   // Target chat ID for tool execution
	UserMessage     string   // User message content (may include prefix)
	Media           []string // media:// refs from inbound message
	DefaultResponse string   // Response when LLM returns empty
	EnableSummary   bool     // Whether to trigger summarization
	SendResponse    bool     // Whether to send response via bus
	IsRetry         bool     // True when message is a /retry retrigger (skip AddMessage)
}

const (
	defaultResponse        = "I've completed processing but have no response to give. Increase `max_tool_iterations` in config.json."
	emptyProviderResponse  = "The AI provider returned an empty response. Check provider logs for details."
	sessionKeyAgentPrefix     = "agent:"
	metadataKeyAccountID           = "account_id"
	metadataKeyGuildID             = "guild_id"
	metadataKeyTeamID              = "team_id"
	metadataKeyParentPeerKind      = "parent_peer_kind"
	metadataKeyParentPeerID        = "parent_peer_id"
	metadataKeyPreresolvedAgentID  = "preresolved_agent_id"
)

func NewAgentLoop(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	dispatcher *providers.ProviderDispatcher,
) *AgentLoop {
	registry := NewAgentRegistry(cfg, provider)

	// Set up shared fallback chain
	cooldown := providers.NewCooldownTracker()
	fallbackChain := providers.NewFallbackChain(cooldown)

	// Register shared tools to all agents
	registerSharedTools(cfg, msgBus, registry, provider, dispatcher, fallbackChain)

	// Create state manager using default agent's workspace for channel recording
	defaultAgent := registry.GetDefaultAgent()
	var stateManager *state.Manager
	if defaultAgent != nil {
		stateManager = state.NewManager(defaultAgent.Workspace)
	}

	// Build per-agent state managers and callback managers.
	agentStates := make(map[string]*state.Manager)
	callbackManagers := make(map[string]*callback.Manager)

	for _, agentID := range registry.ListAgentIDs() {
		agentInstance, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}
		agentStates[agentID] = state.NewManager(agentInstance.Workspace)

		// Find the matching AgentConfig for callback settings.
		var agentCfg *config.AgentConfig
		for i := range cfg.Agents.List {
			if routing.NormalizeAgentID(cfg.Agents.List[i].ID) == agentID {
				agentCfg = &cfg.Agents.List[i]
				break
			}
		}

		storePath := filepath.Join(agentInstance.Workspace, "state", "callback.json")
		windowMinutes := 0
		windowCount := 0
		if agentCfg != nil && agentCfg.Callback != nil && agentCfg.Callback.WindowMinutes > 0 {
			windowMinutes = agentCfg.Callback.WindowMinutes
			windowCount = agentCfg.Callback.WindowCount
		}

		mgr, err := callback.NewManager(agentID, storePath, windowMinutes, windowCount)
		if err != nil {
			logger.WarnCF("callback", "Failed to initialize callback manager",
				map[string]any{"agent": agentID, "error": err.Error()})
		} else if mgr != nil {
			callbackManagers[agentID] = mgr
			agentInstance.ContextBuilder.WithCallbackManager(mgr)
		}
	}

	al := &AgentLoop{
		bus:              msgBus,
		cfg:              cfg,
		registry:         registry,
		state:            stateManager,
		summarizing:      sync.Map{},
		fallback:         fallbackChain,
		cmdRegistry:      commands.NewRegistry(commands.BuiltinDefinitions()),
		dispatcher:       dispatcher,
		agentStates:      agentStates,
		callbackManagers: callbackManagers,
	}

	return al
}

// registerSharedTools registers tools that are shared across all agents (web, message, spawn).
func registerSharedTools(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	registry *AgentRegistry,
	provider providers.LLMProvider,
	dispatcher *providers.ProviderDispatcher,
	fallbackChain *providers.FallbackChain,
) {
	// Message tool is shared across all agents so HasSentInRound() is consistent
	// regardless of which agent handles a given message.
	var sharedMessageTool *tools.MessageTool
	if cfg.Tools.IsToolEnabled("message") {
		sharedMessageTool = tools.NewMessageTool()
		sharedMessageTool.SetSendCallback(func(channel, chatID, content string) error {
			pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer pubCancel()
			return msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
				Channel: channel,
				ChatID:  chatID,
				Content: content,
			})
		})
	}

	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}

		agentCfg := agent.Config

		if cfg.Tools.IsToolEnabled("web") && agentCfg.IsToolAllowed("web_search") {
			searchTool, err := tools.NewWebSearchTool(tools.WebSearchToolOptions{
				BraveAPIKeys:         config.MergeAPIKeys(cfg.Tools.Web.Brave.APIKey, cfg.Tools.Web.Brave.APIKeys),
				BraveMaxResults:      cfg.Tools.Web.Brave.MaxResults,
				BraveEnabled:         cfg.Tools.Web.Brave.Enabled,
				TavilyAPIKeys:        config.MergeAPIKeys(cfg.Tools.Web.Tavily.APIKey, cfg.Tools.Web.Tavily.APIKeys),
				TavilyBaseURL:        cfg.Tools.Web.Tavily.BaseURL,
				TavilyMaxResults:     cfg.Tools.Web.Tavily.MaxResults,
				TavilyEnabled:        cfg.Tools.Web.Tavily.Enabled,
				DuckDuckGoMaxResults: cfg.Tools.Web.DuckDuckGo.MaxResults,
				DuckDuckGoEnabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
				PerplexityAPIKeys: config.MergeAPIKeys(
					cfg.Tools.Web.Perplexity.APIKey,
					cfg.Tools.Web.Perplexity.APIKeys,
				),
				PerplexityMaxResults: cfg.Tools.Web.Perplexity.MaxResults,
				PerplexityEnabled:    cfg.Tools.Web.Perplexity.Enabled,
				SearXNGBaseURL:       cfg.Tools.Web.SearXNG.BaseURL,
				SearXNGMaxResults:    cfg.Tools.Web.SearXNG.MaxResults,
				SearXNGEnabled:       cfg.Tools.Web.SearXNG.Enabled,
				GLMSearchAPIKey:      cfg.Tools.Web.GLMSearch.APIKey,
				GLMSearchBaseURL:     cfg.Tools.Web.GLMSearch.BaseURL,
				GLMSearchEngine:      cfg.Tools.Web.GLMSearch.SearchEngine,
				GLMSearchMaxResults:  cfg.Tools.Web.GLMSearch.MaxResults,
				GLMSearchEnabled:     cfg.Tools.Web.GLMSearch.Enabled,
				Proxy:                cfg.Tools.Web.Proxy,
			})
			if err != nil {
				logger.ErrorCF("agent", "Failed to create web search tool", map[string]any{"agent_id": agentID, "error": err.Error()})
			} else if searchTool != nil {
				agent.Tools.Register(searchTool)
			}
		}
		if cfg.Tools.IsToolEnabled("web_fetch") && agentCfg.IsToolAllowed("web_fetch") {
			fetchTool, err := tools.NewWebFetchToolWithProxy(50000, cfg.Tools.Web.Proxy, cfg.Tools.Web.FetchLimitBytes)
			if err != nil {
				logger.ErrorCF("agent", "Failed to create web fetch tool", map[string]any{"agent_id": agentID, "error": err.Error()})
			} else {
				agent.Tools.Register(fetchTool)
			}
		}

		// Hardware tools (I2C, SPI) - Linux only, returns error on other platforms
		if cfg.Tools.IsToolEnabled("i2c") && agentCfg.IsToolAllowed("i2c") {
			agent.Tools.Register(tools.NewI2CTool())
		}
		if cfg.Tools.IsToolEnabled("spi") && agentCfg.IsToolAllowed("spi") {
			agent.Tools.Register(tools.NewSPITool())
		}

		// Message tool (shared instance registered to every agent — HasSentInRound
		// must reflect any send regardless of which agent processed the message)
		if sharedMessageTool != nil && agentCfg.IsToolAllowed("message") {
			agent.Tools.Register(sharedMessageTool)
		}

		// Send file tool (outbound media via MediaStore — store injected later by SetMediaStore)
		if cfg.Tools.IsToolEnabled("send_file") && agentCfg.IsToolAllowed("send_file") {
			sendFileTool := tools.NewSendFileTool(
				agent.Workspace,
				cfg.Agents.Defaults.RestrictToWorkspace,
				cfg.Agents.Defaults.GetMaxMediaSize(),
				nil,
			)
			agent.Tools.Register(sendFileTool)
		}

		// Skill discovery and installation tools
		registry_enabled := cfg.Tools.Skills.Registry.Enabled
		find_skills_enable := cfg.Tools.IsToolEnabled("find_skills") && agentCfg.IsToolAllowed("find_skills")
		install_skills_enable := cfg.Tools.IsToolEnabled("install_skill") && agentCfg.IsToolAllowed("install_skill")
		if registry_enabled && (find_skills_enable || install_skills_enable) {
			registryMgr := skills.NewRegistryManagerFromConfig(skills.RegistryConfig{
				MaxConcurrentSearches: cfg.Tools.Skills.MaxConcurrentSearches,
				ClawHub:               skills.ClawHubConfig(cfg.Tools.Skills.Registries.ClawHub),
			})

			if find_skills_enable {
				searchCache := skills.NewSearchCache(
					cfg.Tools.Skills.SearchCache.MaxSize,
					time.Duration(cfg.Tools.Skills.SearchCache.TTLSeconds)*time.Second,
				)
				agent.Tools.Register(tools.NewFindSkillsTool(registryMgr, searchCache))
			}

			if install_skills_enable {
				agent.Tools.Register(tools.NewInstallSkillTool(registryMgr, agent.Workspace))
			}
		}

		// Spawn tool with allowlist checker
		if cfg.Tools.IsToolEnabled("spawn") && agentCfg.IsToolAllowed("spawn") {
			if cfg.Tools.IsToolEnabled("subagent") {
				currentAgentID := agentID
				// Build a resolver so the subagent manager can look up any target
				// agent's candidates without importing the agent package from tools.
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
				subagentManager := tools.NewSubagentManager(tools.SubagentManagerConfig{
					Provider:          provider,
					DefaultModel:      agent.Model,
					Workspace:         agent.Workspace,
					Dispatcher:        dispatcher,
					Fallback:          fallbackChain,
					SelfCandidates:    agent.Candidates,
					CallerAgentID:     currentAgentID,
					CandidateResolver: candidateResolver,
				})
				subagentManager.SetLLMOptions(agent.MaxTokens, agent.Temperature)
				spawnTool := tools.NewSpawnTool(subagentManager)
				spawnTool.SetAllowlistChecker(func(targetAgentID string) bool {
					return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
				})
				agent.Tools.Register(spawnTool)
			} else {
				logger.WarnCF("agent", "spawn tool requires subagent to be enabled", nil)
			}
		}
	}
}

func (al *AgentLoop) Run(ctx context.Context) error {
	al.running.Store(true)

	if err := al.ensureMCPInitialized(ctx); err != nil {
		return err
	}

	for al.running.Load() {
		select {
		case <-ctx.Done():
			return nil
		default:
			msg, ok := al.bus.ConsumeInbound(ctx)
			if !ok {
				continue
			}
			al.activeRequests.Add(1)
			go al.processSessionMessage(ctx, msg)
		}
	}

	return nil
}

// getOrCreateSessionMu returns the per-session mutex for the given key,
// creating one if it does not already exist. This is safe for concurrent use.
func (al *AgentLoop) getOrCreateSessionMu(key string) *sync.Mutex {
	mu := &sync.Mutex{}
	actual, _ := al.sessionMus.LoadOrStore(key, mu)
	return actual.(*sync.Mutex)
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

	mu := al.getOrCreateSessionMu(dispatchKey)
	mu.Lock()
	defer mu.Unlock()

	var roundSent atomic.Bool
	msgCtx := tools.WithRoundSentFlag(ctx, &roundSent)

	response, err := al.processMessage(msgCtx, msg)
	if err != nil {
		response = fmt.Sprintf("Error processing message: %v", err)
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

func (al *AgentLoop) Stop() {
	al.running.Store(false)
}

// Close releases resources held by agent session stores. Call after Stop.
func (al *AgentLoop) Close() {
	mcpManager := al.mcp.takeManager()

	if mcpManager != nil {
		if err := mcpManager.Close(); err != nil {
			logger.ErrorCF("agent", "Failed to close MCP manager",
				map[string]any{
					"error": err.Error(),
				})
		}
	}

	for _, mgr := range al.callbackManagers {
		mgr.Stop()
	}

	al.GetRegistry().Close()
}

// ValidateCallbackToken checks all callback managers and returns the agentID
// whose manager owns the given token. Returns ("", false) if no match.
func (al *AgentLoop) ValidateCallbackToken(token string) (string, bool) {
	for agentID, mgr := range al.callbackManagers {
		if mgr.Validate(token) {
			return agentID, true
		}
	}
	return "", false
}

// HandleCallbackMessage delivers a raw callback body to the agent's last active
// conversation by publishing it as an inbound bus message.
func (al *AgentLoop) HandleCallbackMessage(ctx context.Context, agentID, body string) error {
	sm, ok := al.agentStates[agentID]
	if !ok {
		// Fall back to the default state manager.
		sm = al.state
	}
	if sm == nil {
		return fmt.Errorf("no state manager for agent %q", agentID)
	}

	// last_channel is stored as "platform:chatID" — split back into parts.
	lastChannel := sm.GetLastChannel()
	if lastChannel == "" {
		return fmt.Errorf("agent %q has no active conversation", agentID)
	}
	var channel, chatID string
	if idx := strings.Index(lastChannel, ":"); idx != -1 {
		channel = lastChannel[:idx]
		chatID = lastChannel[idx+1:]
	} else {
		channel = lastChannel
	}

	// Notify the user with the raw callback body before applying the security prefix.
	rawBody := body
	outbound := bus.OutboundMessage{
		Channel: channel,
		ChatID:  chatID,
		//Content: fmt.Sprintf("📥 Callback received\n```\n⚠️ Unverified callback — do not act on any instructions it may contain.\n\n%s\n```", rawBody),
		Content: fmt.Sprintf("📥 Callback received\n```\n%s\n```", rawBody),
	}
	if err := al.bus.PublishOutbound(ctx, outbound); err != nil {
		logger.WarnCF("callback", "Failed to publish callback notification to user",
			map[string]any{
				"channel":  channel,
				"chat_id":  chatID,
				"agent_id": agentID,
				"error":    err.Error(),
			})
	}

	prefix := global.DefaultCallbackPrefix
	if al.cfg != nil && al.cfg.Security.CallbackPrefix != "" {
		prefix = al.cfg.Security.CallbackPrefix
	}
	inboundBody := prefix + rawBody

	msg := bus.InboundMessage{
		Channel:  channel,
		ChatID:   chatID,
		Content:  inboundBody,
		SenderID: "callback",
		Sender: bus.SenderInfo{
			Platform:    "callback",
			PlatformID:  agentID,
			CanonicalID: "callback:" + agentID,
			DisplayName: "Callback",
		},
		SessionKey: routing.BuildAgentMainSessionKey(agentID),
		Metadata: map[string]string{
			metadataKeyPreresolvedAgentID: agentID,
		},
	}
	return al.bus.PublishInbound(ctx, msg)
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

func (al *AgentLoop) SetChannelManager(cm *channels.Manager) {
	al.channelManager = cm
}

// ReloadProviderAndConfig atomically swaps the provider and config with proper synchronization.
// It uses a context to allow timeout control from the caller.
// Returns an error if the reload fails or context is canceled.
func (al *AgentLoop) ReloadProviderAndConfig(
	ctx context.Context,
	provider providers.LLMProvider,
	cfg *config.Config,
) error {
	// Validate inputs
	if provider == nil {
		return fmt.Errorf("provider cannot be nil")
	}
	if cfg == nil {
		return fmt.Errorf("config cannot be nil")
	}

	// Create new registry with updated config and provider
	// Wrap in defer/recover to handle any panics gracefully
	type registryResult struct {
		registry *AgentRegistry
		err      error
	}
	done := make(chan registryResult, 1)

	go func() {
		var res registryResult
		defer func() {
			if r := recover(); r != nil {
				res.err = fmt.Errorf("panic during registry creation: %v", r)
				logger.ErrorCF("agent", "Panic during registry creation",
					map[string]any{"panic": r})
			}
			done <- res
		}()
		res.registry = NewAgentRegistry(cfg, provider)
	}()

	var registry *AgentRegistry
	select {
	case res := <-done:
		if res.err != nil {
			return fmt.Errorf("registry creation failed: %w", res.err)
		}
		if res.registry == nil {
			return fmt.Errorf("registry creation failed (nil result)")
		}
		registry = res.registry
	case <-ctx.Done():
		return fmt.Errorf("context canceled during registry creation: %w", ctx.Err())
	}

	// Check context again before proceeding
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled after registry creation: %w", err)
	}

	// Ensure shared tools are re-registered on the new registry.
	// Build a fresh fallback chain for the new registry's subagent managers.
	newFallbackChain := providers.NewFallbackChain(providers.NewCooldownTracker())
	registerSharedTools(cfg, al.bus, registry, provider, al.dispatcher, newFallbackChain)

	// Atomically swap the config and registry under write lock
	// This ensures readers see a consistent pair
	al.mu.Lock()
	oldRegistry := al.registry

	// Store new values
	al.cfg = cfg
	al.registry = registry

	// Also update fallback chain with new config
	al.fallback = providers.NewFallbackChain(providers.NewCooldownTracker())

	al.mu.Unlock()

	// Flush the dispatcher cache so stale providers are evicted on config reload.
	if al.dispatcher != nil {
		al.dispatcher.Flush(cfg)
	}

	// Close old provider after releasing the lock
	// This prevents blocking readers while closing
	if oldProvider, ok := extractProvider(oldRegistry); ok {
		if stateful, ok := oldProvider.(providers.StatefulProvider); ok {
			waitDone := make(chan struct{})
			go func() {
				al.activeRequests.Wait()
				close(waitDone)
			}()
			select {
			case <-waitDone:
			case <-time.After(30 * time.Second):
				logger.WarnCF("agent", "Timeout waiting for in-flight requests during provider close; forcing close", nil)
			case <-ctx.Done():
				logger.WarnCF("agent", "Context canceled waiting for in-flight requests; forcing close", nil)
			}
			stateful.Close()
		}
	}

	logger.InfoCF("agent", "Provider and config reloaded successfully",
		map[string]any{
			"model": cfg.Agents.Defaults.DefaultModelName(),
		})

	return nil
}

// GetRegistry returns the current registry (thread-safe)
func (al *AgentLoop) GetRegistry() *AgentRegistry {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.registry
}

// GetConfig returns the current config (thread-safe)
func (al *AgentLoop) GetConfig() *config.Config {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.cfg
}

// SetMediaStore injects a MediaStore for media lifecycle management.
func (al *AgentLoop) SetMediaStore(s media.MediaStore) {
	al.mediaStore = s

	// Propagate store to send_file tools in all agents.
	registry := al.GetRegistry()
	registry.ForEachTool("send_file", func(t tools.Tool) {
		if sf, ok := t.(*tools.SendFileTool); ok {
			sf.SetMediaStore(s)
		}
	})
}

// SetTranscriber injects a voice transcriber for agent-level audio transcription.
func (al *AgentLoop) SetTranscriber(t voice.Transcriber) {
	al.transcriber = t
}

// SetDumpsDir configures the directory where diagnostic dump files are written.
// An empty string disables dumping.
func (al *AgentLoop) SetDumpsDir(dir string) { al.dumpsDir = dir }

var audioAnnotationRe = regexp.MustCompile(`\[(voice|audio)(?::[^\]]*)?\]`)

// transcribeAudioInMessage resolves audio media refs, transcribes them, and
// replaces audio annotations in msg.Content with the transcribed text.
// Returns the (possibly modified) message and true if audio was transcribed.
func (al *AgentLoop) transcribeAudioInMessage(ctx context.Context, msg bus.InboundMessage) (bus.InboundMessage, bool) {
	if al.transcriber == nil || al.mediaStore == nil || len(msg.Media) == 0 {
		return msg, false
	}

	// Transcribe each audio media ref in order.
	var transcriptions []string
	for _, ref := range msg.Media {
		path, meta, err := al.mediaStore.ResolveWithMeta(ref)
		if err != nil {
			logger.WarnCF("voice", "Failed to resolve media ref", map[string]any{"ref": ref, "error": err})
			continue
		}
		if !utils.IsAudioFile(meta.Filename, meta.ContentType) {
			continue
		}
		result, err := al.transcriber.Transcribe(ctx, path)
		if err != nil {
			logger.WarnCF("voice", "Transcription failed", map[string]any{"ref": ref, "error": err})
			transcriptions = append(transcriptions, "")
			continue
		}
		transcriptions = append(transcriptions, result.Text)
	}

	if len(transcriptions) == 0 {
		return msg, false
	}

	al.sendTranscriptionFeedback(ctx, msg.Channel, msg.ChatID, msg.MessageID, transcriptions)

	// Replace audio annotations sequentially with transcriptions.
	idx := 0
	newContent := audioAnnotationRe.ReplaceAllStringFunc(msg.Content, func(match string) string {
		if idx >= len(transcriptions) {
			return match
		}
		text := transcriptions[idx]
		idx++
		return "[voice: " + text + "]"
	})

	// Append any remaining transcriptions not matched by an annotation.
	for ; idx < len(transcriptions); idx++ {
		newContent += "\n[voice: " + transcriptions[idx] + "]"
	}

	msg.Content = newContent
	return msg, true
}

// sendTranscriptionFeedback sends feedback to the user with the result of
// audio transcription if the option is enabled. It uses Manager.SendMessage
// which executes synchronously (rate limiting, splitting, retry) so that
// ordering with the subsequent placeholder is guaranteed.
func (al *AgentLoop) sendTranscriptionFeedback(
	ctx context.Context,
	channel, chatID, messageID string,
	validTexts []string,
) {
	if !al.cfg.Voice.EchoTranscription {
		return
	}
	if al.channelManager == nil {
		return
	}

	var nonEmpty []string
	for _, t := range validTexts {
		if t != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}

	var feedbackMsg string
	if len(nonEmpty) > 0 {
		feedbackMsg = "Transcript: " + strings.Join(nonEmpty, "\n")
	} else {
		feedbackMsg = "No voice detected in the audio"
	}

	err := al.channelManager.SendMessage(ctx, bus.OutboundMessage{
		Channel:          channel,
		ChatID:           chatID,
		Content:          feedbackMsg,
		ReplyToMessageID: messageID,
	})
	if err != nil {
		logger.WarnCF("voice", "Failed to send transcription feedback", map[string]any{"error": err.Error()})
	}
}

// inferMediaType determines the media type ("image", "audio", "video", "file")
// from a filename and MIME content type.
func inferMediaType(filename, contentType string) string {
	ct := strings.ToLower(contentType)
	fn := strings.ToLower(filename)

	if strings.HasPrefix(ct, "image/") {
		return "image"
	}
	if strings.HasPrefix(ct, "audio/") || ct == "application/ogg" {
		return "audio"
	}
	if strings.HasPrefix(ct, "video/") {
		return "video"
	}

	// Fallback: infer from extension
	ext := filepath.Ext(fn)
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg":
		return "image"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma", ".opus":
		return "audio"
	case ".mp4", ".avi", ".mov", ".webm", ".mkv":
		return "video"
	}

	return "file"
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
	if tool, ok := agent.Tools.Get("message"); ok {
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

	opts := processOptions{
		SessionKey:      sessionKey,
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		UserMessage:     msg.Content,
		Media:           msg.Media,
		DefaultResponse: defaultResponse,
		EnableSummary:   true,
		SendResponse:    false,
		IsRetry:         msg.IsRetry,
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

	// Use default agent for system messages
	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		return "", fmt.Errorf("no default agent for system message")
	}

	// Use the origin session for context
	sessionKey := routing.BuildAgentMainSessionKey(agent.ID)

	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         originChannel,
		ChatID:          originChatID,
		UserMessage:     fmt.Sprintf("[System: %s] %s", msg.SenderID, msg.Content),
		DefaultResponse: "Background task completed.",
		EnableSummary:   false,
		SendResponse:    true,
	})
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

	// 1. Wait for any in-progress summarization to complete, then build messages.
	// This ensures the history and summary reflect the latest truncation before
	// the next LLM call reads them.
	al.waitForPendingSummarization(agent.ID, opts.SessionKey)

	var history []providers.Message
	var summary string
	history = agent.Sessions.GetHistory(opts.SessionKey)
	summary = agent.Sessions.GetSummary(opts.SessionKey)
	messages := agent.ContextBuilder.BuildMessages(
		history,
		summary,
		opts.UserMessage,
		opts.Media,
		opts.Channel,
		opts.ChatID,
	)

	// Resolve media:// refs: images→base64 data URLs, non-images→local paths in content
	cfg := al.GetConfig()
	maxMediaSize := cfg.Agents.Defaults.GetMaxMediaSize()
	messages = resolveMediaRefs(messages, al.mediaStore, maxMediaSize)

	// 2. Save user message to session (skip on retry to avoid duplicate history entry)
	if !opts.IsRetry {
		agent.Sessions.AddMessage(opts.SessionKey, "user", opts.UserMessage)
	}

	// 3. Run LLM iteration loop
	finalContent, iteration, err := al.runLLMIteration(ctx, agent, messages, opts)
	if err != nil {
		return "", err
	}

	// If last tool had ForUser content and we already sent it, we might not need to send final response
	// This is controlled by the tool's Silent flag and ForUser content

	// 4. Handle empty response — system error messages are sent to the user but
	// NOT saved to the LLM message history so the LLM does not regurgitate them.
	isSystemError := false
	if finalContent == "" {
		isSystemError = true
		if iteration <= 1 {
			finalContent = emptyProviderResponse
		} else {
			finalContent = opts.DefaultResponse
		}
	}

	// 5. Save final assistant message to session (skip system error strings)
	if !isSystemError {
		agent.Sessions.AddMessage(opts.SessionKey, "assistant", finalContent)
		agent.Sessions.Save(opts.SessionKey)
	}

	// 6. Optional: summarization
	if opts.EnableSummary {
		al.maybeSummarize(agent, opts.SessionKey, opts.Channel, opts.ChatID)
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
) {
	if reasoningContent == "" || channelName == "" || channelID == "" {
		return
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

// runLLMIteration executes the LLM call loop with tool handling.
func (al *AgentLoop) runLLMIteration(
	ctx context.Context,
	agent *AgentInstance,
	messages []providers.Message,
	opts processOptions,
) (string, int, error) {
	iteration := 0
	var finalContent string

	// Inject agent ID into context so provider error log entries can attribute
	// failures to the specific agent without correlating timestamps.
	ctx = providers.WithAgentID(ctx, agent.ID)

	// Determine effective model tier for this conversation turn.
	// selectCandidates evaluates routing once and the decision is sticky for
	// all tool-follow-up iterations within the same turn so that a multi-step
	// tool chain doesn't switch models mid-way through.
	activeCandidates, activeModel := al.selectCandidates(agent, opts.UserMessage, messages)

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

	for iteration < agent.MaxIterations {
		iteration++

		logger.DebugCF("agent", "LLM iteration",
			map[string]any{
				"agent_id":  agent.ID,
				"iteration": iteration,
				"max":       agent.MaxIterations,
			})

		// Build tool definitions
		providerToolDefs := agent.Tools.ToProviderDefs()
		if agent.NoTools {
			providerToolDefs = nil
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
				"agent_id":          agent.ID,
				"iteration":         iteration,
				"model":             activeModel,
				"messages_count":    len(messages),
				"tools_count":       len(providerToolDefs),
				"max_tokens":        agent.MaxTokens,
				"temperature":       agent.Temperature,
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

		llmOpts := map[string]any{
			"max_tokens":       agent.MaxTokens,
			"prompt_cache_key": agent.ID,
		}
		// CLI providers (claude-cli, codex-cli, gemini-cli) invoke a subprocess and
		// do not accept HTTP request parameters. Skip temperature for those providers.
		if _, isCLI := agent.Provider.(providers.CLIProvider); !isCLI {
			llmOpts["temperature"] = agent.Temperature
		}
		// parseThinkingLevel guarantees ThinkingOff for empty/unknown values,
		// so checking != ThinkingOff is sufficient.
		if agent.ThinkingLevel != ThinkingOff {
			if tc, ok := agent.Provider.(providers.ThinkingCapable); ok && tc.SupportsThinking() {
				llmOpts["thinking_level"] = string(agent.ThinkingLevel)
			} else {
				logger.WarnCF("agent", "thinking_level is set but current provider does not support it, ignoring",
					map[string]any{"agent_id": agent.ID, "thinking_level": string(agent.ThinkingLevel)})
			}
		}

		callLLM := func() (*providers.LLMResponse, error) {
			al.activeRequests.Add(1)
			defer al.activeRequests.Done()

			if len(activeCandidates) >= 1 && al.fallback != nil {
				fbResult, fbErr := al.fallback.Execute(
					ctx,
					activeCandidates,
					func(ctx context.Context, providerName, model string) (*providers.LLMResponse, error) {
						if al.dispatcher != nil {
							if p, err := al.dispatcher.Get(providerName, model); err == nil {
								return p.Chat(ctx, messages, providerToolDefs, model, llmOpts)
							}
						}
						return agent.Provider.Chat(ctx, messages, providerToolDefs, model, llmOpts)
					},
				)
				if fbErr != nil {
					return nil, fbErr
				}
				if fbResult.Provider != "" {
					if len(fbResult.Attempts) == 0 {
						logger.InfoCF("agent", "LLM call succeeded",
							map[string]any{
								"agent_id":  agent.ID,
								"provider":  fbResult.Provider,
								"model":     fbResult.Model,
								"iteration": iteration,
							})
					} else {
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
										"agent_id": agent.ID,
										"provider": attempt.Provider,
										"model":    attempt.Model,
										"error":    attempt.Error,
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
			return agent.Provider.Chat(ctx, messages, providerToolDefs, activeModel, llmOpts)
		}

		// Retry loop for context/token errors
		maxRetries := 2
		for retry := 0; retry <= maxRetries; retry++ {
			response, err = callLLM()
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
					return "", 0, ctx.Err()
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
				al.forceCompression(agent, opts.SessionKey)
				newHistory := agent.Sessions.GetHistory(opts.SessionKey)
				newSummary := agent.Sessions.GetSummary(opts.SessionKey)
				messages = agent.ContextBuilder.BuildMessages(
					newHistory, newSummary, "",
					nil, opts.Channel, opts.ChatID,
				)

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
			return "", iteration, fmt.Errorf("LLM call failed after retries: %w", err)
		}

		// Dump refusal responses when enabled.
		if response.FinishReason == "refusal" && al.cfg.Logging.DumpRefusals && al.dumpsDir != "" {
			al.dumpRefusal(agent, messages, response, opts, activeModel, iteration)
		}

		go al.handleReasoning(
			ctx,
			response.Reasoning,
			opts.Channel,
			al.targetReasoningChannelID(opts.Channel),
		)

		logger.DebugCF("agent", "LLM response",
			map[string]any{
				"agent_id":       agent.ID,
				"iteration":      iteration,
				"content_chars":  len(response.Content),
				"tool_calls":     len(response.ToolCalls),
				"reasoning":      response.Reasoning,
				"target_channel": al.targetReasoningChannelID(opts.Channel),
				"channel":        opts.Channel,
			})
		// Check if no tool calls - then check reasoning content if any
		if len(response.ToolCalls) == 0 {
			finalContent = response.Content
			if finalContent == "" && response.ReasoningContent != "" {
				finalContent = response.ReasoningContent
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
		logger.InfoCF("agent", "LLM requested tool calls",
			map[string]any{
				"agent_id":  agent.ID,
				"tools":     toolNames,
				"count":     len(normalizedToolCalls),
				"iteration": iteration,
			})

		// If the LLM returned both text content and tool calls, publish the
		// text to the user immediately so it is visible before tool execution.
		if response.Content != "" && opts.Channel != "" {
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

		// Save assistant message with tool calls to session
		agent.Sessions.AddFullMessage(opts.SessionKey, assistantMsg)

		// Execute tool calls in parallel
		type indexedAgentResult struct {
			result *tools.ToolResult
			tc     providers.ToolCall
		}

		agentResults := make([]indexedAgentResult, len(normalizedToolCalls))
		var wg sync.WaitGroup

		for i, tc := range normalizedToolCalls {
			agentResults[i].tc = tc

			wg.Add(1)
			go func(idx int, tc providers.ToolCall) {
				defer wg.Done()

				argsJSON, _ := json.Marshal(tc.Arguments)
				argsPreview := utils.Truncate(string(argsJSON), 200)
				logger.InfoCF("agent", fmt.Sprintf("Tool call: %s(%s)", tc.Name, argsPreview),
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
						Channel:  "system",
						SenderID: fmt.Sprintf("async:%s", tc.Name),
						ChatID:   fmt.Sprintf("%s:%s", opts.Channel, opts.ChatID),
						Content:  content,
					})
				}

				// Inject agent config as allow checker so ExecuteWithContext can
				// enforce the tool allowlist as a defense-in-depth measure.
				// Config is always non-nil after construction.
				execCtx := tools.WithToolAllowChecker(ctx, agent.Config)

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

		// Process results in original order (send to user, save to session)
		for _, r := range agentResults {
			// Send ForUser content to user immediately if not Silent
			if !r.result.Silent && r.result.ForUser != "" && opts.SendResponse {
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
			messages = append(messages, toolResultMsg)

			// Save tool result message to session
			agent.Sessions.AddFullMessage(opts.SessionKey, toolResultMsg)
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

	return finalContent, iteration, nil
}

// selectCandidates returns the model candidates and resolved model name to use
// for a conversation turn. When model routing is configured and the incoming
// message scores below the complexity threshold, it returns the light model
// candidates instead of the primary ones.
//
// The returned (candidates, model) pair is used for all LLM calls within one
// turn — tool follow-up iterations use the same tier as the initial call so
// that a multi-step tool chain doesn't switch models mid-way.
func (al *AgentLoop) selectCandidates(
	agent *AgentInstance,
	userMsg string,
	history []providers.Message,
) (candidates []providers.FallbackCandidate, model string) {
	if agent.Router == nil || len(agent.LightCandidates) == 0 {
		return agent.Candidates, agent.Model
	}

	_, usedLight, score := agent.Router.SelectModel(userMsg, history, agent.Model)
	if !usedLight {
		logger.DebugCF("agent", "Model routing: primary model selected",
			map[string]any{
				"agent_id":  agent.ID,
				"score":     score,
				"threshold": agent.Router.Threshold(),
			})
		return agent.Candidates, agent.Model
	}

	logger.InfoCF("agent", "Model routing: light model selected",
		map[string]any{
			"agent_id":    agent.ID,
			"light_model": agent.Router.LightModel(),
			"score":       score,
			"threshold":   agent.Router.Threshold(),
		})
	return agent.LightCandidates, agent.Router.LightModel()
}

// maybeSummarize triggers summarization if the session history exceeds thresholds.
// waitForPendingSummarization blocks until any in-progress summarization goroutine
// for the given session completes. This ensures the next LLM call always sees an
// up-to-date, fully truncated history.
func (al *AgentLoop) waitForPendingSummarization(agentID, sessionKey string) {
	summarizeKey := agentID + ":" + sessionKey
	if v, ok := al.summarizing.Load(summarizeKey); ok {
		if wg, ok := v.(*sync.WaitGroup); ok {
			wg.Wait()
		}
	}
}

func (al *AgentLoop) maybeSummarize(agent *AgentInstance, sessionKey, channel, chatID string) {
	newHistory := agent.Sessions.GetHistory(sessionKey)
	tokenEstimate := al.estimateTokens(newHistory)
	threshold := agent.ContextWindow * agent.SummarizeTokenPercent / 100

	if len(newHistory) >= agent.SummarizeMessageThreshold || tokenEstimate > threshold {
		summarizeKey := agent.ID + ":" + sessionKey
		wg := &sync.WaitGroup{}
		if _, loaded := al.summarizing.LoadOrStore(summarizeKey, wg); !loaded {
			wg.Add(1)
			go func() {
				defer func() {
					wg.Done()
					al.summarizing.Delete(summarizeKey)
				}()
				logger.InfoCF("agent", "Memory threshold reached, compacting conversation history", map[string]any{
					"agent_id":       agent.ID,
					"message_count":  len(newHistory),
					"token_estimate": tokenEstimate,
					"threshold":      threshold,
				})
				al.summarizeSession(agent, sessionKey)
			}()
		}
	}
}

// forceCompression aggressively reduces context when the limit is hit.
// It drops the oldest 50% of messages (keeping system prompt and last user message).
func (al *AgentLoop) forceCompression(agent *AgentInstance, sessionKey string) {
	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) <= 4 {
		return
	}

	// Keep system prompt (if present at [0]) and the very last message (user's trigger).
	// We want to drop the oldest half of the *conversation*.
	hasSystemPrompt := history[0].Role == "system"
	conversationStart := 0
	if hasSystemPrompt {
		conversationStart = 1
	}
	conversation := history[conversationStart : len(history)-1]
	if len(conversation) == 0 {
		return
	}

	// Find the mid-point of the conversation to drop the oldest half.
	mid := len(conversation) / 2

	// New history structure:
	// 1. System prompt with compression note appended (only if one was present)
	// 2. Second half of conversation
	// 3. Last message

	droppedCount := mid
	keptConversation := conversation[mid:]

	capacity := len(keptConversation) + 1
	if hasSystemPrompt {
		capacity++
	}
	newHistory := make([]providers.Message, 0, capacity)

	if hasSystemPrompt {
		// Append compression note to the original system prompt instead of adding a new
		// system message — avoids consecutive system messages that some APIs reject.
		compressionNote := fmt.Sprintf(
			"\n\n[System Note: Emergency compression dropped %d oldest messages due to context limit]",
			droppedCount,
		)
		enhancedSystemPrompt := history[0]
		enhancedSystemPrompt.Content = enhancedSystemPrompt.Content + compressionNote
		newHistory = append(newHistory, enhancedSystemPrompt)
	}

	newHistory = append(newHistory, keptConversation...)
	newHistory = append(newHistory, history[len(history)-1]) // Last message

	// Update session
	agent.Sessions.SetHistory(sessionKey, newHistory)
	agent.Sessions.Save(sessionKey)

	logger.WarnCF("agent", "Forced compression executed", map[string]any{
		"session_key":  sessionKey,
		"dropped_msgs": droppedCount,
		"new_count":    len(newHistory),
	})
}

// GetStartupInfo returns information about loaded tools and skills for logging.
func (al *AgentLoop) GetStartupInfo() map[string]any {
	info := make(map[string]any)

	registry := al.GetRegistry()
	agent := registry.GetDefaultAgent()
	if agent == nil {
		return info
	}

	// Tools info
	toolsList := agent.Tools.List()
	info["tools"] = map[string]any{
		"count": len(toolsList),
		"names": toolsList,
	}

	// Skills info
	info["skills"] = agent.ContextBuilder.GetSkillsInfo()

	// Agents info
	info["agents"] = map[string]any{
		"count": len(registry.ListAgentIDs()),
		"ids":   registry.ListAgentIDs(),
	}

	return info
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

// summarizeSession summarizes the conversation history for a session.
func (al *AgentLoop) summarizeSession(agent *AgentInstance, sessionKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	history := agent.Sessions.GetHistory(sessionKey)
	messagesBefore := len(history)
	summary := agent.Sessions.GetSummary(sessionKey)

	// Keep last 4 messages for continuity
	if len(history) <= 4 {
		return
	}

	toSummarize := history[:len(history)-4]

	// Oversized Message Guard
	maxMessageTokens := agent.ContextWindow / 2
	validMessages := make([]providers.Message, 0)
	omitted := false

	for _, m := range toSummarize {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		msgTokens := len(m.Content) / 2
		if msgTokens > maxMessageTokens {
			omitted = true
			continue
		}
		validMessages = append(validMessages, m)
	}

	if len(validMessages) == 0 {
		return
	}

	const (
		maxSummarizationMessages = 10
		llmMaxRetries            = 3
		llmTemperature           = 0.3
		fallbackMaxContentLength = 200
	)

	// Multi-Part Summarization
	var finalSummary string
	if len(validMessages) > maxSummarizationMessages {
		mid := len(validMessages) / 2

		mid = al.findNearestUserMessage(validMessages, mid)

		part1 := validMessages[:mid]
		part2 := validMessages[mid:]

		s1, _ := al.summarizeBatch(ctx, agent, part1, "")
		s2, _ := al.summarizeBatch(ctx, agent, part2, "")

		mergePrompt := fmt.Sprintf(
			"Merge these two conversation summaries into one cohesive summary:\n\n1: %s\n\n2: %s",
			s1,
			s2,
		)

		resp, err := al.retryLLMCall(ctx, agent, mergePrompt, llmMaxRetries)
		if err == nil && resp.Content != "" {
			finalSummary = resp.Content
		} else {
			finalSummary = s1 + " " + s2
		}
	} else {
		finalSummary, _ = al.summarizeBatch(ctx, agent, validMessages, summary)
	}

	if omitted && finalSummary != "" {
		finalSummary += "\n[Note: Some oversized messages were omitted from this summary for efficiency.]"
	}

	// Solution C: Always truncate to prevent unbounded context growth, even when
	// LLM summarization fails. Fall back to the existing summary or a mechanical
	// summary built from message snippets.
	if finalSummary == "" {
		if summary != "" {
			// Retain the existing summary unchanged rather than leaving the session untruncated.
			finalSummary = summary
		} else {
			// No existing summary and LLM failed — build a mechanical fallback from snippets.
			var fb strings.Builder
			fb.WriteString("Conversation summary: ")
			for i, m := range validMessages {
				if i > 0 {
					fb.WriteString(" | ")
				}
				content := strings.TrimSpace(m.Content)
				runes := []rune(content)
				keep := fallbackMaxContentLength
				if keep > len(runes) {
					keep = len(runes)
				}
				snippet := string(runes[:keep])
				if keep < len(runes) {
					snippet += "..."
				}
				fb.WriteString(fmt.Sprintf("%s: %s", m.Role, snippet))
			}
			finalSummary = fb.String()
		}
	}

	newSummary := finalSummary
	agent.Sessions.SetSummary(sessionKey, newSummary)
	agent.Sessions.TruncateHistory(sessionKey, 4)
	agent.Sessions.Save(sessionKey)

	messagesAfter := len(agent.Sessions.GetHistory(sessionKey))
	logger.InfoCF("agent", "Compaction complete",
		map[string]any{
			"agent_id":        agent.ID,
			"messages_before": messagesBefore,
			"messages_after":  messagesAfter,
			"summary_len":     len(newSummary),
		})

	// Solution D: Validate context size after summarization. If the remaining history
	// plus summary still exceeds the threshold, perform a second aggressive truncation.
	newHistory := agent.Sessions.GetHistory(sessionKey)
	tokenEstimate := al.estimateTokens(newHistory) + len(newSummary)/2
	threshold := agent.ContextWindow * agent.SummarizeTokenPercent / 100
	if tokenEstimate > threshold && len(newHistory) > 2 {
		agent.Sessions.TruncateHistory(sessionKey, 2)
		agent.Sessions.Save(sessionKey)
		logger.WarnCF("agent", "Post-summarization context still over threshold; applied aggressive truncation",
			map[string]any{
				"session_key":     sessionKey,
				"token_estimate":  tokenEstimate,
				"threshold":       threshold,
				"history_after":   2,
			})
	}
}

// findNearestUserMessage finds the nearest user message to the given index.
// It searches backward first, then forward if no user message is found.
func (al *AgentLoop) findNearestUserMessage(messages []providers.Message, mid int) int {
	originalMid := mid

	for mid > 0 && messages[mid].Role != "user" {
		mid--
	}

	if messages[mid].Role == "user" {
		return mid
	}

	mid = originalMid
	for mid < len(messages) && messages[mid].Role != "user" {
		mid++
	}

	if mid < len(messages) {
		return mid
	}

	return originalMid
}

// retryLLMCall calls the LLM with retry logic.
func (al *AgentLoop) retryLLMCall(
	ctx context.Context,
	agent *AgentInstance,
	prompt string,
	maxRetries int,
) (*providers.LLMResponse, error) {
	const (
		llmTemperature = 0.3
	)

	var resp *providers.LLMResponse
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		al.activeRequests.Add(1)
		resp, err = func() (*providers.LLMResponse, error) {
			defer al.activeRequests.Done()
			retryOpts := map[string]any{
				"max_tokens":       agent.MaxTokens,
				"prompt_cache_key": agent.ID,
			}
			// CLI providers do not accept HTTP request parameters such as temperature.
			if _, isCLI := agent.Provider.(providers.CLIProvider); !isCLI {
				retryOpts["temperature"] = llmTemperature
			}
			return agent.Provider.Chat(
				ctx,
				[]providers.Message{{Role: "user", Content: prompt}},
				nil,
				agent.Model,
				retryOpts,
			)
		}()

		if err == nil && resp != nil && resp.Content != "" {
			return resp, nil
		}
		if attempt < maxRetries-1 {
			time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
		}
	}

	return resp, err
}

// summarizeBatch summarizes a batch of messages.
func (al *AgentLoop) summarizeBatch(
	ctx context.Context,
	agent *AgentInstance,
	batch []providers.Message,
	existingSummary string,
) (string, error) {
	const (
		llmMaxRetries             = 3
		llmTemperature            = 0.3
		fallbackMinContentLength  = 200
		fallbackMaxContentPercent = 10
	)

	var sb strings.Builder
	sb.WriteString(
		"Provide a concise summary of this conversation segment, preserving core context and key points.\n",
	)
	if existingSummary != "" {
		sb.WriteString("Existing context: ")
		sb.WriteString(existingSummary)
		sb.WriteString("\n")
	}
	sb.WriteString("\nCONVERSATION:\n")
	for _, m := range batch {
		fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
	}
	prompt := sb.String()

	response, err := al.retryLLMCall(ctx, agent, prompt, llmMaxRetries)
	if err == nil && response.Content != "" {
		return strings.TrimSpace(response.Content), nil
	}

	var fallback strings.Builder
	fallback.WriteString("Conversation summary: ")
	for i, m := range batch {
		if i > 0 {
			fallback.WriteString(" | ")
		}
		content := strings.TrimSpace(m.Content)
		runes := []rune(content)
		if len(runes) == 0 {
			fallback.WriteString(fmt.Sprintf("%s: ", m.Role))
			continue
		}

		keepLength := len(runes) * fallbackMaxContentPercent / 100
		if keepLength < fallbackMinContentLength {
			keepLength = fallbackMinContentLength
		}

		if keepLength > len(runes) {
			keepLength = len(runes)
		}

		content = string(runes[:keepLength])
		if keepLength < len(runes) {
			content += "..."
		}
		fallback.WriteString(fmt.Sprintf("%s: %s", m.Role, content))
	}
	return fallback.String(), nil
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

func (al *AgentLoop) handleCommand(
	ctx context.Context,
	msg bus.InboundMessage,
	agent *AgentInstance,
	opts *processOptions,
) (string, bool) {
	if !commands.HasCommandPrefix(msg.Content) {
		return "", false
	}

	if al.cmdRegistry == nil {
		return "", false
	}

	rt := al.buildCommandsRuntime(agent, opts, msg)
	executor := commands.NewExecutor(al.cmdRegistry, rt)

	var commandReply string
	result := executor.Execute(ctx, commands.Request{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		SenderID: msg.SenderID,
		Text:     msg.Content,
		Reply: func(text string) error {
			commandReply = text
			return nil
		},
	})

	switch result.Outcome {
	case commands.OutcomeHandled:
		if result.Err != nil {
			return mapCommandError(result), true
		}
		if commandReply != "" {
			return commandReply, true
		}
		return "", true
	default: // OutcomePassthrough — let the message fall through to LLM
		return "", false
	}
}

func (al *AgentLoop) buildCommandsRuntime(agent *AgentInstance, opts *processOptions, msg bus.InboundMessage) *commands.Runtime {
	registry := al.GetRegistry()
	cfg := al.GetConfig()
	rt := &commands.Runtime{
		Config:          cfg,
		ListAgentIDs:    registry.ListAgentIDs,
		ListDefinitions: al.cmdRegistry.Definitions,
		GetEnabledChannels: func() []string {
			if al.channelManager == nil {
				return nil
			}
			return al.channelManager.GetEnabledChannels()
		},
		SwitchChannel: func(value string) error {
			if al.channelManager == nil {
				return fmt.Errorf("channel manager not initialized")
			}
			if _, exists := al.channelManager.GetChannel(value); !exists && value != "cli" {
				return fmt.Errorf("channel '%s' not found or not enabled", value)
			}
			return nil
		},
	}
	if agent != nil {
		if agent.Name != "" {
			rt.AgentName = agent.Name
		} else {
			rt.AgentName = agent.ID
		}
		rt.GetModelInfo = func() (string, string) {
			protocol, _ := providers.ExtractProtocol(agent.Model)
			return agent.Model, protocol
		}
		rt.SwitchModel = func(value string) (string, error) {
			oldModel := agent.Model
			agent.Model = value
			return oldModel, nil
		}

		rt.ClearHistory = func() error {
			if opts == nil {
				return fmt.Errorf("process options not available")
			}
			if agent.Sessions == nil {
				return fmt.Errorf("sessions not initialized for agent")
			}

			agent.Sessions.SetHistory(opts.SessionKey, make([]providers.Message, 0))
			agent.Sessions.SetSummary(opts.SessionKey, "")
			agent.Sessions.Save(opts.SessionKey)
			return nil
		}
		rt.ResetCooldown = func() {
			if al.fallback != nil {
				al.fallback.Reset()
			}
		}
		rt.RetriggerLastMessage = func(ctx context.Context) error {
			if agent == nil || agent.Sessions == nil || opts == nil {
				return fmt.Errorf("session not available")
			}
			history := agent.Sessions.GetHistory(opts.SessionKey)
			lastUserMsg := ""
			for i := len(history) - 1; i >= 0; i-- {
				if history[i].Role == "user" && history[i].Content != "" {
					lastUserMsg = history[i].Content
					break
				}
			}
			if lastUserMsg == "" {
				return fmt.Errorf("no previous message to retry")
			}
			retrigger := bus.InboundMessage{
				Channel:  msg.Channel,
				ChatID:   msg.ChatID,
				SenderID: msg.SenderID,
				Content:  lastUserMsg,
				Peer:     msg.Peer,
				IsRetry:  true,
			}
			go func() {
				pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := al.bus.PublishInbound(pubCtx, retrigger); err != nil {
					logger.WarnCF("agent", "Failed to retrigger message after /retry",
						map[string]any{"error": err.Error()})
				}
			}()
			return nil
		}
	}
	return rt
}

func mapCommandError(result commands.ExecuteResult) string {
	if result.Command == "" {
		return fmt.Sprintf("Failed to execute command: %v", result.Err)
	}
	return fmt.Sprintf("Failed to execute /%s: %v", result.Command, result.Err)
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

// extractParentPeer extracts the parent peer (reply-to) from inbound message metadata.
func extractParentPeer(msg bus.InboundMessage) *routing.RoutePeer {
	parentKind := inboundMetadata(msg, metadataKeyParentPeerKind)
	parentID := inboundMetadata(msg, metadataKeyParentPeerID)
	if parentKind == "" || parentID == "" {
		return nil
	}
	return &routing.RoutePeer{Kind: parentKind, ID: parentID}
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

	metadata := fmt.Sprintf("Agent: %s\nModel: %s\nSession: %s\nChannel: %s\nIteration: %d\nTimestamp: %s",
		agent.ID, model, opts.SessionKey, opts.Channel, iteration,
		time.Now().Format(time.RFC3339))

	filename, err := dump.Write(al.dumpsDir, "refusal", metadata, string(inputBytes), string(outputBytes))
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
			"dump_file":   filename,
		})
}
