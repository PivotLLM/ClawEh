// ClawEh - Personal AI Assistant
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/cogmem/consolidate"
	"github.com/PivotLLM/ClawEh/commands"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/msgtoken"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/state"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
	"github.com/PivotLLM/ClawEh/voice"
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
	activeRequests sync.WaitGroup
	dispatcher     *providers.ProviderDispatcher
	// cooldown is the shared per-model cooldown tracker used by BOTH the main
	// fallback chain and the compaction path, so a model parked by either (e.g.
	// an out-of-credits 402) is skipped by both. Swapped under mu on reload.
	cooldown        *providers.CooldownTracker
	messageManagers map[string]*msgtoken.Manager // agentID -> manager (nil entry means disabled)
	// namedTokens holds the long-lived, user-named message-API tokens (one store
	// for all agents, persisted under state/message-api-tokens.json). It is
	// separate from the rotating messageManagers: named tokens never expire and
	// are minted/revoked from the WebUI. The same instance is shared with the API
	// handler so a mint/revoke is visible to ValidateMessageToken immediately.
	namedTokens *msgtoken.NamedStore
	agentStates map[string]*state.Manager // agentID -> per-agent state manager
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

	// exposeReasoning caches the per-session "expose reasoning to the user" flag
	// (write-through to CompactionState). Same key scheme as activeModelIdx.
	exposeReasoningMu    sync.Mutex
	exposeReasoningCache map[string]bool

	// showToolActivity caches the per-session "post tool-call breadcrumbs" flag
	// (write-through to CompactionState). Same key scheme as activeModelIdx.
	showToolActivityMu    sync.Mutex
	showToolActivityCache map[string]bool

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

	// mcpRetryStop is closed by Close() to stop the background MCP reconnect loop
	// (mcpRetryLoop), which recovers servers whose initial connect failed.
	mcpRetryStop chan struct{}

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
// mcpserver.sessionTokenStore satisfies this interface.
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

	// Set up shared fallback chain with the config-driven cooldown policy.
	cooldown := providers.NewCooldownTrackerWithPolicy(cooldownPolicy(cfg))
	fallbackChain := providers.NewFallbackChain(cooldown)

	// Create state manager using default agent's workspace for channel recording
	defaultAgent := registry.GetDefaultAgent()
	var stateManager *state.Manager
	if defaultAgent != nil {
		stateManager = state.NewManager(defaultAgent.Workspace)
	}

	// Build per-agent state managers and message-token managers.
	agentStates := make(map[string]*state.Manager)

	for _, agentID := range registry.ListAgentIDs() {
		if agentInstance, ok := registry.GetAgent(agentID); ok {
			agentStates[agentID] = state.NewManager(agentInstance.Workspace)
		}
	}
	messageManagers := buildMessageManagers(registry, cfg)

	// Load the long-lived named message-API token store from the data dir. An
	// empty data dir (only happens in tests that build a bare Config) yields an
	// in-memory-only store so nothing is written to a relative path. A load error
	// (e.g. unreadable state file) is non-fatal: fall back to an empty in-memory
	// store so the gateway still boots; named tokens are then unusable until the
	// file is fixed, but rotating tokens and everything else keep working.
	namedTokenPath := ""
	if cfg.DataDir() != "" {
		namedTokenPath = msgtoken.NamedTokenPath(cfg.DataDir())
	}
	namedTokens, err := msgtoken.NewNamedStore(namedTokenPath)
	if err != nil {
		logger.WarnCF("message", "Failed to load named message-token store, starting empty",
			map[string]any{"error": err.Error()})
		namedTokens, _ = msgtoken.NewNamedStore("")
	}

	al := &AgentLoop{
		bus:                   msgBus,
		cfg:                   cfg,
		registry:              registry,
		state:                 stateManager,
		fallback:              fallbackChain,
		cooldown:              cooldown,
		cmdRegistry:           commands.NewRegistry(commands.BuiltinDefinitions()),
		dispatcher:            dispatcher,
		agentStates:           agentStates,
		messageManagers:       messageManagers,
		namedTokens:           namedTokens,
		startedAt:             time.Now(),
		evictStop:             make(chan struct{}),
		mcpRetryStop:          make(chan struct{}),
		evictTTL:              defaultEvictTTL,
		evictInterval:         defaultEvictInterval,
		activeModelIdx:        make(map[string]int),
		exposeReasoningCache:  make(map[string]bool),
		showToolActivityCache: make(map[string]bool),
		taskLive:              toolsagents.NewLiveSet(),
		spawnManagers:         make(map[string]*toolsagents.SubagentManager),
		superStop:             make(chan struct{}),
	}

	// Register runtime-dependent tools via providers (session closures,
	// spawn/subagent, msg with shared MessageTool).
	al.registerRuntimeTools(registry, provider, dispatcher, fallbackChain, cfg)

	return al
}

func (al *AgentLoop) Run(ctx context.Context) error {
	al.running.Store(true)

	if err := al.ensureMCPInitialized(ctx); err != nil {
		return err
	}

	// Start the background context-manager eviction goroutine.
	go al.evictContextManagers()

	// Start the background MCP reconnect loop, which recovers desired servers whose
	// initial connect failed (so a transiently-down upstream needs no restart).
	go al.mcpRetryLoop(ctx)

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
	case <-al.mcpRetryStop:
		// already closed
	default:
		close(al.mcpRetryStop)
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

	for _, mgr := range al.messageManagers {
		mgr.Stop()
	}

	al.GetRegistry().Close()
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

	// Register tools on the freshly built registry with the full runtime deps —
	// the same single registration the initial construction performs. Without
	// this, reloaded agents would have an empty tool set (NewAgentInstance no
	// longer registers anything). The new fallback chain is built here so it is
	// shared between the registered spawn tools and the swapped-in al.fallback.
	// Reuse the existing cooldown tracker so its state (notably out-of-credits
	// parks) PERSISTS across the reload; only refresh its policy from the new
	// config. The shared tracker continues to back both the new fallback chain
	// and the rebuilt compaction managers.
	al.cooldown.SetPolicy(cooldownPolicy(cfg))
	newFallback := providers.NewFallbackChain(al.cooldown)
	al.registerRuntimeTools(registry, provider, al.dispatcher, newFallback, cfg)

	// Rebuild callback managers against the new registry/config and wire them onto
	// the new ContextBuilders. Without this, reloaded agents get no callback token
	// injected, and the stale managers would keep validating tokens against the
	// OLD config (e.g. an agent whose callbacks were since disabled).
	newCallbackManagers := buildMessageManagers(registry, cfg)

	// Atomically swap the config and registry under write lock
	// This ensures readers see a consistent pair
	al.mu.Lock()
	oldRegistry := al.registry
	oldCallbackManagers := al.messageManagers

	// Store new values
	al.cfg = cfg
	al.registry = registry
	al.messageManagers = newCallbackManagers

	// Also update fallback chain with new config (same chain used for the tools
	// just registered above). al.cooldown is unchanged — the same shared tracker
	// persists across reload and backs both the chain and the rebuilt compaction
	// managers.
	al.fallback = newFallback

	al.mu.Unlock()

	// Stop the superseded callback managers (their tokens persist on disk and are
	// reloaded by the rebuilt manager for still-enabled agents).
	for _, mgr := range oldCallbackManagers {
		mgr.Stop()
	}

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

// cooldownTracker returns the shared cooldown tracker (thread-safe). Compaction
// managers use it so cooldowns are unified with the main fallback chain.
func (al *AgentLoop) cooldownTracker() *providers.CooldownTracker {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.cooldown
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
