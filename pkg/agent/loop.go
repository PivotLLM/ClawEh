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
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/cogmem/consolidate"
	cogmemstore "github.com/PivotLLM/ClawEh/pkg/cogmem/store"
	"github.com/PivotLLM/ClawEh/pkg/commands"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/constants"
	"github.com/PivotLLM/ClawEh/pkg/dump"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/media"
	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/msgtoken"
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
	IsGroup         bool     // True when the inbound message came from a group/multi-listener chat
	IterationsOut   *int     // optional: runAgentLoop writes the LLM iteration count here
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
		bus:                  msgBus,
		cfg:                  cfg,
		registry:             registry,
		state:                stateManager,
		fallback:             fallbackChain,
		cooldown:             cooldown,
		cmdRegistry:          commands.NewRegistry(commands.BuiltinDefinitions()),
		dispatcher:           dispatcher,
		agentStates:          agentStates,
		messageManagers:      messageManagers,
		namedTokens:          namedTokens,
		startedAt:            time.Now(),
		evictStop:            make(chan struct{}),
		evictTTL:             defaultEvictTTL,
		evictInterval:        defaultEvictInterval,
		activeModelIdx:       make(map[string]int),
		exposeReasoningCache: make(map[string]bool),
		taskLive:             toolsagents.NewLiveSet(),
		spawnManagers:        make(map[string]*toolsagents.SubagentManager),
		superStop:            make(chan struct{}),
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
	// Overall turn budget: a hard backstop so a hung provider or tool can never
	// leave the user waiting forever. When it elapses the context is cancelled,
	// the LLM/tool loop unwinds, and we deliver a clear message below (which also
	// clears the typing indicator via the channel manager's preSend).
	turnTimeout := al.GetConfig().Agents.Defaults.GetTurnTimeout()
	turnCtx, turnCancel := context.WithTimeout(ctx, turnTimeout)
	defer turnCancel()
	msgCtx := tools.WithRoundSentFlag(turnCtx, &roundSent)

	response, err := al.processMessageSafely(msgCtx, msg)
	if err != nil {
		response = renderTurnError(turnCtx, turnTimeout, err)
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

// ValidateMessageToken resolves an external-message token to its owning agent. It
// checks BOTH token mechanisms: the long-lived named tokens (minted in the WebUI)
// and the short-lived rotating per-agent tokens. Named tokens are checked first
// since they are the primary, operator-facing path. Returns ("", false) if no
// match.
func (al *AgentLoop) ValidateMessageToken(token string) (string, bool) {
	agentID, _, _, ok := al.resolveMessageToken(token)
	return agentID, ok
}

// resolveMessageToken resolves a token to its owning agent and reports whether
// it is a named token (which is rate-limited) or a rotating one. named is only
// meaningful when ok is true. Shared by ValidateMessageToken and
// CheckMessageToken so both paths agree on which store owns a token.
func (al *AgentLoop) resolveMessageToken(token string) (agentID, tokenID string, named, ok bool) {
	al.mu.RLock()
	managers := al.messageManagers
	namedStore := al.namedTokens
	al.mu.RUnlock()
	if namedStore != nil {
		if aid, tid, found := namedStore.ValidateWithID(token); found {
			return aid, tid, true, true
		}
	}
	for aid, mgr := range managers {
		if mgr.Validate(token) {
			return aid, "", false, true
		}
	}
	return "", "", false, false
}

// MsgTokenDecision is the closed set of outcomes for an external-message token
// check: a valid token, an unknown/expired one, or a named token that is
// currently rate-limited (blocked).
type MsgTokenDecision int

const (
	MsgTokenInvalid MsgTokenDecision = iota
	MsgTokenValid
	MsgTokenRateLimited
)

// CheckMessageToken resolves a token AND applies the per-token rate limit for
// named tokens. Rotating tokens are never rate-limited (they are short-lived by
// construction). retryAfter is only meaningful for MsgTokenRateLimited.
func (al *AgentLoop) CheckMessageToken(token string) (agentID string, retryAfter time.Duration, decision MsgTokenDecision) {
	aid, tokenID, named, ok := al.resolveMessageToken(token)
	if !ok {
		return "", 0, MsgTokenInvalid
	}
	if !named {
		return aid, 0, MsgTokenValid
	}
	al.mu.RLock()
	namedStore := al.namedTokens
	al.mu.RUnlock()
	if namedStore == nil {
		return aid, 0, MsgTokenValid
	}
	allowed, ra := namedStore.Allow(aid, tokenID)
	if !allowed {
		return aid, ra, MsgTokenRateLimited
	}
	return aid, 0, MsgTokenValid
}

// ListMessageTokens returns the named message-API tokens for an agent.
func (al *AgentLoop) ListMessageTokens(agentID string) []msgtoken.NamedToken {
	al.mu.RLock()
	named := al.namedTokens
	al.mu.RUnlock()
	if named == nil {
		return nil
	}
	return named.List(agentID)
}

// CreateMessageToken mints a new named message-API token for an agent and
// persists it. The returned token includes its plaintext secret.
func (al *AgentLoop) CreateMessageToken(agentID, name string) (msgtoken.NamedToken, error) {
	al.mu.RLock()
	named := al.namedTokens
	al.mu.RUnlock()
	if named == nil {
		return msgtoken.NamedToken{}, fmt.Errorf("named message-token store unavailable")
	}
	return named.Create(agentID, name)
}

// DeleteMessageToken revokes a named message-API token by id. Returns true if a
// token was removed.
func (al *AgentLoop) DeleteMessageToken(agentID, id string) bool {
	al.mu.RLock()
	named := al.namedTokens
	al.mu.RUnlock()
	if named == nil {
		return false
	}
	return named.Delete(agentID, id)
}

// MessageTokenQuota returns the per-token rate-limit status for an agent's named
// message-API tokens.
func (al *AgentLoop) MessageTokenQuota(agentID string) []msgtoken.TokenQuota {
	al.mu.RLock()
	named := al.namedTokens
	al.mu.RUnlock()
	if named == nil {
		return nil
	}
	return named.Quota(agentID)
}

// ResetMessageTokenBlocks clears active rate-limit blocks for an agent's tokens.
// An empty name clears all; a name clears just that token. Returns the count
// cleared.
func (al *AgentLoop) ResetMessageTokenBlocks(agentID, name string) int {
	al.mu.RLock()
	named := al.namedTokens
	al.mu.RUnlock()
	if named == nil {
		return 0
	}
	return named.ResetBlocks(agentID, name)
}

// UpdateMessageToken sets a named token's rate/block config and persists it.
// Returns true when the token exists.
func (al *AgentLoop) UpdateMessageToken(agentID, id string, ratePerMin, blockMinutes int) bool {
	al.mu.RLock()
	named := al.namedTokens
	al.mu.RUnlock()
	if named == nil {
		return false
	}
	return named.Update(agentID, id, ratePerMin, blockMinutes)
}

// ErrNoDefaultChannel is returned (wrapped) by HandleExternalMessage when the
// target agent has no default channel binding to deliver an external event to.
// Callers can errors.Is against it to report a precondition failure (4xx).
var ErrNoDefaultChannel = errors.New("agent has no default channel")

// HandleExternalMessage delivers a raw external-message body to an agent as an
// unsolicited event — the same way a cron job fires (see schedule.ExecuteJob). It
// does NOT continue any existing conversation: it resolves the agent's default
// channel via CronTarget and publishes an inbound message there, so a monitoring
// webhook / GPS tracker / alarm reaches the agent even with no active chat.
//
// Returns ErrNoDefaultChannel (wrapped) when the agent has no default channel
// binding, so the HTTP layer can report a precondition (4xx) rather than a 500.
//
// The body is wrapped with the security prefix (so the model treats external
// input with caution) but the raw text is preserved intact. Delivery mirrors cron
// exactly: same CronTarget resolution and a fixed SenderID so downstream routing
// and dedupe treat it as a system-originated event.
func (al *AgentLoop) HandleExternalMessage(_ context.Context, agentID, body string) error {
	cfg := al.GetConfig()
	if cfg == nil {
		return fmt.Errorf("configuration not loaded")
	}
	channel, chatID, peerKind, ok := cfg.CronTarget(agentID)
	if !ok {
		return fmt.Errorf("%w: agent %q — configure a default channel binding for it", ErrNoDefaultChannel, agentID)
	}

	prefix := global.DefaultMessagePrefix
	if cfg.Security.MessagePrefix != "" {
		prefix = cfg.Security.MessagePrefix
	}

	msg := bus.InboundMessage{
		Channel:  channel,
		SenderID: "webhook",
		ChatID:   chatID,
		Content:  prefix + body,
		Peer:     bus.Peer{Kind: peerKind, ID: chatID},
	}

	// Publish on a fresh bounded context (not the request context) so a client
	// that hangs up right after POSTing does not abort delivery — matching cron.
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pubCancel()
	return al.bus.PublishInbound(pubCtx, msg)
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

func (al *AgentLoop) SetChannelManager(cm *channels.Manager) {
	al.channelManager = cm
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

// buildMessageManagers constructs per-agent message-token managers from cfg and
// wires each onto its agent's ContextBuilder. An agent whose message-token window is
// not > 0 gets no manager — no token is ever issued, injected, or validated for
// it. Call this on initial construction AND on every config reload so the
// managers (and the ContextBuilders' wiring) track the CURRENT config; otherwise
// a reloaded agent gets no token injected, and a now-disabled agent's old token
// would keep validating against a stale manager.
func buildMessageManagers(registry *AgentRegistry, cfg *config.Config) map[string]*msgtoken.Manager {
	managers := make(map[string]*msgtoken.Manager)
	for _, agentID := range registry.ListAgentIDs() {
		agentInstance, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}
		// Find the matching AgentConfig for callback settings.
		var agentCfg *config.AgentConfig
		for i := range cfg.Agents.List {
			if routing.NormalizeAgentID(cfg.Agents.List[i].ID) == agentID {
				agentCfg = &cfg.Agents.List[i]
				break
			}
		}
		storePath := filepath.Join(agentInstance.Workspace, "state", "message-tokens.json")
		windowMinutes, windowCount := 0, 0
		if agentCfg != nil && agentCfg.Message != nil && agentCfg.Message.WindowMinutes > 0 {
			windowMinutes = agentCfg.Message.WindowMinutes
			windowCount = agentCfg.Message.WindowCount
		}
		mgr, err := msgtoken.NewManager(agentID, storePath, windowMinutes, windowCount)
		if err != nil {
			logger.WarnCF("callback", "Failed to initialize callback manager",
				map[string]any{"agent": agentID, "error": err.Error()})
			continue
		}
		if mgr != nil {
			managers[agentID] = mgr
			logger.InfoCF("callback", "callbacks ENABLED for agent",
				map[string]any{"agent": agentID, "window_minutes": windowMinutes, "window_count": windowCount})
		} else {
			logger.InfoCF("callback", "callbacks disabled for agent (no token will be issued or injected)",
				map[string]any{"agent": agentID})
		}
	}
	return managers
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

var audioAnnotationRe = regexp.MustCompile(`\[(voice|audio)(?::[^\]]*)?\]`)

// transcribeAudioInMessage resolves audio media refs, transcribes them, and
// replaces audio annotations in msg.Content with the transcribed text.
// Returns the (possibly modified) message and true if audio was transcribed.
func (al *AgentLoop) transcribeAudioInMessage(ctx context.Context, msg bus.InboundMessage) (bus.InboundMessage, bool) {
	if al.transcriber == nil || al.mediaStore == nil || len(msg.Media) == 0 {
		return msg, false
	}

	// Transcribe each audio media ref in order. Non-audio refs (and audio we can't
	// resolve) pass through in kept for normal attachment handling; transcribed
	// audio is dropped from Media so it isn't also materialized as a raw file the
	// model can't use (it becomes a "[voice: ...]" transcript in the content).
	var transcriptions []string
	kept := make([]string, 0, len(msg.Media))
	for _, ref := range msg.Media {
		path, meta, err := al.mediaStore.ResolveWithMeta(ref)
		if err != nil {
			logger.WarnCF("voice", "Failed to resolve media ref", map[string]any{"ref": ref, "error": err})
			kept = append(kept, ref)
			continue
		}
		if !utils.IsAudioFile(meta.Filename, meta.ContentType) {
			kept = append(kept, ref)
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
	msg.Media = kept

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

var receivedFileNameUnsafeRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// materializeInboundMedia copies any media attached to an inbound message into a
// "tmp" folder inside the agent's workspace, so the agent's (workspace-sandboxed)
// file tools can actually read what the user sent. Returns a note to append to the
// user message naming the workspace-relative paths, or "" if nothing was written.
// The MediaStore copies still exist (with their own TTL cleanup); these workspace
// copies are intentionally left for the agent to use.
func (al *AgentLoop) materializeInboundMedia(msg bus.InboundMessage, agent *AgentInstance) string {
	if al.mediaStore == nil || len(msg.Media) == 0 || agent == nil || agent.Workspace == "" {
		return ""
	}
	tmpDir := filepath.Join(agent.Workspace, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		logger.WarnCF("agent", "received media: create tmp dir failed", map[string]any{"error": err.Error()})
		return ""
	}
	// Bound growth: drop attachments from earlier turns before adding this turn's.
	sweepReceivedMedia(tmpDir, time.Now())
	var rels []string
	for _, ref := range msg.Media {
		src, meta, err := al.mediaStore.ResolveWithMeta(ref)
		if err != nil {
			continue
		}
		name := receivedFileName(ref, meta.Filename)
		if err := copyFileContents(src, filepath.Join(tmpDir, name)); err != nil {
			logger.WarnCF("agent", "received media: copy failed", map[string]any{"ref": ref, "error": err.Error()})
			continue
		}
		rels = append(rels, "tmp/"+name)
	}
	if len(rels) == 0 {
		return ""
	}
	return "\n\n[Received attachment(s) saved in your workspace: " + strings.Join(rels, ", ") + "]"
}

// receivedMediaTTL bounds how long materialized inbound attachments linger in
// <workspace>/tmp. They are swept on the next materialize for that agent.
const receivedMediaTTL = 24 * time.Hour

// sweepReceivedMedia removes files in dir older than receivedMediaTTL so the
// received-media tmp dir does not grow without bound. Best-effort; errors ignored.
func sweepReceivedMedia(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > receivedMediaTTL {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// receivedFileName builds a collision-resistant, sandbox-safe filename from a media
// ref ("media://<uuid>") and its original filename.
func receivedFileName(ref, filename string) string {
	id := strings.TrimPrefix(ref, "media://")
	if i := strings.IndexByte(id, '-'); i > 0 {
		id = id[:i] // first uuid segment is enough to disambiguate
	}
	base := filepath.Base(strings.TrimSpace(filename))
	base = receivedFileNameUnsafeRe.ReplaceAllString(base, "_")
	base = strings.Trim(base, "._")
	if base == "" {
		base = "file"
	}
	if id == "" {
		return base
	}
	return id + "_" + base
}

// copyFileContents streams src to dst, creating/truncating dst.
func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
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

// processMessageSafely runs processMessage with a panic guard so a bug in the
// turn (or a tool) is converted into a returned error and delivered to the user
// instead of crashing the per-message goroutine and leaving the typing indicator
// stuck. processSessionMessage runs us in our own goroutine, so an unrecovered
// panic here would otherwise take down the process.
func (al *AgentLoop) processMessageSafely(ctx context.Context, msg bus.InboundMessage) (resp string, err error) {
	defer func() {
		if r := recover(); r != nil {
			logger.ErrorCF("agent", "panic while processing message",
				map[string]any{
					"channel": msg.Channel,
					"chat_id": msg.ChatID,
					"panic":   fmt.Sprintf("%v", r),
					"stack":   string(debug.Stack()),
				})
			resp = ""
			err = fmt.Errorf("internal error while processing the message: %v", r)
		}
	}()
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

	userContent := prependSenderLabel(msg.Content, msg.Sender)
	// Drop received attachments into the agent's workspace so its file tools can read
	// them (the model otherwise only sees an annotation it can't open).
	userContent += al.materializeInboundMedia(msg, agent)

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
		IsGroup:         inboundMetadata(msg, "is_group") == "true",
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

	agent, sessionKey := al.resolveSystemMessageTarget(msg)
	if agent == nil {
		return "", fmt.Errorf("no agent available for system message")
	}

	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         originChannel,
		ChatID:          originChatID,
		UserMessage:     fmt.Sprintf("[System: %s] %s", msg.SenderID, msg.Content),
		DefaultResponse: "Background task completed.",
		SendResponse:    true,
	})
}

// resolveSystemMessageTarget picks the agent and session a "system" message
// (e.g. an async tool/sub-agent completion) should be processed in. A spawn or
// other async tool carries the originating agent on preresolved_agent_id and its
// session on session_key, so the completion is handled by the SPAWNING agent in
// its own session — not whichever agent happens to be the default. Falls back to
// the default agent and that agent's main session when no originator is given
// (legacy behavior). Returns (nil, "") when no agent is available.
func (al *AgentLoop) resolveSystemMessageTarget(msg bus.InboundMessage) (*AgentInstance, string) {
	var agent *AgentInstance
	if preresolved := inboundMetadata(msg, metadataKeyPreresolvedAgentID); preresolved != "" {
		if a, ok := al.GetRegistry().GetAgent(routing.NormalizeAgentID(preresolved)); ok && a != nil {
			agent = a
		}
	}
	if agent == nil {
		agent = al.GetRegistry().GetDefaultAgent()
	}
	if agent == nil {
		return nil, ""
	}

	// Prefer the originator's session (so the completion lands in the conversation
	// that spawned the work); otherwise the agent's main session.
	sessionKey := strings.TrimSpace(msg.SessionKey)
	if sessionKey == "" || !strings.HasPrefix(sessionKey, sessionKeyAgentPrefix) {
		sessionKey = routing.BuildAgentMainSessionKey(agent.ID)
	}
	return agent, sessionKey
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
			// Flow B: text-only primary + vision side-model configured → describe
			// the inbound image(s) once here and fold the description into the
			// message text (clearing Media), so every dispatch iteration carries
			// usable text instead of an image the model can't see. No-op for
			// vision-capable primaries and when vision is not configured.
			al.describeInboundMedia(ctx, agent, &userMsg)
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
	finalContent, normal, degenerate, finishReason, iteration, err := al.runLLMIteration(ctx, agent, messages, opts, cm)
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
	return fmt.Sprintf("[Context: evicted %d read(s), freed %s — %s]",
		len(events), humanBytes(totalBytes), strings.Join(parts, ", "))
}

func humanBytes(n int) string {
	if n >= 1024 {
		return fmt.Sprintf("%.0f KB", float64(n)/1024.0)
	}
	return fmt.Sprintf("%d B", n)
}

func capEvictResource(s string) string {
	const max = 64
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
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

func (al *AgentLoop) runLLMIteration(
	ctx context.Context,
	agent *AgentInstance,
	messages []providers.Message,
	opts processOptions,
	cm llmcontext.ContextManager,
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

		// Per-turn context eviction sweep (LLM-free): collapse stale, superseded,
		// or oversized re-retrievable tool results (file reads, web fetches) in the
		// live window to a placeholder. Runs before PreDispatchCheck so cheap
		// eviction relieves window pressure first and summarization compaction
		// fires far less often. Every eviction is DEBUG-logged inside the sweep;
		// when the agent's policy has notify_user on, a one-line notice is also
		// surfaced in the conversation.
		if events := cm.SweepEvictions(ctx); len(events) > 0 {
			if rebuilt, berr := cm.Build(ctx); berr == nil {
				messages = rebuilt
			}
			evictedThisTurn = append(evictedThisTurn, events...)
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
						msgs := al.messagesForModel(messages, modelKey)
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
					al.fallbackNotifier(opts),
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
			return runProvider.Chat(ctx, al.messagesForModel(messages, runModel), providerToolDefs, runModel, llmOpts)
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

		// Per-tool budget: a single tool call cannot run longer than this. The
		// tool's context is cancelled at the deadline so a well-behaved tool
		// returns a timeout error and the model can continue the turn. The overall
		// turn budget (applied to ctx upstream) is the hard backstop for any tool
		// that ignores cancellation.
		toolTimeout := al.GetConfig().Agents.Defaults.GetToolTimeout()

		for i, tc := range normalizedToolCalls {
			agentResults[i].tc = tc

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
			if err := cm.AddToolResult(ctx, toolResultMsg); err != nil {
				logger.WarnCF("agent", "AddToolResult failed", map[string]any{
					"agent_id": agent.ID,
					"error":    err.Error(),
				})
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
			if err := cm.AddUserMessage(ctx, imgMsg); err != nil {
				logger.WarnCF("agent", "failed to persist tool image message",
					map[string]any{"agent_id": agent.ID, "error": err.Error()})
			}
			logger.InfoCF("agent", "passed tool image(s) to vision model (user message)",
				map[string]any{"agent_id": agent.ID, "model": activeModel, "images": len(toolImages)})
		}

		// Flow A: the active model is text-only and a vision side-model is
		// configured — describe the tool image(s) that would otherwise be dropped
		// and inject the description as a follow-up user turn so the model can use
		// it. Gated on VisionClients so an unconfigured deployment keeps today's
		// silent-drop behavior.
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
			if err := cm.AddUserMessage(ctx, descMsg); err != nil {
				logger.WarnCF("agent", "failed to persist vision-describe message",
					map[string]any{"agent_id": agent.ID, "error": err.Error()})
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

// getExposeReasoning returns whether the session exposes the model's reasoning
// to the user, loading it from the session store on a cache miss. Default false.
func (al *AgentLoop) getExposeReasoning(agent *AgentInstance, sessionKey string) bool {
	key := activeModelCacheKey(agent.ID, sessionKey)

	al.exposeReasoningMu.Lock()
	if v, ok := al.exposeReasoningCache[key]; ok {
		al.exposeReasoningMu.Unlock()
		return v
	}
	al.exposeReasoningMu.Unlock()

	v := false
	if store, ok := agent.Sessions.(compactionStateStore); ok {
		if st, err := store.GetCompactionState(sessionKey); err == nil {
			v = st.ExposeReasoning
		}
	}

	al.exposeReasoningMu.Lock()
	al.exposeReasoningCache[key] = v
	al.exposeReasoningMu.Unlock()
	return v
}

// setExposeReasoning sets the session's expose-reasoning flag, updating the cache
// and persisting it through CompactionState (best-effort).
func (al *AgentLoop) setExposeReasoning(agent *AgentInstance, sessionKey string, v bool) {
	key := activeModelCacheKey(agent.ID, sessionKey)
	al.exposeReasoningMu.Lock()
	al.exposeReasoningCache[key] = v
	al.exposeReasoningMu.Unlock()

	if store, ok := agent.Sessions.(compactionStateStore); ok {
		st, err := store.GetCompactionState(sessionKey)
		if err != nil {
			logger.WarnCF("agent", "expose reasoning: load compaction state failed",
				map[string]any{"session_key": sessionKey, "error": err.Error()})
			return
		}
		st.ExposeReasoning = v
		if err := store.SetCompactionState(sessionKey, st); err != nil {
			logger.WarnCF("agent", "expose reasoning: persist failed",
				map[string]any{"session_key": sessionKey, "error": err.Error()})
		}
	}
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
func (al *AgentLoop) messagesForModel(messages []providers.Message, modelKey string) []providers.Message {
	if mc, err := al.GetConfig().GetModelConfig(modelKey); err == nil && mc != nil {
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
		out[i].Media = nil
	}
	return out
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

// cogmemSessionStatus renders a short cognitive-memory summary for the session:
// active domain/memory counts, the pending-review count, and the last
// consolidation run. Returns "" when the agent is not cognitive. Opening the
// store runs the normal idempotent migrations, matching the cogmem_status tool.
func (al *AgentLoop) cogmemSessionStatus(agent *AgentInstance, sessionKey string) string {
	if agent == nil || agent.Config == nil || !agent.Config.CognitiveMemoryEnabled() {
		return ""
	}
	path := cogmemstore.SessionDBPath(agent.Workspace, sessionKey)
	if _, err := os.Stat(path); err != nil {
		return "No cognitive-memory database for this session yet."
	}
	s, err := cogmemstore.Open(path)
	if err != nil {
		return "Cognitive memory unavailable: " + err.Error()
	}
	defer s.Close()

	ctx := context.Background()
	db := s.DB()
	var b strings.Builder

	active, _ := s.ListDomains(ctx, db, cogmemstore.StatusActive)
	memCount := 0
	for _, d := range active {
		ms, _ := s.ListMemories(ctx, db, d.ID, cogmemstore.StatusActive)
		memCount += len(ms)
	}
	fmt.Fprintf(&b, "Active domains: %d\n", len(active))
	fmt.Fprintf(&b, "Active memories: %d\n", memCount)

	if pending, perr := s.PendingCount(ctx, db); perr == nil {
		fmt.Fprintf(&b, "Pending (review): %d\n", pending)
	}

	run, ok, _ := s.LastRun(ctx, db)
	if !ok {
		b.WriteString("Last consolidation: none yet")
	} else {
		when := run.StartedAt.Format("2006-01-02 15:04")
		fmt.Fprintf(&b, "Last consolidation: %s — trigger=%s, status=%s, changes=%d",
			when, run.Trigger, run.Status, run.OpsApplied)
		if run.Error != "" {
			fmt.Fprintf(&b, "\nLast error: %s", run.Error)
		}
	}
	return strings.TrimRight(b.String(), "\n")
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
		GetMemoryStatus: func() string {
			if agent == nil || opts == nil {
				return ""
			}
			return al.cogmemSessionStatus(agent, opts.SessionKey)
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
		rt.GetContextWindow = func() int { return agent.ContextWindow }
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

		rt.GetExposeReasoning = func() bool {
			if opts == nil {
				return false
			}
			return al.getExposeReasoning(agent, opts.SessionKey)
		}
		rt.SetExposeReasoning = func(on bool) {
			if opts == nil {
				return
			}
			al.setExposeReasoning(agent, opts.SessionKey, on)
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
		// Token-quota hooks scope to THIS agent by closing over its normalized id
		// (the same id the named-token store and message API key on).
		quotaAgentID := routing.NormalizeAgentID(agent.ID)
		rt.ListTokenQuota = func() []commands.TokenQuotaEntry {
			snap := al.MessageTokenQuota(quotaAgentID)
			out := make([]commands.TokenQuotaEntry, 0, len(snap))
			for _, q := range snap {
				out = append(out, commands.TokenQuotaEntry{
					Name:           q.Name,
					RatePerMin:     q.RatePerMin,
					BlockMinutes:   q.BlockMinutes,
					HitsInWindow:   q.HitsInWindow,
					Blocked:        q.Blocked,
					BlockRemaining: q.BlockRemaining,
				})
			}
			return out
		}
		rt.ResetTokenQuota = func(name string) int {
			return al.ResetMessageTokenBlocks(quotaAgentID, name)
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

// prependSenderLabel attributes a message with its sender ("[From: <label>]").
// Applied to every message, direct chats included: under the default unified
// session scope, direct and group messages share one session, so a "private"
// message is just one of several senders in the shared context — the label keeps
// attribution unambiguous. Cheap, and the clarity is worth the few tokens. No-op
// when the sender yields no label.
func prependSenderLabel(content string, s bus.SenderInfo) string {
	label := senderLabel(s)
	if label == "" {
		return content
	}
	return "[From: " + label + "]\n" + content
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
