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
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/PivotLLM/ClawEh/pkg/agenttoken"
	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/callback"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/cogmem/consolidate"
	"github.com/PivotLLM/ClawEh/pkg/commands"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/constants"
	"github.com/PivotLLM/ClawEh/pkg/dump"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/media"
	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/routing"
	"github.com/PivotLLM/ClawEh/pkg/state"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	toolsagents "github.com/PivotLLM/ClawEh/pkg/tools/agents"
	toolsmsg "github.com/PivotLLM/ClawEh/pkg/tools/msg"
	"github.com/PivotLLM/ClawEh/pkg/utils"
	"github.com/PivotLLM/ClawEh/pkg/voice"
)

type AgentLoop struct {
	bus             *bus.MessageBus
	cfg             *config.Config
	registry        *AgentRegistry
	state           *state.Manager
	running         atomic.Bool
	contextManagers sync.Map
	fallback        *providers.FallbackChain
	channelManager  *channels.Manager
	mediaStore      media.MediaStore
	transcriber     voice.Transcriber
	cmdRegistry     *commands.Registry
	mcp             mcpRuntime
	mu              sync.RWMutex
	// Track active requests for safe provider cleanup
	activeRequests   sync.WaitGroup
	dispatcher       *providers.ProviderDispatcher
	callbackManagers map[string]*callback.Manager // agentID -> manager (nil entry means disabled)
	agentTokens      *agenttoken.Manager
	agentStates      map[string]*state.Manager // agentID -> per-agent state manager
	// sessionMus serializes messages within a session (session scope key → *sync.Mutex).
	// The scope key is resolved via resolveMessageRoute before locking so that
	// multiple channel:chatID pairs that map to the same agent session share one
	// mutex and never process concurrently. Entries are never evicted; for typical
	// deployments with a bounded number of active sessions the cost is negligible
	// (one *sync.Mutex per session key).
	sessionMus          sync.Map
	sessionCancelStates sync.Map // scope key → *sessionCancelState
	lastSelfClear       sync.Map // session key → time.Time, rate-limits session_clear
	dumpsDir            string
	startedAt           time.Time

	// activeModelIdx caches the per-session active model index (write-through to
	// the session store's CompactionState). Key = agent.ID + "\x00" + sessionKey.
	activeModelMu  sync.Mutex
	activeModelIdx map[string]int

	// sessionTokenIssuer issues and revokes per-session MCP tokens. Wired in
	// from the MCP server at startup via SetSessionTokenIssuer; nil when the
	// MCP host is not configured.
	sessionTokenIssuer SessionTokenIssuer

	// cogmemManager schedules background cognitive-memory consolidation. Wired in
	// from the gateway at startup via SetCogmemManager; nil when cognitive memory
	// is not in use. Only ever consulted for cognitive agents (those allowed the
	// cogmem tools), so non-cognitive agents are entirely unaffected.
	cogmemManager *consolidate.Manager

	// Context manager idle eviction.
	// evictStop is closed by Close() to signal the eviction goroutine to exit.
	// evictTTL is the idle TTL after which an unused context manager is evicted.
	// evictInterval is how often the eviction pass runs.
	evictStop     chan struct{}
	evictTTL      time.Duration
	evictInterval time.Duration

	// Background-task supervision. taskLive is the process-shared running-task
	// set, shared by every per-agent SubagentManager (across reloads) so the
	// supervisor never relaunches a task that is still running. spawnManagers maps
	// agentID → its current manager (rebuilt on each registerRuntimeTools), used
	// by the supervisor to scan/relaunch interrupted tasks. superStop is closed by
	// Close() to stop the supervisor goroutine.
	taskLive      *toolsagents.LiveSet
	spawnMu       sync.Mutex
	spawnManagers map[string]*toolsagents.SubagentManager
	superStop     chan struct{}
}

// SessionTokenIssuer issues and revokes SST-prefixed session tokens used by
// session-scoped MCP tools (get_session_messages, search_session_messages).
// pkg/mcpserver.sessionTokenStore satisfies this interface.
type SessionTokenIssuer interface {
	// Issue generates and stores a new token for the given session. Returns
	// the SST<64hex> token, or "" on failure.
	Issue(agentID, sessionKey, archiveDir string) string
	// Revoke removes the token for a given session key.
	Revoke(sessionKey string)
	// RevokeAgent removes all tokens for a given agent.
	RevokeAgent(agentID string)
	// SetSource records the most recent inbound user-message source (channel +
	// chatID) on the session record so MCP-routed tool dispatch can publish a
	// tool's ForUser payload back to the originating user. No-op when the
	// sessionKey is unknown.
	SetSource(sessionKey, channel, chatID string)
}

// sessionCancelState tracks a pending cancel request for a session. When
// pending is set, goroutines waiting for the session mutex will skip themselves
// rather than process, incrementing skipCount. The /cancel command reads and
// resets both fields.
type sessionCancelState struct {
	pending   atomic.Bool
	skipCount atomic.Int32
}

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
}

const (
	defaultResponse               = "I've completed processing but have no response to give. Increase `max_tool_iterations` in config.json."
	sessionKeyAgentPrefix         = "agent:"
	metadataKeyAccountID          = "account_id"
	metadataKeyGuildID            = "guild_id"
	metadataKeyTeamID             = "team_id"
	metadataKeyParentPeerKind     = "parent_peer_kind"
	metadataKeyParentPeerID       = "parent_peer_id"
	metadataKeyPreresolvedAgentID = "preresolved_agent_id"
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

	// Create state manager using default agent's workspace for channel recording
	defaultAgent := registry.GetDefaultAgent()
	var stateManager *state.Manager
	if defaultAgent != nil {
		stateManager = state.NewManager(defaultAgent.Workspace)
	}

	// Build per-agent state managers, callback managers, and agent tokens.
	agentStates := make(map[string]*state.Manager)
	callbackManagers := make(map[string]*callback.Manager)
	agentTokens := agenttoken.NewManager()
	issueAgentTokens(registry, agentTokens)

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
		fallback:         fallbackChain,
		cmdRegistry:      commands.NewRegistry(commands.BuiltinDefinitions()),
		dispatcher:       dispatcher,
		agentStates:      agentStates,
		callbackManagers: callbackManagers,
		agentTokens:      agentTokens,
		startedAt:        time.Now(),
		evictStop:        make(chan struct{}),
		evictTTL:         defaultEvictTTL,
		evictInterval:    defaultEvictInterval,
		activeModelIdx:   make(map[string]int),
		taskLive:         toolsagents.NewLiveSet(),
		spawnManagers:    make(map[string]*toolsagents.SubagentManager),
		superStop:        make(chan struct{}),
	}

	// Register runtime-dependent tools via providers (session closures,
	// spawn/subagent, msg with shared MessageTool).
	al.registerRuntimeTools(registry, provider, dispatcher, fallbackChain)

	return al
}

func (al *AgentLoop) Run(ctx context.Context) error {
	al.running.Store(true)

	if err := al.ensureMCPInitialized(ctx); err != nil {
		return err
	}

	// Start the background context-manager eviction goroutine.
	go al.evictContextManagers()

	// Start the background task supervisor: periodically relaunches interrupted
	// callback tasks (.run markers with no live worker).
	go al.superviseTasks(ctx)

	al.recoverPendingTurns(ctx)

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
	msgCtx := tools.WithRoundSentFlag(ctx, &roundSent)

	response, err := al.processMessage(msgCtx, msg)
	if err != nil {
		if friendly := renderBillingError(err); friendly != "" {
			response = friendly
		} else {
			response = fmt.Sprintf("Error processing message: %v", err)
		}
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
	// Signal the eviction goroutine to stop and drain all remaining managers.
	// Use a non-blocking close in case Close() is called before Run().
	select {
	case <-al.evictStop:
		// already closed
	default:
		close(al.evictStop)
	}
	select {
	case <-al.superStop:
		// already closed
	default:
		close(al.superStop)
	}
	al.drainContextManagers()

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

	if al.agentTokens != nil {
		al.agentTokens.RevokeAll()
	}

	al.GetRegistry().Close()
}

// AgentTokens returns the manager that holds the per-agent MCP isolation
// tokens. Used by the MCP server to resolve the calling agent.
func (al *AgentLoop) AgentTokens() *agenttoken.Manager {
	return al.agentTokens
}

// SetSessionTokenIssuer wires the MCP server's session token store into the
// agent loop so that session tokens are issued when a new ContextManager is
// created and revoked on eviction or session clear.
func (al *AgentLoop) SetSessionTokenIssuer(sti SessionTokenIssuer) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.sessionTokenIssuer = sti
}

// SetCogmemManager wires the cognitive-memory consolidation manager into the
// agent loop. The loop notifies it (OnMessage) on archive writes for cognitive
// agents only. Nil → cognitive memory inert (non-cognitive agents are never
// affected regardless).
func (al *AgentLoop) SetCogmemManager(mgr *consolidate.Manager) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.cogmemManager = mgr
}

// issueAgentTokens issues (or re-issues) MCP isolation tokens for every
// registered agent and injects them into the agents' system prompts. The
// boot-log entry is the Eric-approved assertion that lets mis-bindings
// show up at startup.
func issueAgentTokens(registry *AgentRegistry, mgr *agenttoken.Manager) {
	if mgr == nil || registry == nil {
		return
	}
	for _, agentID := range registry.ListAgentIDs() {
		instance, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}
		token := mgr.Issue(agentID)
		instance.ContextBuilder.WithAgentToken(token)
		logger.InfoCF("mcpserver", "Agent token registered",
			map[string]any{
				"agent":     agentID,
				"workspace": instance.Workspace,
			})
	}
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
) {
	cfg := al.GetConfig()

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
		})
		managers[agentID] = spawnMgr
		spawner := toolsagents.NewSpawner(spawnMgr)
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

		for _, p := range tools.GetProviders() {
			if ok, _ := p.Available(cfg); !ok {
				continue
			}
			builtTools := p.Build(deps)
			for _, t := range builtTools {
				if agentCfg == nil || agentCfg.IsToolAllowed(t.Name()) {
					currentAgent.Tools.Register(t)
				}
			}
		}
	}

	al.spawnMu.Lock()
	al.spawnManagers = managers
	al.spawnMu.Unlock()
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

	// Register tools on the freshly built registry with the full runtime deps —
	// the same single registration the initial construction performs. Without
	// this, reloaded agents would have an empty tool set (NewAgentInstance no
	// longer registers anything). The new fallback chain is built here so it is
	// shared between the registered spawn tools and the swapped-in al.fallback.
	newFallback := providers.NewFallbackChain(providers.NewCooldownTracker())
	al.registerRuntimeTools(registry, provider, al.dispatcher, newFallback)

	// Re-issue MCP isolation tokens for the new registry so the new
	// agents' context builders carry fresh tokens. Old tokens are
	// implicitly replaced by Issue(); agents removed from config are
	// not pruned here because the manager has no way to know.
	if al.agentTokens != nil {
		al.agentTokens.RevokeAll()
		issueAgentTokens(registry, al.agentTokens)
	}

	// Atomically swap the config and registry under write lock
	// This ensures readers see a consistent pair
	al.mu.Lock()
	oldRegistry := al.registry

	// Store new values
	al.cfg = cfg
	al.registry = registry

	// Also update fallback chain with new config (same chain used for the tools
	// just registered above).
	al.fallback = newFallback

	al.mu.Unlock()

	// Flush the dispatcher cache so stale providers are evicted on config reload.
	if al.dispatcher != nil {
		al.dispatcher.Flush(cfg)
	}

	// Drop cached ContextManagers so per-session config baked in at creation —
	// notably the summarization model chain — is rebuilt from the new config on
	// next use.
	al.invalidateContextManagers()

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

	// Propagate store to send_file / msg_send_file tools in all agents.
	registry := al.GetRegistry()
	type mediaStoreSetter interface {
		SetMediaStore(media.MediaStore)
	}
	for _, toolName := range []string{"send_file", "msg_send_file"} {
		registry.ForEachTool(toolName, func(t tools.Tool) {
			if sf, ok := t.(mediaStoreSetter); ok {
				sf.SetMediaStore(s)
			}
		})
	}
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

	userContent := msg.Content
	if msg.Peer.Kind == "group" || msg.Peer.Kind == "channel" {
		if label := senderLabel(msg.Sender); label != "" {
			userContent = "[From: " + label + "]\n" + msg.Content
		}
	}

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

	// Attach the agent ID to the context so every downstream provider call —
	// including compression-time invocations through PreDispatchCheck,
	// CheckAndCompress, AddUserMessage's trigger check, and the compact_session
	// MCP tool — surfaces the agent in error logs. runLLMIteration also wraps
	// ctx for its own scope, but compression paths fan out from runAgentLoop
	// before reaching that point, so we set it once here at the top.
	ctx = providers.WithAgentID(ctx, agent.ID)

	// 1. Get or create the ContextManager for this session.
	cm, releaseCtxMgr := al.getContextManager(agent, opts.SessionKey)
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
		}
		if err := cm.AddUserMessage(ctx, userMsg); err != nil {
			logger.WarnCF("agent", "Failed to add user message to context manager",
				map[string]any{"error": err.Error(), "session": opts.SessionKey})
		}
	}

	// 3. Build messages from current history + session context.
	messages, buildErr := cm.Build(ctx)
	if buildErr != nil {
		return "", fmt.Errorf("context manager build: %w", buildErr)
	}

	// Post-Build emergency check: include overhead (system prompt, tool defs,
	// completion budget) to catch cases where stored history is within threshold
	// but the full request would exceed the context window. Like PreDispatchCheck,
	// this only fires at the safety-net level; first-stage compaction happens at
	// the turn boundary (AddUserMessage / AddAssistantMessage), not here.
	if messages, buildErr = cm.CheckAndCompress(ctx, messages); buildErr != nil {
		logger.WarnCF("agent", "CheckAndCompress failed (continuing with current slice)",
			map[string]any{"error": buildErr.Error(), "session": opts.SessionKey})
	}

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
	finalContent, normal, finishReason, iteration, err := al.runLLMIteration(ctx, agent, messages, opts, cm)
	if err != nil {
		return "", err
	}

	// If last tool had ForUser content and we already sent it, we might not need to send final response
	// This is controlled by the tool's Silent flag and ForUser content

	// 5. Handle empty response.
	// Normal termination with no content means the LLM has nothing to say (e.g., message
	// was @-directed at another agent). Non-normal termination means something went wrong.
	isSystemError := false
	if finalContent == "" {
		if normal {
			logger.DebugCF("agent", "LLM returned empty response with normal termination",
				map[string]any{
					"agent_id":      agent.ID,
					"session_key":   opts.SessionKey,
					"finish_reason": finishReason,
				})
			return "", nil
		}
		isSystemError = true
		finalContent = fmt.Sprintf("The AI provider returned an empty response (finish reason: %s). Check provider logs for details.", finishReason)
	}

	// 6. Save final assistant message to session (skip system error strings)
	if !isSystemError {
		if err := cm.AddAssistantMessage(ctx, providers.Message{Role: "assistant", Content: finalContent}); err != nil {
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
	cm llmcontext.ContextManager,
) (string, bool, string, int, error) {
	iteration := 0
	var finalContent string
	lastNormal := false
	lastFinishReason := "max_iterations"

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

		// Run pre-dispatch compression check. If tool-call messages or tool results
		// pushed the context over the threshold, compress now and use the rebuilt
		// slice so the provider never receives a stale pre-compression slice.
		{
			var preErr error
			messages, preErr = cm.PreDispatchCheck(ctx, messages)
			if preErr != nil && !errors.Is(preErr, llmcontext.ErrCompressionFailed) {
				logger.WarnCF("agent", "PreDispatchCheck unexpected error", map[string]any{
					"agent_id": agent.ID,
					"error":    preErr.Error(),
				})
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

		// activeProvider tracks the protocol name surfaced in the finish event.
		activeProvider := ""
		if len(activeCandidates) > 0 {
			activeProvider = activeCandidates[0].Provider
		}

		callLLM := func() (*providers.LLMResponse, error) {
			al.activeRequests.Add(1)
			defer al.activeRequests.Done()

			if len(activeCandidates) >= 1 && al.fallback != nil {
				fbResult, fbErr := al.fallback.Execute(
					ctx,
					activeCandidates,
					func(ctx context.Context, c providers.FallbackCandidate) (*providers.LLMResponse, error) {
						if al.dispatcher != nil {
							key := c.Alias
							if key == "" {
								key = c.Provider + "/" + c.Model
							}
							if p, err := al.dispatcher.Get(key); err == nil {
								return p.Chat(ctx, messages, providerToolDefs, c.Model, llmOpts)
							}
						}
						return agent.Provider.Chat(ctx, messages, providerToolDefs, c.Model, llmOpts)
					},
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
			// No active fallback chain (no candidates, or fallback disabled):
			// route through the dispatcher-resolved provider for the agent's
			// primary model so non-default protocols don't get silently routed
			// through the shared agent.Provider (the default-config claude-cli).
			// resolveRunProvider returns agent.Provider + activeModel as the
			// last-resort safety net when dispatcher resolution cannot satisfy
			// the request.
			return runProvider.Chat(ctx, messages, providerToolDefs, runModel, llmOpts)
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
					return "", false, "", 0, ctx.Err()
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
				if rebuilt, berr := comprMgr.Build(ctx); berr == nil {
					messages = rebuilt
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
			return "", false, "", iteration, fmt.Errorf("LLM call failed after retries: %w", err)
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

		go al.handleReasoning(
			ctx,
			response.Reasoning,
			opts.Channel,
			al.targetReasoningChannelID(opts.Channel),
		)

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
			if finalContent == "" && response.ReasoningContent != "" {
				finalContent = response.ReasoningContent
			}
			lastNormal = response.Normal
			lastFinishReason = response.FinishReason
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
		// Feed the recent-tool ring so cognitive memory can auto-load domains whose
		// triggers match a tool the agent just used; the next build (same turn,
		// after tool results) picks it up. No-op for non-cognitive agents.
		cm.RecordToolUse(toolNames...)
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
		if err := cm.AddToolCallMessage(ctx, assistantMsg); err != nil {
			logger.WarnCF("agent", "AddToolCallMessage failed", map[string]any{
				"agent_id": agent.ID,
				"error":    err.Error(),
			})
		}

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
				execCtx = tools.WithSessionKey(execCtx, opts.SessionKey)

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
		streamActivity := al.GetConfig().Agents.Defaults.StreamToolActivity
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
			messages = append(messages, toolResultMsg)

			// Save tool result message through the context manager so that
			// msgCount is incremented and the message is written to the archive.
			if err := cm.AddToolResult(ctx, toolResultMsg); err != nil {
				logger.WarnCF("agent", "AddToolResult failed", map[string]any{
					"agent_id": agent.ID,
					"error":    err.Error(),
				})
			}
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

	return finalContent, lastNormal, lastFinishReason, iteration, nil
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

// compactionStateStore is the subset of the session store used to persist the
// per-session active model index inside the CompactionState record.
type compactionStateStore interface {
	GetCompactionState(sessionKey string) (memory.CompactionState, error)
	SetCompactionState(sessionKey string, state memory.CompactionState) error
}

// activeModelCacheKey builds the cache key for a session's active model index.
func activeModelCacheKey(agentID, sessionKey string) string {
	return agentID + "\x00" + sessionKey
}

// getActiveModelIndex returns the session's active model index, loading it from
// the session store on a cache miss. The result is clamped to a valid candidate
// index and cached.
func (al *AgentLoop) getActiveModelIndex(agent *AgentInstance, sessionKey string) int {
	key := activeModelCacheKey(agent.ID, sessionKey)

	al.activeModelMu.Lock()
	if idx, ok := al.activeModelIdx[key]; ok {
		al.activeModelMu.Unlock()
		return idx
	}
	al.activeModelMu.Unlock()

	idx := 0
	if store, ok := agent.Sessions.(compactionStateStore); ok {
		if st, err := store.GetCompactionState(sessionKey); err == nil {
			idx = st.ActiveModelIndex
		}
	}
	// Clamp to the valid candidate range.
	if n := len(agent.Candidates); n == 0 {
		idx = 0
	} else if idx < 0 {
		idx = 0
	} else if idx >= n {
		idx = n - 1
	}

	al.activeModelMu.Lock()
	al.activeModelIdx[key] = idx
	al.activeModelMu.Unlock()
	return idx
}

// setActiveModelIndex sets the session's active model index, updating the cache
// and persisting it through the session store's CompactionState. Persistence is
// best-effort: a store error is logged but does not fail the call.
func (al *AgentLoop) setActiveModelIndex(agent *AgentInstance, sessionKey string, idx int) error {
	n := len(agent.Candidates)
	if n == 0 {
		return fmt.Errorf("this agent has no selectable models")
	}
	if idx < 0 || idx >= n {
		return fmt.Errorf("model index out of range (0-%d)", n-1)
	}

	key := activeModelCacheKey(agent.ID, sessionKey)
	al.activeModelMu.Lock()
	al.activeModelIdx[key] = idx
	al.activeModelMu.Unlock()

	if store, ok := agent.Sessions.(compactionStateStore); ok {
		st, err := store.GetCompactionState(sessionKey)
		if err != nil {
			logger.WarnCF("agent", "active model index: load compaction state failed",
				map[string]any{"session_key": sessionKey, "error": err.Error()})
			return nil
		}
		st.ActiveModelIndex = idx
		if err := store.SetCompactionState(sessionKey, st); err != nil {
			logger.WarnCF("agent", "active model index: persist failed",
				map[string]any{"session_key": sessionKey, "error": err.Error()})
		}
	}
	return nil
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
		cmdName, _ := commands.ParseCommandName(msg.Content)
		if cmdName != "" {
			return fmt.Sprintf("Unknown command: /%s", cmdName), true
		}
		return "Unknown command.", true
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
	default:
		if result.Command != "" {
			return fmt.Sprintf("Unknown command: /%s. Type /help for available commands.", result.Command), true
		}
		return "Unknown command. Type /help for available commands.", true
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
		GetSessionChannels: func() []string {
			if cfg == nil || agent == nil {
				return nil
			}
			return sessionChannelsForAgent(cfg.Bindings, agent.ID)
		},
		GetArchiveStats: func() (int, time.Time, time.Time) {
			if agent == nil || opts == nil {
				return 0, time.Time{}, time.Time{}
			}
			path := archiveDBPath(agent.Workspace, opts.SessionKey)
			store, err := memory.OpenReadOnly(path)
			if err != nil {
				return 0, time.Time{}, time.Time{}
			}
			defer store.Close()
			count, first, last, _ := store.Stats()
			return count, first, last
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
		Uptime: func() time.Duration { return time.Since(al.startedAt) },
		GetSessionStats: func() (int, int, int) {
			if agent == nil || agent.Sessions == nil || opts == nil {
				return 0, 0, 0
			}
			history := agent.Sessions.GetHistory(opts.SessionKey)
			summary := agent.Sessions.GetSummary(opts.SessionKey)
			return len(history), al.estimateTokens(history), len(summary)
		},
	}
	if agent != nil {
		if agent.Name != "" {
			rt.AgentName = agent.Name
		} else {
			rt.AgentName = agent.ID
		}
		rt.GetModelInfo = func() (name, provider, protocol, apiBase string) {
			// Resolve the model that is actually active for THIS session (the
			// /model selection), not just the agent's first candidate, so /status
			// and /show model reflect the current choice.
			active := agent.Model
			if len(agent.Candidates) > 0 {
				idx := 0
				if opts != nil {
					idx = al.getActiveModelIndex(agent, opts.SessionKey)
				}
				if idx >= 0 && idx < len(agent.Candidates) {
					if a := agent.Candidates[idx].Alias; a != "" {
						active = a
					} else if m := agent.Candidates[idx].Model; m != "" {
						active = m
					}
				}
			}
			// Resolve the configured model so the provider name, wire protocol,
			// and base URL come from the model's named provider.
			name = active
			if mc, err := cfg.GetModelConfig(active); err == nil && mc != nil {
				if mc.ModelName != "" {
					name = mc.ModelName
				} else if mc.Model != "" {
					name = mc.Model
				}
				if prov, perr := cfg.GetProvider(mc.Provider); perr == nil && prov != nil {
					provider = prov.Name
					protocol = prov.Protocol
					apiBase = prov.BaseURL
				}
			}
			return name, provider, protocol, apiBase
		}
		rt.GetAgentModels = func() ([]commands.ModelEntry, int) {
			entries := make([]commands.ModelEntry, 0, len(agent.Candidates))
			for _, c := range agent.Candidates {
				name := c.Alias
				if name == "" {
					name = c.Model
				}
				entries = append(entries, commands.ModelEntry{Name: name, Provider: c.Provider})
			}
			active := 0
			if opts != nil {
				active = al.getActiveModelIndex(agent, opts.SessionKey)
			}
			return entries, active
		}
		rt.SetActiveModel = func(idx int) (string, error) {
			if opts == nil {
				return "", fmt.Errorf("process options not available")
			}
			if err := al.setActiveModelIndex(agent, opts.SessionKey, idx); err != nil {
				return "", err
			}
			name := agent.Candidates[idx].Alias
			if name == "" {
				name = agent.Candidates[idx].Model
			}
			return name, nil
		}

		rt.ClearHistory = func() error {
			if opts == nil {
				return fmt.Errorf("process options not available")
			}
			if agent.Sessions == nil {
				return fmt.Errorf("sessions not initialized for agent")
			}
			cm, releaseCM := al.getContextManager(agent, opts.SessionKey)
			defer releaseCM()
			if err := cm.Reset(context.Background()); err != nil {
				logger.WarnCF("agent", "clear: Reset failed", map[string]any{
					"session_key": opts.SessionKey,
					"error":       err.Error(),
				})
				return err
			}
			// Revoke the old session token and issue a fresh one. The LLM will
			// receive the new token in the next Build() call.
			al.mu.RLock()
			sti := al.sessionTokenIssuer
			al.mu.RUnlock()
			if sti != nil {
				archiveDir := filepath.Join(agent.Workspace, "sessions")
				tok := sti.Issue(agent.ID, opts.SessionKey, archiveDir)
				if tok != "" {
					cm.SetSessionToken(tok)
				}
			}
			// Notify the agent that its context was cleared, so it can re-orient
			// (the same notice an agent-initiated clear delivers, minus a handoff).
			// The reset already happened above, so no reset metadata is set.
			notice := bus.InboundMessage{
				Channel:    msg.Channel,
				ChatID:     msg.ChatID,
				SenderID:   "system",
				SessionKey: opts.SessionKey,
				Content:    wrapClearNotice(""),
				Peer:       msg.Peer,
			}
			go func() {
				pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := al.bus.PublishInbound(pubCtx, notice); err != nil {
					logger.WarnCF("agent", "clear: failed to publish clear notice", map[string]any{
						"session_key": opts.SessionKey, "error": err.Error(),
					})
				}
			}()
			return nil
		}
		rt.CompactHistory = func(ctx context.Context) (string, error) {
			if opts == nil {
				return "", fmt.Errorf("process options not available")
			}
			cm, releaseCM := al.getContextManager(agent, opts.SessionKey)
			defer releaseCM()
			cm.SetCallContext(opts.Channel, opts.ChatID)
			err := cm.Compact(ctx)
			report := ""
			if r := cm.LastCompactionReport(); r != nil {
				report = r.String()
			}
			return report, err
		}
		rt.ResetCooldown = func() {
			if al.fallback != nil {
				al.fallback.Reset()
			}
		}
		rt.ClearCooldown = func(provider, model string) bool {
			if al.fallback == nil {
				return false
			}
			return al.fallback.Clear(provider, model)
		}
		rt.ListCooldowns = func() []commands.CooldownEntry {
			if al.fallback == nil {
				return nil
			}
			snap := al.fallback.CooldownSnapshot()
			out := make([]commands.CooldownEntry, 0, len(snap))
			for _, s := range snap {
				out = append(out, commands.CooldownEntry{
					Provider: s.Provider,
					Model:    s.Model,
					Reason:   string(s.Reason),
					Since:    s.Since,
					Until:    s.Until,
				})
			}
			return out
		}
		if opts != nil {
			sessionKey := opts.SessionKey
			rt.CancelPending = func() int {
				cs := al.getOrCreateCancelState(sessionKey)
				cs.pending.Store(false)
				return int(cs.skipCount.Swap(0))
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

// sessionChannelsForAgent returns the distinct channel names that route to
// agentID via the supplied bindings. The result is the per-agent channel
// surface area — used by /status to avoid leaking the full daemon-wide
// channel list to callers on unrelated channels. Channel names are returned
// in the order they first appear in bindings; comparisons are case-insensitive
// for dedup but original casing is preserved in the output.
func sessionChannelsForAgent(bindings []config.AgentBinding, agentID string) []string {
	target := routing.NormalizeAgentID(agentID)
	seen := make(map[string]struct{})
	var out []string
	for _, b := range bindings {
		if routing.NormalizeAgentID(b.AgentID) != target {
			continue
		}
		ch := strings.TrimSpace(b.Match.Channel)
		if ch == "" {
			continue
		}
		key := strings.ToLower(ch)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ch)
	}
	return out
}

// archiveDBPath returns the on-disk path of the SQLite archive for a session.
// Must stay in sync with pkg/llmcontext.sanitizeSessionKey and the
// archive-open path in pkg/llmcontext/Manager.getOrOpenArchive.
func archiveDBPath(workspace, sessionKey string) string {
	sanitized := strings.ReplaceAll(sessionKey, ":", "_")
	sanitized = strings.ReplaceAll(sanitized, "/", "_")
	sanitized = strings.ReplaceAll(sanitized, "\\", "_")
	return filepath.Join(workspace, "sessions", sanitized+".archive.db")
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

// metaSessionReset, when set to "true" on an inbound message's Metadata, tells
// processMessage to clear the session (preserving the archive) before handling
// the message. Set by session_clear so the reset happens at a clean turn
// boundary instead of mid-turn.
const metaSessionReset = "session_reset"

// selfClearCooldown rate-limits agent-initiated session_clear per session to
// prevent a clear→continue→clear runaway loop.
const selfClearCooldown = 10 * time.Second

// wrapClearNotice builds the system notice delivered to the agent on the fresh
// turn after a clear, optionally appending a self-authored handoff note.
func wrapClearNotice(handoff string) string {
	const base = "[System notice] Your active conversation was just cleared. Your long-term memory is " +
		"preserved — retrieve past messages with session_messages / session_search and past summaries " +
		"with session_summary_get."
	if h := strings.TrimSpace(handoff); h != "" {
		return base + "\n\nHandoff note you left yourself before clearing:\n" + h
	}
	return base
}

// allowSelfClear reports whether an agent-initiated session_clear may proceed for
// the session now, enforcing selfClearCooldown to bound runaway loops.
func (al *AgentLoop) allowSelfClear(sessionKey string) bool {
	now := time.Now()
	if v, ok := al.lastSelfClear.Load(sessionKey); ok {
		if last, ok := v.(time.Time); ok && now.Sub(last) < selfClearCooldown {
			return false
		}
	}
	al.lastSelfClear.Store(sessionKey, now)
	return true
}
