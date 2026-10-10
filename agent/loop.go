// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/cogmem/consolidate"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/alerts"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
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
	bus      *bus.MessageBus
	cfg      *config.Config
	registry *AgentRegistry
	running  atomic.Bool
	// started is closed once Run has begun serving (see Started).
	started         chan struct{}
	startedOnce     sync.Once
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
	cooldown *providers.CooldownTracker
	// alerter receives operator alerts (parked models, dead MCP servers, …);
	// nil until SetAlerter, in which case Alerter() hands out a Nop.
	alerterMu       sync.RWMutex
	alerter         alerter.Alerter
	messageManagers map[string]*msgtoken.Manager // agentID -> manager (nil entry means disabled)
	// namedTokens holds the long-lived, user-named message-API tokens (one store
	// for all agents, persisted under internal/message-api-tokens.json). It is
	// separate from the rotating messageManagers: named tokens never expire and
	// are minted/revoked from the WebUI. The same instance is shared with the API
	// handler so a mint/revoke is visible to ValidateMessageToken immediately.
	namedTokens *msgtoken.NamedStore
	// agentStates holds the restart-recovery state managers of config
	// agents, by workspace (one manager per state file), made on first use so
	// an agent a reload adds gets one too. Guarded by agentStatesMu.
	agentStatesMu sync.Mutex
	agentStates   map[string]*state.Manager
	// sessions holds the per-session dispatch state (session scope key →
	// *sessionState): the goroutine running the session's turns, the messages
	// queued behind it and the /cancel bookkeeping. The scope key is resolved via
	// resolveMessageRoute before dispatch so that multiple channel:chatID pairs
	// that map to the same agent session share one entry and never process
	// concurrently. sessionsMu guards the map and every entry's refs/lastUsed;
	// pruneSessions drops entries idle for sessionIdleTTL with no holder.
	sessionsMu sync.Mutex
	sessions   map[string]*sessionState
	// turnSem bounds the turns running at once across all sessions
	// (agents.defaults.max_concurrent_turns); nil = unlimited.
	turnSem chan struct{}
	// spend sums dispatch cost per UTC day for the daily-spend alert.
	spend         dailySpend
	lastSelfClear sync.Map // session key → time.Time, rate-limits session_clear
	dumpsDir      string
	startedAt     time.Time

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
	// once via SetSessionTokenIssuer with the process-lifetime store every MCP
	// server shares; nil when the MCP host is not configured.
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
	// supervisor never relaunches a task that is still running. Each config
	// agent's current manager hangs off its instance (spawnMgr). superStop is
	// closed by Close() to stop the supervisor goroutine.
	taskLive  *toolsagents.LiveSet
	superStop chan struct{}

	// sweepWG tracks the temporary-agent sweeper, stopped by evictStop.
	sweepWG sync.WaitGroup

	// extraTools are the host-built tools added with RegisterTool, kept so a
	// temporary agent created later gets them too.
	extraToolsMu sync.Mutex
	extraTools   []tools.Tool

	// stopRun cancels the context Run derives for every turn it starts, with
	// errShuttingDown as the cause; set by Run, called by Stop. Guarded by
	// runMu.
	runMu   sync.Mutex
	stopRun context.CancelCauseFunc
	// runCtx is the context Run serves under (nil until Run), for background
	// work that must stop with the service (/ask). Guarded by runMu.
	runCtx context.Context

	// asks are the asks (Ask) waiting for their reply; whispers the messages
	// held for each agent's next message (Whisper).
	asks     askRegistry
	whispers whisperStore
	waits    waitGraph

	// humans holds the requests waiting for a person's answer (human agents).
	humans humanDesk
	// dismisser clears a chat's indicators for a person's answer, which gets
	// no reply of its own: the channel manager (SetChannelManager); tests
	// substitute a fake.
	dismisser inboundDismisser
}

// errShuttingDown is the cause Stop gives the turn context. A turn ended by it
// is an interrupted turn: no reply is sent and its pending-turn flag is kept,
// so it is replayed when the gateway starts again.
var errShuttingDown = errors.New("the service is shutting down")

// stoppedOnPurpose reports whether ctx was cancelled on purpose: by /cancel,
// or because the asker of the turn stopped waiting for it.
func stoppedOnPurpose(ctx context.Context) bool {
	cause := context.Cause(ctx)
	return errors.Is(cause, errCancelledByUser) || errors.Is(cause, errAskerStopped)
}

// shuttingDown reports whether ctx was cancelled by Stop.
func shuttingDown(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errShuttingDown)
}

// SessionTokenIssuer issues and revokes SST-prefixed session tokens used by
// session-scoped MCP tools (get_session_messages, search_session_messages).
// mcpserver.SessionTokenStore satisfies this interface.
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
	// SetDepth records the sub-agent depth of the turn starting on sessionKey,
	// so MCP tool calls made with the session's token (CLI providers) run at
	// it. No-op when the sessionKey is unknown.
	SetDepth(sessionKey string, depth int)
	// SetTurnScope records the ask chain of the turn starting on sessionKey
	// (see tools.AskChain), so MCP tool calls made with the session's token
	// carry it. No-op when the sessionKey is unknown.
	SetTurnScope(sessionKey string, askChain []string)
	// Source returns the source SetSource last recorded for sessionKey (empty
	// when none or unknown).
	Source(sessionKey string) (channel, chatID string)
}

// publishTimeout bounds handing one message to the bus, so a full queue
// delays a turn or a background delivery by at most this much.
const publishTimeout = 5 * time.Second

const (
	defaultResponse               = "I've completed processing but have no response to give. Increase `max_tool_iterations` in config.json."
	metadataKeyAccountID          = "account_id"
	metadataKeyGuildID            = "guild_id"
	metadataKeyTeamID             = "team_id"
	metadataKeyParentPeerKind     = "parent_peer_kind"
	metadataKeyParentPeerID       = "parent_peer_id"
	metadataKeyPreresolvedAgentID = "preresolved_agent_id"
)

// LoopOption configures NewAgentLoop.
type LoopOption func(*loopOptions)

type loopOptions struct {
	ownsDataDir bool
}

// OwnsDataDir marks the loop as the data directory's owner: the process that
// holds claw.lock (the gateway). Only the owner restores the saved temporary
// agents at start, removes stale temporary-agent directories and writes
// internal/temp_agents.json. Without it (`claw agent` running beside the
// service) the loop's temporary agents are kept in memory only and the
// service's are left alone.
func OwnsDataDir() LoopOption { return func(o *loopOptions) { o.ownsDataDir = true } }

func NewAgentLoop(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	dispatcher *providers.ProviderDispatcher,
	opts ...LoopOption,
) (*AgentLoop, error) {
	var lo loopOptions
	for _, opt := range opts {
		opt(&lo)
	}
	// Route the context engine's logs into ours before anything constructs a
	// session store or context manager.
	InstallLogging()

	// Set up shared fallback chain with the config-driven cooldown policy.
	cooldown := providers.NewCooldownTrackerWithPolicy(cooldownPolicy(cfg))
	fallbackChain := providers.NewFallbackChain(cooldown)

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
		alerts.Send(alerter.Alert{
			Title:       "Named message-token store unreadable",
			Description: namedTokenPath + ": named tokens do not work, and the next change overwrites the file",
			Details:     err.Error(),
			EventID:     "msgtoken:named",
		})
		namedTokens, err = msgtoken.NewNamedStore("")
		if err != nil {
			logger.ErrorCF("message", "Failed to create empty named message-token store",
				map[string]any{"error": err.Error()})
		}
	}

	al := &AgentLoop{
		bus:                   msgBus,
		cfg:                   cfg,
		fallback:              fallbackChain,
		cooldown:              cooldown,
		cmdRegistry:           commands.NewRegistry(commands.BuiltinDefinitions()),
		dispatcher:            dispatcher,
		agentStates:           make(map[string]*state.Manager),
		namedTokens:           namedTokens,
		sessions:              make(map[string]*sessionState),
		startedAt:             time.Now(),
		evictStop:             make(chan struct{}),
		started:               make(chan struct{}),
		mcpRetryStop:          make(chan struct{}),
		evictTTL:              defaultEvictTTL,
		evictInterval:         defaultEvictInterval,
		activeModelIdx:        make(map[string]int),
		exposeReasoningCache:  make(map[string]bool),
		showToolActivityCache: make(map[string]bool),
		taskLive:              toolsagents.NewLiveSet(),
		superStop:             make(chan struct{}),
	}
	if n := cfg.Agents.Defaults.MaxConcurrentTurns; n > 0 {
		al.turnSem = make(chan struct{}, n)
	}

	// Build the registry: every config agent, then the temporary agents saved
	// by the previous run. The builder registers each agent's tools (session
	// closures, spawn/subagent, msg with shared MessageTool), so it runs once
	// al exists.
	registry, err := agentreg.New(cfg, agentreg.Hooks[*AgentInstance]{
		Build:    al.agentBuilder(provider, dispatcher, fallbackChain),
		Retire:   al.retireAgent,
		Inserted: al.agentInserted,
		Owner:    lo.ownsDataDir,
	})
	if err != nil {
		return nil, err
	}
	al.registry = registry

	// Per-agent state managers and message-token managers: config agents only.
	for _, agentID := range registry.List() {
		al.stateManager(agentID)
	}
	al.messageManagers = buildMessageManagers(registry, cfg)

	return al, nil
}

func (al *AgentLoop) Run(ctx context.Context) error {
	al.running.Store(true)
	al.startedOnce.Do(func() {
		if al.started != nil {
			close(al.started)
		}
	})

	// Every turn runs under this context, so Stop can abort the model requests,
	// CLI subprocesses and tool calls still in flight.
	ctx, stopRun := context.WithCancelCause(ctx)
	defer stopRun(nil)
	al.runMu.Lock()
	al.stopRun = stopRun
	al.runCtx = ctx
	al.runMu.Unlock()

	if err := al.ensureMCPInitialized(ctx); err != nil {
		return err
	}

	// Start the background context-manager eviction goroutine.
	go al.evictContextManagers() //nolint:contextcheck // idle eviction closes managers on a fresh context so the archive flush completes regardless of the run context

	// Drop dispatch state for sessions that have been idle for an hour.
	go al.pruneSessions()

	// Delete temporary agents idle past their TTL (and any the configuration
	// can no longer build). One pass now catches those that expired while the
	// process was down.
	al.registry.Sweep(time.Now())
	al.sweepWG.Go(func() { al.registry.RunSweeper(al.evictStop, agentreg.SweepInterval) })

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
			al.dispatchInbound(ctx, msg)
		}
	}

	return nil
}

// Started is closed once Run has begun: from then on asks are accepted
// (the forum resumes its forums only after that).
func (al *AgentLoop) Started() <-chan struct{} { return al.started }

// runContext is the context Run serves under, or nil before Run.
func (al *AgentLoop) runContext() context.Context {
	al.runMu.Lock()
	defer al.runMu.Unlock()
	return al.runCtx
}

// dispatchInbound hands one inbound message to its session goroutine. A
// person's answer to a waiting request is taken here instead, in arrival
// order, before any goroutine could race it (human agents); a message that
// finds nothing waiting here is never retried as an answer later.
func (al *AgentLoop) dispatchInbound(ctx context.Context, msg bus.InboundMessage) {
	if al.takeHumanAnswer(ctx, msg) {
		return
	}
	al.activeRequests.Add(1)
	go al.processSessionMessage(ctx, msg)
}

// Stop ends Run and cancels the turns in flight. A cancelled turn is left
// pending, so it is replayed on the next start.
func (al *AgentLoop) Stop() {
	al.running.Store(false)
	al.runMu.Lock()
	stopRun := al.stopRun
	al.runMu.Unlock()
	if stopRun != nil {
		stopRun(errShuttingDown)
	}
}

// WaitTurns blocks until every turn and model request the loop started has
// returned, or until ctx ends, whose error it then returns. Call it after Stop
// and after Run has returned: a turn Stop cancelled may still be unwinding
// and writing to its session.
func (al *AgentLoop) WaitTurns(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		al.activeRequests.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// BeginMCPShutdown stops the MCP liveness probes and refuses reconnects, so
// servers the stop signal killed are not respawned while the gateway shuts
// down. The first step of a shutdown; Close closes the servers.
func (al *AgentLoop) BeginMCPShutdown() {
	if mgr := al.mcp.peekManager(); mgr != nil {
		mgr.BeginShutdown()
	}
}

// Close releases resources held by agent session stores. Call after Stop.
// Closing the sessions and the MCP servers shares ctx as their time budget:
// what is still busy when it ends is logged and abandoned, since the process
// is exiting.
func (al *AgentLoop) Close(ctx context.Context) {
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
	start := time.Now()
	al.drainContextManagers(ctx)
	logger.Infof("Shutdown: sessions closed in %.1fs", time.Since(start).Seconds())

	if mcpManager := al.mcp.takeManager(); mcpManager != nil {
		start = time.Now()
		if err := mcpManager.Close(ctx); err != nil {
			logger.ErrorCF("agent", "Failed to close MCP manager",
				map[string]any{
					"error": err.Error(),
				})
		}
		logger.Infof("Shutdown: MCP servers closed in %.1fs", time.Since(start).Seconds())
	}

	for _, mgr := range al.messageManagers {
		mgr.Stop()
	}

	// The sweeper stopped with evictStop; wait for a sweep in progress so it
	// cannot close an agent the registry closes next. Close runs once.
	al.sweepWG.Wait()
	al.GetRegistry().Close()
}

// SetSessionTokenIssuer wires the MCP session token store into the
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
	if cm != nil {
		al.dismisser = cm
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
		return errors.New("provider cannot be nil")
	}
	if cfg == nil {
		return errors.New("config cannot be nil")
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before registry creation: %w", err)
	}

	registry := al.GetRegistry()
	oldProvider, hadProvider := extractProvider(registry)

	// The new fallback chain is built here so it is shared between the tools the
	// rebuilt agents register and the swapped-in al.fallback. Reuse the existing
	// cooldown tracker so its state (notably out-of-credits parks) PERSISTS
	// across the reload; only refresh its policy from the new config. The shared
	// tracker continues to back both the new fallback chain and the rebuilt
	// compaction managers.
	al.cooldown.SetPolicy(cooldownPolicy(cfg))
	newFallback := providers.NewFallbackChain(al.cooldown)

	if err := al.reloadRegistry(ctx, registry, provider, cfg, newFallback); err != nil {
		return err
	}

	// Whispers held for an agent the new configuration removed are dropped;
	// rebuilt agents keep theirs.
	al.whispers.prune(func(id string) bool {
		_, ok := registry.Get(id)
		return ok
	})
	al.replaceMessageManagers(registry, cfg)

	// Flush the dispatcher cache so stale providers are evicted on config reload.
	if al.dispatcher != nil {
		al.dispatcher.Flush(cfg)
	}

	// Have cached ContextManagers rebuilt from the new config, since per-session
	// config is baked in at creation (notably the summarization model chain).
	// Idle sessions are evicted now; sessions in use keep their manager and
	// session token until released, then rebuild on their next access.
	al.invalidateContextManagers(ctx)

	if hadProvider {
		al.closeReplacedProvider(ctx, oldProvider)
	}

	logger.InfoCF("agent", "Provider and config reloaded successfully",
		map[string]any{
			"model": cfg.Agents.Defaults.DefaultModelName(),
		})

	return nil
}

// reloadRegistry rebuilds every agent against the new config and provider
// (config agents from the config, temporary agents against it too), each
// with its tools registered the same way as at start. Nothing changes until
// all are built; then the registry swaps them in one step and, in the same
// step, the config and fallback chain are swapped here, so readers see a
// consistent pair. A ctx that ends first returns at once: the rebuild is
// abandoned (the commit refuses) and closes what it built. Whichever of the
// commit and the abandonment comes first wins. Panics are recovered and
// reported as a failed reload.
func (al *AgentLoop) reloadRegistry(
	ctx context.Context,
	registry *AgentRegistry,
	provider providers.LLMProvider,
	cfg *config.Config,
	newFallback *providers.FallbackChain,
) error {
	const (
		reloadPending int32 = iota
		reloadCommitted
		reloadAbandoned
	)
	var outcome atomic.Int32
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic during registry creation: %v", r)
				logger.ErrorCF("agent", "Panic during registry creation",
					map[string]any{"panic": r})
			}
			done <- err
		}()
		err = registry.Reload(ctx, cfg, al.agentBuilder(provider, al.dispatcher, newFallback), func() bool {
			if !outcome.CompareAndSwap(reloadPending, reloadCommitted) {
				return false
			}
			al.mu.Lock()
			al.cfg = cfg
			al.fallback = newFallback
			al.mu.Unlock()
			return true
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("registry creation failed: %w", err)
		}
	case <-ctx.Done():
		if outcome.CompareAndSwap(reloadPending, reloadAbandoned) {
			return fmt.Errorf("context canceled during registry creation: %w", ctx.Err())
		}
		// The swap already happened: finish the reload.
		if err := <-done; err != nil {
			return fmt.Errorf("registry creation failed: %w", err)
		}
	}
	return nil
}

// replaceMessageManagers rebuilds the callback managers against the new
// registry and config, so tokens are no longer validated against the old
// config (e.g. an agent whose callbacks were since disabled), and stops the
// superseded ones. Their tokens persist on disk and are reloaded by the
// rebuilt manager for still-enabled agents.
func (al *AgentLoop) replaceMessageManagers(registry *AgentRegistry, cfg *config.Config) {
	newCallbackManagers := buildMessageManagers(registry, cfg)
	al.mu.Lock()
	oldCallbackManagers := al.messageManagers
	al.messageManagers = newCallbackManagers
	al.mu.Unlock()
	for _, mgr := range oldCallbackManagers {
		mgr.Stop()
	}
}

// closeReplacedProvider closes the provider the reload replaced, once the
// requests in flight on it have finished (at most 30 seconds, or until ctx
// ends). It runs after the swap, so readers are never blocked by it.
func (al *AgentLoop) closeReplacedProvider(ctx context.Context, oldProvider providers.LLMProvider) {
	stateful, ok := oldProvider.(providers.StatefulProvider)
	if !ok {
		return
	}
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
	for _, toolName := range mediaStoreTools {
		forEachTool(registry, toolName, func(t tools.Tool) {
			if sf, ok := t.(mediaStoreSetter); ok {
				sf.SetMediaStore(s)
			}
		})
	}
}

// mediaStoreSetter is a tool that sends files and needs the media store.
type mediaStoreSetter interface {
	SetMediaStore(store media.MediaStore)
}

// mediaStoreTools are the tools SetMediaStore wires.
var mediaStoreTools = []string{"send_file", "msg_send_file"}

// applyMediaStore gives one agent's file-sending tools the media store, for an
// agent built after SetMediaStore ran. No-op before it runs.
func (al *AgentLoop) applyMediaStore(agent *AgentInstance) {
	if al.mediaStore == nil {
		return
	}
	for _, toolName := range mediaStoreTools {
		if t, ok := agent.Tools.Get(toolName); ok {
			if sf, ok := t.(mediaStoreSetter); ok {
				sf.SetMediaStore(al.mediaStore)
			}
		}
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
	agent := registry.Default()
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
		"count": len(registry.List()),
		"ids":   registry.List(),
	}

	return info
}

// SetAlerter installs the operator alerter: on the cooldown tracker, and on
// anything created later that alerts (the MCP manager). Call once at startup.
func (al *AgentLoop) SetAlerter(a alerter.Alerter) {
	al.alerterMu.Lock()
	al.alerter = a
	al.alerterMu.Unlock()
	if al.cooldown != nil {
		al.cooldown.SetAlerter(a)
	}
}

// Alerter returns the installed alerter, or a Nop when none was installed, so
// callers never check for nil.
func (al *AgentLoop) Alerter() alerter.Alerter {
	al.alerterMu.RLock()
	defer al.alerterMu.RUnlock()
	if al.alerter == nil {
		return alerter.Nop{}
	}
	return al.alerter
}
