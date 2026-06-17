package gateway

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/pkg/agent"
	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/discord"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/line"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/matrix"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/slack"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/telegram"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/webui"
	"github.com/PivotLLM/ClawEh/pkg/cogmem/consolidate"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/cron"
	"github.com/PivotLLM/ClawEh/pkg/devices"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/health"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/mcpserver"
	"github.com/PivotLLM/ClawEh/pkg/media"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/state"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	toolschedule "github.com/PivotLLM/ClawEh/pkg/tools/schedule"
	"github.com/PivotLLM/ClawEh/pkg/voice"
	webserver "github.com/PivotLLM/ClawEh/web/backend"
	"github.com/PivotLLM/ClawEh/web/backend/launcherconfig"
)

// Timeout constants for service operations
const (
	serviceRestartTimeout   = 30 * time.Second
	serviceShutdownTimeout  = 30 * time.Second
	providerReloadTimeout   = 30 * time.Second
	gracefulShutdownTimeout = 15 * time.Second
)

// newMergedWebServer constructs the in-process WebUI server bundle (API
// handler + embedded SPA) configured against the active config file and the
// merged binary's actual listen settings. The gateway host/port from
// cfg.Gateway are the source of truth — the launcher process and its
// config-as-IPC channel are gone in the merged binary.
func newMergedWebServer(configPath string, cfg *config.Config) *webserver.Server {
	publicBind := cfg != nil && cfg.Gateway.Host == "0.0.0.0"
	port := 0
	if cfg != nil {
		port = cfg.Gateway.Port
	}
	if port == 0 {
		port = launcherconfig.DefaultPort
	}
	srv := webserver.New(webserver.Options{
		ConfigPath: configPath,
		ListenPort: port,
		Public:     publicBind,
	})
	if _, err := srv.APIHandler().EnsureWebUIChannel(); err != nil {
		logger.WarnCF("gateway", "Failed to ensure webui channel", map[string]any{"error": err.Error()})
	}
	return srv
}

// buildMergedMux constructs the shared mux that the gateway HTTP server backs.
// Order matters only for the catch-all "/" (embedded frontend) vs. specific
// API paths: Go's ServeMux picks the longest-prefix match, so registering the
// SPA fallback alongside more-specific /api/* and /webui/ws handlers is safe.
func buildMergedMux(srv *webserver.Server) *http.ServeMux {
	mux := http.NewServeMux()
	if srv != nil {
		srv.RegisterRoutes(mux)
	}
	return mux
}

// gatewayServices holds references to all running services
type gatewayServices struct {
	CronService    *cron.CronService
	MediaStore     media.MediaStore
	ChannelManager *channels.Manager
	DeviceService  *devices.Service
	HealthServer   *health.Server
	MCPServer      *mcpserver.MCPServer
	WebServer      *webserver.Server
	HTTPHost       *httpHost
	CogmemManager  *consolidate.Manager
}

func gatewayCmd(debug bool) error {
	// Acquire PID lock before connecting to any external service.
	// If another instance is already running this exits immediately with a clear error.
	baseDir := internal.GetClawHome()

	// Enable file logging as early as possible so startup, config load, prune
	// warnings, and any fatal error are captured in claw.log — not just on the
	// console / journal. The configured format/level are re-applied below once
	// the config is loaded.
	logPath := filepath.Join(baseDir, "logs", "claw.log")
	logger.SetErrorLogLevel(logger.ParseLevel(global.ErrorLogLevel))
	if err := logger.EnableFileLogging(logPath, false); err != nil {
		logger.WarnCF("gateway", "Failed to enable file logging", map[string]any{"path": logPath, "error": err.Error()})
	}

	lockFile, err := acquireLock(baseDir)
	if err != nil {
		return fmt.Errorf("startup aborted: %w", err)
	}
	defer releaseLock(lockFile)

	logger.InfoCF("gateway", "Starting", map[string]any{"app": global.AppName, "version": global.Version})

	configPath := internal.GetConfigPath()
	cfg, err := internal.LoadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	// Drop invalid providers/models (e.g. a stale/unknown protocol, or a model
	// pointing at a missing provider) with a WARN and continue on the survivors,
	// rather than failing startup over one bad entry. The on-disk config is left
	// untouched so it can be repaired via the WebUI.
	if dp, dm := cfg.PruneInvalid(); dp > 0 || dm > 0 {
		logger.WarnCF("gateway", "ignored invalid config entries; continuing with the rest", map[string]any{
			"providers_dropped": dp,
			"models_dropped":    dm,
		})
	}

	// Re-apply logging config (debug flag overrides level).
	if cfg.Logging.File {
		if err := logger.EnableFileLogging(logPath, cfg.Logging.JSON); err != nil {
			logger.WarnCF("gateway", "Failed to enable file logging", map[string]any{"path": logPath, "error": err.Error()})
		}
	} else {
		logger.DisableFileLogging()
	}
	if !cfg.Logging.Console {
		logger.DisableConsole()
	}
	if debug {
		logger.SetLevel(logger.DEBUG)
		logger.DebugC("gateway", "Debug mode enabled")
	} else if cfg.Logging.Level != "" {
		logger.SetLevel(logger.ParseLevel(cfg.Logging.Level))
	}
	logger.SetLogMessageContent(cfg.Logging.LogMessageContent)

	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		logger.WarnCF("gateway", "No model configured, starting in unconfigured state", map[string]any{"detail": err.Error()})
		provider = providers.NewUnconfiguredProvider()
		modelID = ""
	}

	// Use the resolved model ID from provider creation
	if modelID != "" {
		cfg.Agents.Defaults.SetDefaultModel(modelID)
	}

	registerToolProviders()

	// Default per-agent allowlist = every DefaultEnabled tool (single source of
	// truth; the MCP-host default below uses the same set).
	config.SetDefaultAgentTools(tools.DefaultEnabledToolNames())

	dispatcher := providers.NewProviderDispatcher(cfg)
	msgBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider, dispatcher)

	dumpsDir := filepath.Join(internal.GetClawHome(), "logs", "dumps")
	agentLoop.SetDumpsDir(dumpsDir)

	startupInfo := agentLoop.GetStartupInfo()
	if len(startupInfo) == 0 {
		return fmt.Errorf("no default agent configured — add at least one entry to agents.list in your config")
	}
	toolsInfo, _ := startupInfo["tools"].(map[string]any)
	skillsInfo, _ := startupInfo["skills"].(map[string]any)
	logger.InfoCF("agent", "Agent initialized",
		map[string]any{
			"tools_count":      toolsInfo["count"],
			"skills_total":     skillsInfo["total"],
			"skills_available": skillsInfo["available"],
		})

	// Setup and start all services
	services, err := setupAndStartServices(cfg, agentLoop, msgBus, configPath)
	if err != nil {
		return err
	}

	logger.InfoF("Gateway started", map[string]any{"addr": fmt.Sprintf("%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Daily log rotation: roll claw.log/error.log at local midnight (and on
	// startup if claw.log predates today), pruning archives past retention.
	startLogRotation(ctx, logPath, cfg.Logging.RetentionDays)

	go agentLoop.Run(ctx)

	// Setup config file watcher for hot reload
	reloadInterval := cfg.ConfigReloadInterval()
	logger.InfoF("Config reload watcher", map[string]any{"interval": reloadInterval.String()})
	configReloadChan, stopWatch := setupConfigWatcherPolling(configPath, reloadInterval,
		time.Duration(global.ConfigReloadDebounceSeconds)*time.Second, debug)
	defer stopWatch()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	// Main event loop - wait for signals or config changes
	for {
		select {
		case <-sigChan:
			logger.Info("Shutting down...")
			shutdownGateway(services, agentLoop, provider, true)
			return nil

		case newCfg := <-configReloadChan:
			err := handleConfigReload(ctx, agentLoop, newCfg, &provider, services, msgBus)
			if err != nil {
				logger.Errorf("Config reload failed: %v", err)
			}
		}
	}
}

// setupAndStartServices initializes and starts all services
func setupAndStartServices(
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
	msgBus *bus.MessageBus,
	configPath string,
) (*gatewayServices, error) {
	services := &gatewayServices{}

	// Ensure the shared "common" directory exists so the common_* tools have a
	// place to read/write. Idempotent.
	if commonDir := cfg.ResolveCommonDir(); commonDir != "" {
		if err := os.MkdirAll(commonDir, 0o755); err != nil {
			logger.WarnCF("gateway", "Failed to create common directory", map[string]any{"path": commonDir, "error": err.Error()})
		}
	}

	// Setup cron tool and service
	execTimeout := time.Duration(cfg.Tools.Cron.ExecTimeoutMinutes) * time.Minute
	var cronTool *toolschedule.CronTool
	services.CronService, cronTool = setupCronTool(
		agentLoop,
		msgBus,
		cfg.WorkspacePath(),
		cfg.Agents.Defaults.RestrictToWorkspace,
		execTimeout,
		cfg,
	)
	if cronTool != nil {
		agentLoop.RegisterTool(cronTool)
	}
	if err := services.CronService.Start(); err != nil {
		return nil, fmt.Errorf("error starting cron service: %w", err)
	}
	logger.InfoC("cron", "Cron service started")

	// Create media store for file lifecycle management with TTL cleanup
	services.MediaStore = media.NewFileMediaStoreWithCleanup(media.MediaCleanerConfig{
		Enabled:  cfg.Tools.MediaCleanup.Enabled,
		MaxAge:   time.Duration(cfg.Tools.MediaCleanup.MaxAge) * time.Minute,
		Interval: time.Duration(cfg.Tools.MediaCleanup.Interval) * time.Minute,
	})
	// Start the media store if it's a FileMediaStore with cleanup
	if fms, ok := services.MediaStore.(*media.FileMediaStore); ok {
		fms.Start()
	}

	// Create channel manager
	var err error
	services.ChannelManager, err = channels.NewManager(cfg, msgBus, services.MediaStore)
	if err != nil {
		// Stop the media store if it's a FileMediaStore with cleanup
		if fms, ok := services.MediaStore.(*media.FileMediaStore); ok {
			fms.Stop()
		}
		return nil, fmt.Errorf("error creating channel manager: %w", err)
	}

	// Inject channel manager and media store into agent loop
	agentLoop.SetChannelManager(services.ChannelManager)
	agentLoop.SetMediaStore(services.MediaStore)

	// Wire up voice transcription if a supported provider is configured.
	if transcriber := voice.DetectTranscriber(cfg); transcriber != nil {
		agentLoop.SetTranscriber(transcriber)
		logger.InfoCF("voice", "Transcription enabled (agent-level)", map[string]any{"provider": transcriber.Name()})
	}

	enabledChannels := services.ChannelManager.GetEnabledChannels()
	if len(enabledChannels) > 0 {
		logger.InfoCF("channels", "Channels enabled", map[string]any{"channels": enabledChannels})
	} else {
		logger.WarnC("channels", "No channels enabled")
	}

	// Setup shared HTTP listener. The listener is owned by httpHost and stays
	// up across config reloads; only the handler mux is swapped on reload, so
	// WebUI WebSocket connections and channel webhooks survive a Manager
	// rebuild. See rebuildSharedHTTPServer for the swap seam.
	addr := fmt.Sprintf("%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
	services.WebServer = newMergedWebServer(configPath, cfg)
	services.HTTPHost = newHTTPHost(addr)
	rebuildSharedHTTPServer(services, cfg.Gateway.Host, cfg.Gateway.Port, services.ChannelManager, services.HTTPHost, agentLoop)
	services.HTTPHost.Start()

	if err := services.ChannelManager.StartAll(context.Background()); err != nil {
		return nil, fmt.Errorf("error starting channels: %w", err)
	}

	logger.InfoF("Health endpoints available", map[string]any{"health": fmt.Sprintf("http://%s:%d/health", cfg.Gateway.Host, cfg.Gateway.Port), "ready": fmt.Sprintf("http://%s:%d/ready", cfg.Gateway.Host, cfg.Gateway.Port)})

	// Setup state manager and device service
	stateManager := state.NewManager(cfg.WorkspacePath())
	services.DeviceService = devices.NewService(devices.Config{
		Enabled:    cfg.Devices.Enabled,
		MonitorUSB: cfg.Devices.MonitorUSB,
	}, stateManager)
	services.DeviceService.SetBus(msgBus)
	if err := services.DeviceService.Start(context.Background()); err != nil {
		logger.ErrorCF("device", "Error starting device service", map[string]any{"error": err.Error()})
	} else if cfg.Devices.Enabled {
		logger.InfoC("device", "Device event service started")
	}

	// Start the MCP server so CLI providers (claude-cli/codex-cli/gemini-cli)
	// can call claw's host-side tools natively over MCP.
	if err := startMCPServer(cfg, agentLoop, msgBus, services); err != nil {
		return nil, err
	}

	// Start cognitive-memory consolidation (inert unless an agent is allowed the
	// cogmem tools).
	services.CogmemManager = setupCogmemConsolidation(cfg, agentLoop)

	return services, nil
}

// mcpHostAllowlist resolves the tool allowlist the MCP host catalogue (tools/list)
// should advertise. Parity rule: external MCP and the internal API path expose the
// SAME tools, gated per-agent at execution time (tools/call resolves the
// session_token to an agent and enforces that agent's config + ACL). So the
// default exposes the FULL union of every agent's allowed tools ("*") — anything
// an agent can use internally is discoverable and callable externally, subject to
// the same per-agent gate. An explicit cfg.MCPHost.Tools narrows the catalogue
// (operator override); to expose NO tools, disable mcp_host instead.
func mcpHostAllowlist(cfg *config.Config) []string {
	if len(cfg.MCPHost.Tools) > 0 {
		return cfg.MCPHost.Tools
	}
	return []string{"*"}
}

// startMCPServer starts the MCP server if enabled and wires it into services
// and the agent loop. Called on both initial startup and config reload.
func startMCPServer(cfg *config.Config, agentLoop *agent.AgentLoop, msgBus *bus.MessageBus, services *gatewayServices) error {
	if !cfg.MCPHostEffectivelyEnabled() {
		return nil
	}
	autoStarted := !cfg.MCPHost.Enabled
	defaultAgent := agentLoop.GetRegistry().GetDefaultAgent()
	if defaultAgent == nil || defaultAgent.Tools == nil {
		logger.WarnC("mcpserver", "MCP host enabled but no default agent registry available — skipping start")
		return nil
	}

	agentRegistries := make(map[string]*tools.ToolRegistry)
	agentWorkspaces := make(map[string]string)
	for _, agentID := range agentLoop.GetRegistry().ListAgentIDs() {
		a, ok := agentLoop.GetRegistry().GetAgent(agentID)
		if !ok || a.Tools == nil {
			continue
		}
		agentRegistries[agentID] = a.Tools
		agentWorkspaces[agentID] = a.Workspace
	}

	srv, err := mcpserver.New(
		mcpserver.WithAgentRegistries(agentRegistries),
		mcpserver.WithAgentTokens(agentLoop.AgentTokens()),
		mcpserver.WithAgentWorkspaces(agentWorkspaces),
		mcpserver.WithListen(cfg.MCPHost.Listen),
		mcpserver.WithEndpointPath(cfg.MCPHost.EndpointPath),
		mcpserver.WithAllowlist(mcpHostAllowlist(cfg)),
		mcpserver.WithMessageBus(msgBus),
	)
	if err != nil {
		return fmt.Errorf("error creating MCP server: %w", err)
	}
	if err := srv.Start(); err != nil {
		return fmt.Errorf("error starting MCP server: %w", err)
	}
	services.MCPServer = srv
	agentLoop.SetSessionTokenIssuer(srv.SessionTokens())

	if testTok := os.Getenv("CLAW_MCP_TEST_TOKEN"); testTok != "" {
		defaultAgentID := agentLoop.GetRegistry().GetDefaultAgentID()
		if defaultAgentID == "" {
			logger.WarnC("mcpserver", "CLAW_MCP_TEST_TOKEN set but no default agent found — skipping registration")
		} else {
			if da, ok := agentLoop.GetRegistry().GetAgent(defaultAgentID); ok && da != nil {
				archiveDir := filepath.Join(da.Workspace, "sessions")
				srv.SessionTokens().Register(testTok, defaultAgentID, "test-session", archiveDir)
				logger.InfoCF("mcpserver", "Test session token registered",
					map[string]any{"agent": defaultAgentID})
			}
		}
	}

	logger.InfoCF("mcpserver", "MCP host started",
		map[string]any{
			"listen":       srv.Listen(),
			"endpoint":     srv.EndpointPath(),
			"auto_enabled": autoStarted,
		})

	return nil
}

// stopAndCleanupServices stops all services and cleans up resources
func stopAndCleanupServices(
	services *gatewayServices,
	shutdownTimeout time.Duration,
) {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if services.CogmemManager != nil {
		services.CogmemManager.Stop()
	}
	if services.MCPServer != nil {
		if err := services.MCPServer.Shutdown(shutdownCtx); err != nil {
			logger.WarnCF("mcpserver", "MCP server shutdown error", map[string]any{"error": err.Error()})
		}
	}
	if services.ChannelManager != nil {
		services.ChannelManager.StopAll(shutdownCtx)
	}
	if services.DeviceService != nil {
		services.DeviceService.Stop()
	}
	if services.CronService != nil {
		services.CronService.Stop()
	}
	if services.MediaStore != nil {
		// Stop the media store if it's a FileMediaStore with cleanup
		if fms, ok := services.MediaStore.(*media.FileMediaStore); ok {
			fms.Stop()
		}
	}
}

// shutdownGateway performs a complete gateway shutdown
func shutdownGateway(
	services *gatewayServices,
	agentLoop *agent.AgentLoop,
	provider providers.LLMProvider,
	fullShutdown bool,
) {
	if cp, ok := provider.(providers.StatefulProvider); ok && fullShutdown {
		cp.Close()
	}

	stopAndCleanupServices(services, gracefulShutdownTimeout)

	if services.HTTPHost != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		if err := services.HTTPHost.Stop(shutdownCtx); err != nil {
			logger.WarnCF("gateway", "Shared HTTP listener shutdown error", map[string]any{"error": err.Error()})
		}
		cancel()
	}

	agentLoop.Stop()
	agentLoop.Close()

	logger.Info("✓ Gateway stopped")
}

// handleConfigReload handles config file reload by stopping all services,
// reloading the provider and config, and restarting services with the new config.
func handleConfigReload(
	ctx context.Context,
	al *agent.AgentLoop,
	newCfg *config.Config,
	providerRef *providers.LLMProvider,
	services *gatewayServices,
	msgBus *bus.MessageBus,
) error {
	logger.Info("🔄 Config file changed, reloading...")

	newModel := newCfg.Agents.Defaults.DefaultModelName()

	logger.Infof(" New model is '%s', recreating provider...", newModel)

	// Stop all services before reloading
	logger.Info("  Stopping all services...")
	stopAndCleanupServices(services, serviceShutdownTimeout)

	// Create new provider from updated config first to ensure validity
	// This will use the correct API key and settings from newCfg.Models
	newProvider, newModelID, err := providers.CreateProvider(newCfg)
	if err != nil {
		logger.WarnCF("gateway", "No model configured after reload, running in unconfigured state", map[string]any{"detail": err.Error()})
		newProvider = providers.NewUnconfiguredProvider()
		newModelID = ""
	}

	if newModelID != "" {
		newCfg.Agents.Defaults.SetDefaultModel(newModelID)
	}

	// Use the atomic reload method on AgentLoop to safely swap provider and config.
	// This handles locking internally to prevent races with in-flight LLM calls
	// and concurrent reads of registry/config while the swap occurs.
	reloadCtx, reloadCancel := context.WithTimeout(context.Background(), providerReloadTimeout)
	defer reloadCancel()

	if err := al.ReloadProviderAndConfig(reloadCtx, newProvider, newCfg); err != nil {
		logger.Errorf("  ⚠ Error reloading agent loop: %v", err)
		// Close the newly created provider since it wasn't adopted
		if cp, ok := newProvider.(providers.StatefulProvider); ok {
			cp.Close()
		}
		logger.Warn("  Attempting to restart services with old provider and config...")
		if restartErr := restartServices(ctx, al, services, msgBus); restartErr != nil {
			logger.Errorf("  ⚠ Failed to restart services: %v", restartErr)
		}
		return fmt.Errorf("error reloading agent loop: %w", err)
	}

	// Update local provider reference only after successful atomic reload
	*providerRef = newProvider

	// Restart all services with new config
	logger.Info("  Restarting all services with new configuration...")
	if err := restartServices(ctx, al, services, msgBus); err != nil {
		logger.Errorf("  ⚠ Error restarting services: %v", err)
		return fmt.Errorf("error restarting services: %w", err)
	}

	logger.Info("  ✓ Provider, configuration, and services reloaded successfully (thread-safe)")
	return nil
}

// restartServices restarts all services after a config reload.
// runCtx is the long-lived main loop context used for channel lifetime;
// a short-lived init context is used internally for bounded init steps.
func restartServices(
	runCtx context.Context,
	al *agent.AgentLoop,
	services *gatewayServices,
	msgBus *bus.MessageBus,
) error {
	// Get current config from agent loop (which has been updated if this is a reload)
	cfg := al.GetConfig()

	// Re-create and start cron service with new config, then re-register the
	// cron tool with all agents so it is available after the registry is rebuilt.
	execTimeout := time.Duration(cfg.Tools.Cron.ExecTimeoutMinutes) * time.Minute
	var cronTool *toolschedule.CronTool
	services.CronService, cronTool = setupCronTool(
		al,
		msgBus,
		cfg.WorkspacePath(),
		cfg.Agents.Defaults.RestrictToWorkspace,
		execTimeout,
		cfg,
	)
	if cronTool != nil {
		al.RegisterTool(cronTool)
	}
	if err := services.CronService.Start(); err != nil {
		return fmt.Errorf("error restarting cron service: %w", err)
	}
	logger.InfoC("cron", "Cron service restarted")

	// Stop the old media store before creating a new one
	if fms, ok := services.MediaStore.(*media.FileMediaStore); ok {
		fms.Stop()
	}

	// Re-create media store with new config
	services.MediaStore = media.NewFileMediaStoreWithCleanup(media.MediaCleanerConfig{
		Enabled:  cfg.Tools.MediaCleanup.Enabled,
		MaxAge:   time.Duration(cfg.Tools.MediaCleanup.MaxAge) * time.Minute,
		Interval: time.Duration(cfg.Tools.MediaCleanup.Interval) * time.Minute,
	})
	// Start the media store if it's a FileMediaStore with cleanup
	if fms, ok := services.MediaStore.(*media.FileMediaStore); ok {
		fms.Start()
	}
	al.SetMediaStore(services.MediaStore)

	// Re-create channel manager with new config
	var err error
	services.ChannelManager, err = channels.NewManager(cfg, msgBus, services.MediaStore)
	if err != nil {
		// Stop the media store if it's a FileMediaStore with cleanup
		if fms, ok := services.MediaStore.(*media.FileMediaStore); ok {
			fms.Stop()
		}
		return fmt.Errorf("error recreating channel manager: %w", err)
	}
	al.SetChannelManager(services.ChannelManager)

	enabledChannels := services.ChannelManager.GetEnabledChannels()
	if len(enabledChannels) > 0 {
		logger.InfoCF("channels", "Channels enabled", map[string]any{"channels": enabledChannels})
	} else {
		logger.WarnC("channels", "No channels enabled")
	}

	// Rebuild the shared mux (channel webhooks, WebUI routes, callback route)
	// and swap it into the long-lived httpHost. The listener is NOT recreated
	// — keeping it alive is what lets WebUI WebSocket connections survive a
	// config reload (investigation 7a5377d9, option #1).
	rebuildSharedHTTPServer(services, cfg.Gateway.Host, cfg.Gateway.Port, services.ChannelManager, services.HTTPHost, al)

	if err := services.ChannelManager.StartAll(runCtx); err != nil {
		return fmt.Errorf("error restarting channels: %w", err)
	}
	logger.InfoCF("channels", "Channels restarted", map[string]any{"health": fmt.Sprintf("http://%s:%d/health", cfg.Gateway.Host, cfg.Gateway.Port)})

	// Re-create device service with new config
	stateManager := state.NewManager(cfg.WorkspacePath())
	services.DeviceService = devices.NewService(devices.Config{
		Enabled:    cfg.Devices.Enabled,
		MonitorUSB: cfg.Devices.MonitorUSB,
	}, stateManager)
	services.DeviceService.SetBus(msgBus)
	if err := services.DeviceService.Start(runCtx); err != nil {
		logger.WarnCF("device", "Failed to restart device service", map[string]any{"error": err.Error()})
	} else if cfg.Devices.Enabled {
		logger.InfoC("device", "Device event service restarted")
	}

	// Wire up voice transcription with new config
	transcriber := voice.DetectTranscriber(cfg)
	al.SetTranscriber(transcriber) // This will set it to nil if disabled
	if transcriber != nil {
		logger.InfoCF("voice", "Transcription re-enabled (agent-level)", map[string]any{"provider": transcriber.Name()})
	} else {
		logger.InfoCF("voice", "Transcription disabled", nil)
	}

	// Restart MCP server — it was shut down as part of stopAndCleanupServices.
	if err := startMCPServer(cfg, al, msgBus, services); err != nil {
		return fmt.Errorf("error restarting MCP server: %w", err)
	}

	return nil
}

// setupConfigWatcherPolling sets up a simple polling-based config file watcher.
// interval controls how often the file is polled; callers should pass
// cfg.ConfigReloadInterval() so the value honours the config override and
// MinConfigReloadIntervalSeconds floor. Returns a channel for config updates
// and a stop function.
func setupConfigWatcherPolling(configPath string, interval, debounce time.Duration, debug bool) (chan *config.Config, func()) {
	configChan := make(chan *config.Config, 1)
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		// appliedModTime/appliedSize track the last config we actually reloaded.
		// observedModTime/observedSize track the most recent on-disk state.
		appliedModTime := getFileModTime(configPath)
		appliedSize := getFileSize(configPath)
		observedModTime := appliedModTime
		observedSize := appliedSize

		// Quiescence debounce: once a change is seen we wait for the file to be
		// stable for `debounce` (resetting on every further change) before
		// reloading, so a burst of edits collapses into one reload.
		var pending bool
		var quietDeadline time.Time

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				currentModTime := getFileModTime(configPath)
				currentSize := getFileSize(configPath)

				// A change relative to the most recent observation resets the
				// quiet timer — the user is still editing.
				if currentModTime.After(observedModTime) || currentSize != observedSize {
					observedModTime = currentModTime
					observedSize = currentSize
					quietDeadline = time.Now().Add(debounce)
					pending = true
					if debug {
						logger.Debugf("🔍 Config file change detected; debouncing %s", debounce)
					}
					continue
				}

				// File is stable since the last observation. If a change is
				// pending and it has now been quiet for the full debounce window,
				// and the file actually differs from what we last applied, reload.
				if !pending || time.Now().Before(quietDeadline) {
					continue
				}
				pending = false
				if !currentModTime.After(appliedModTime) && currentSize == appliedSize {
					continue
				}

				newCfg, err := config.LoadConfig(configPath)
				if err != nil {
					logger.Errorf("⚠ Error loading new config: %v", err)
					logger.Warn("  Using previous valid config")
					continue
				}
				if err := newCfg.ValidateModels(); err != nil {
					logger.Errorf("  ⚠ New config validation failed: %v", err)
					logger.Warn("  Using previous valid config")
					continue
				}

				logger.Info("✓ Config file validated and loaded")
				appliedModTime = currentModTime
				appliedSize = currentSize

				select {
				case configChan <- newCfg:
				default:
					logger.Warn("⚠ Previous config reload still in progress, skipping")
				}

			case <-stop:
				return
			}
		}
	}()

	stopFunc := func() {
		close(stop)
		wg.Wait()
	}

	return configChan, stopFunc
}

// getFileModTime returns the modification time of a file, or zero time if file doesn't exist
func getFileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// getFileSize returns the size of a file, or 0 if file doesn't exist
func getFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// setupCronTool creates the cron service and, if cron is enabled, the CronTool
// that agents use to manage scheduled jobs. Registration of the tool with the
// agent registry is the caller's responsibility so that reloads re-register on
// the freshly-rebuilt registry (matching the pattern used by registerSharedTools).
func setupCronTool(
	agentLoop *agent.AgentLoop,
	msgBus *bus.MessageBus,
	workspace string,
	restrict bool,
	execTimeout time.Duration,
	cfg *config.Config,
) (*cron.CronService, *toolschedule.CronTool) {
	cronStorePath := filepath.Join(cfg.CronPath(), "jobs.json")

	// Create cron service
	cronService := cron.NewCronService(cronStorePath, nil)

	// Create CronTool if enabled
	var cronTool *toolschedule.CronTool
	if cfg.Tools.Cron.Enabled {
		var err error
		cronTool, err = toolschedule.NewCronTool(cronService, agentLoop, msgBus, workspace, restrict, execTimeout, cfg)
		if err != nil {
			logger.Fatalf("Critical error during CronTool initialization: %v", err)
		}
	}

	// Set onJob handler
	if cronTool != nil {
		cronService.SetOnJob(func(job *cron.CronJob) (string, error) {
			result := cronTool.ExecuteJob(context.Background(), job)
			return result, nil
		})
	}

	return cronService, cronTool
}
