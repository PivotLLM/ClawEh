package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/cmd/claw/internal"
	"github.com/PivotLLM/ClawEh/pkg/agent"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/discord"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/irc"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/line"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/matrix"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/webui"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/slack"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/telegram"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/whatsapp"
	_ "github.com/PivotLLM/ClawEh/pkg/channels/whatsapp_native"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/cron"
	"github.com/PivotLLM/ClawEh/pkg/devices"
	"github.com/PivotLLM/ClawEh/pkg/health"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/mcpserver"
	"github.com/PivotLLM/ClawEh/pkg/media"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/state"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/PivotLLM/ClawEh/pkg/voice"
)

// Timeout constants for service operations
const (
	serviceRestartTimeout   = 30 * time.Second
	serviceShutdownTimeout  = 30 * time.Second
	providerReloadTimeout   = 30 * time.Second
	gracefulShutdownTimeout = 15 * time.Second
)

// gatewayServices holds references to all running services
type gatewayServices struct {
	CronService    *cron.CronService
	MediaStore     media.MediaStore
	ChannelManager *channels.Manager
	DeviceService  *devices.Service
	HealthServer   *health.Server
	MCPServer      *mcpserver.MCPServer
}

func gatewayCmd(debug bool) error {
	// Acquire PID lock before connecting to any external service.
	// If another instance is already running this exits immediately with a clear error.
	baseDir := internal.GetPicoclawHome()
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

	// Apply logging config (debug flag overrides level)
	if cfg.Logging.File {
		logPath := filepath.Join(internal.GetPicoclawHome(), "logs", "claw.log")
		if err := logger.EnableFileLogging(logPath, cfg.Logging.JSON); err != nil {
			logger.WarnCF("gateway", "Failed to enable file logging", map[string]any{"path": logPath, "error": err.Error()})
		}
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

	dispatcher := providers.NewProviderDispatcher(cfg)
	msgBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider, dispatcher)

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
	services, err := setupAndStartServices(cfg, agentLoop, msgBus)
	if err != nil {
		return err
	}

	// Register callback reply endpoint. Returns 401 when no valid token is found.
	services.HealthServer.Handle("POST /api/reply/{token}", func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		content := strings.TrimSpace(string(body))
		if content == "" {
			http.Error(w, "empty body", http.StatusBadRequest)
			return
		}

		agentID, ok := agentLoop.ValidateCallbackToken(token)
		if !ok {
			logger.WarnCF("callback", "Rejected callback with invalid or expired token",
				map[string]any{"remote_addr": r.RemoteAddr, "body_len": len(content)})
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}

		logger.InfoCF("callback", "Accepted callback",
			map[string]any{"agent": agentID, "remote_addr": r.RemoteAddr, "body_len": len(content)})

		if err := agentLoop.HandleCallbackMessage(r.Context(), agentID, content); err != nil {
			logger.WarnCF("callback", "Failed to deliver callback message",
				map[string]any{"agent": agentID, "error": err.Error()})
			http.Error(w, "failed to deliver message", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusAccepted)
	})

	logger.InfoF("Gateway started", map[string]any{"addr": fmt.Sprintf("%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go agentLoop.Run(ctx)

	// Setup config file watcher for hot reload
	reloadInterval := cfg.ConfigReloadInterval()
	logger.InfoF("Config reload watcher", map[string]any{"interval": reloadInterval.String()})
	configReloadChan, stopWatch := setupConfigWatcherPolling(configPath, reloadInterval, debug)
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
) (*gatewayServices, error) {
	services := &gatewayServices{}

	// Setup cron tool and service
	execTimeout := time.Duration(cfg.Tools.Cron.ExecTimeoutMinutes) * time.Minute
	var cronTool *tools.CronTool
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

	// Setup shared HTTP server with health endpoints and webhook handlers
	addr := fmt.Sprintf("%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
	services.HealthServer = health.NewServer(cfg.Gateway.Host, cfg.Gateway.Port)
	services.ChannelManager.SetupHTTPServer(addr, services.HealthServer)

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
	// can call claw's host-side tools natively over MCP. Uses the default
	// agent's tool registry so the tools have a workspace bound.
	if cfg.MCPHostEffectivelyEnabled() {
		autoStarted := !cfg.MCPHost.Enabled
		defaultAgent := agentLoop.GetRegistry().GetDefaultAgent()
		if defaultAgent == nil || defaultAgent.Tools == nil {
			logger.WarnC("mcpserver", "MCP host enabled but no default agent registry available — skipping start")
		} else {
			// Collect per-agent registries (skip the default agent to avoid double-registration).
			agentRegistries := make(map[string]*tools.ToolRegistry)
			for _, agentID := range agentLoop.GetRegistry().ListAgentIDs() {
				a, ok := agentLoop.GetRegistry().GetAgent(agentID)
				if !ok || a.Tools == nil {
					continue
				}
				// Skip if this is the same registry as the default agent's.
				if a.Tools == defaultAgent.Tools {
					continue
				}
				agentRegistries[agentID] = a.Tools
			}

			srv, err := mcpserver.New(
				mcpserver.WithRegistry(defaultAgent.Tools),
				mcpserver.WithAgentRegistries(agentRegistries),
				mcpserver.WithListen(cfg.MCPHost.Listen),
				mcpserver.WithEndpointPath(cfg.MCPHost.EndpointPath),
				mcpserver.WithAllowlist(cfg.MCPHost.Tools),
			)
			if err != nil {
				return nil, fmt.Errorf("error creating MCP server: %w", err)
			}
			if err := srv.Start(); err != nil {
				return nil, fmt.Errorf("error starting MCP server: %w", err)
			}
			services.MCPServer = srv
			logger.InfoCF("mcpserver", "MCP host started",
				map[string]any{
					"listen":       srv.Listen(),
					"endpoint":     srv.EndpointPath(),
					"auto_enabled": autoStarted,
				})

			// Write per-agent .claude.json files so each agent's claude subprocess
			// uses the correct workspace-scoped MCP endpoint.
			agentWorkspaces := make(map[string]string)
			for agentID := range agentRegistries {
				if a, ok := agentLoop.GetRegistry().GetAgent(agentID); ok {
					agentWorkspaces[agentID] = a.Workspace
				}
			}
			baseURL := "http://" + srv.Listen()
			srv.WriteWorkspaceConfigs(baseURL, agentWorkspaces)
		}
	}

	return services, nil
}

// stopAndCleanupServices stops all services and cleans up resources
func stopAndCleanupServices(
	services *gatewayServices,
	shutdownTimeout time.Duration,
) {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

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
	// This will use the correct API key and settings from newCfg.ModelList
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
	var cronTool *tools.CronTool
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

	// Setup HTTP server with new config
	addr := fmt.Sprintf("%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
	services.HealthServer = health.NewServer(cfg.Gateway.Host, cfg.Gateway.Port)
	services.ChannelManager.SetupHTTPServer(addr, services.HealthServer)

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

	return nil
}

// setupConfigWatcherPolling sets up a simple polling-based config file watcher.
// interval controls how often the file is polled; callers should pass
// cfg.ConfigReloadInterval() so the value honours the config override and
// MinConfigReloadIntervalSeconds floor. Returns a channel for config updates
// and a stop function.
func setupConfigWatcherPolling(configPath string, interval time.Duration, debug bool) (chan *config.Config, func()) {
	configChan := make(chan *config.Config, 1)
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		// Get initial file info
		lastModTime := getFileModTime(configPath)
		lastSize := getFileSize(configPath)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				currentModTime := getFileModTime(configPath)
				currentSize := getFileSize(configPath)

				// Check if file changed (modification time or size changed)
				if currentModTime.After(lastModTime) || currentSize != lastSize {
					if debug {
						logger.Debugf("🔍 Config file change detected")
					}

					// Debounce - wait a bit to ensure file write is complete
					time.Sleep(500 * time.Millisecond)

					// Validate and load new config
					newCfg, err := config.LoadConfig(configPath)
					if err != nil {
						logger.Errorf("⚠ Error loading new config: %v", err)
						logger.Warn("  Using previous valid config")
						continue
					}

					// Validate the new config
					if err := newCfg.ValidateModelList(); err != nil {
						logger.Errorf("  ⚠ New config validation failed: %v", err)
						logger.Warn("  Using previous valid config")
						continue
					}

					logger.Info("✓ Config file validated and loaded")

					// Update last known state
					lastModTime = currentModTime
					lastSize = currentSize

					// Send new config to main loop (non-blocking)
					select {
					case configChan <- newCfg:
					default:
						// Channel full, skip this update
						logger.Warn("⚠ Previous config reload still in progress, skipping")
					}
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
) (*cron.CronService, *tools.CronTool) {
	cronStorePath := filepath.Join(cfg.CronPath(), "jobs.json")

	// Create cron service
	cronService := cron.NewCronService(cronStorePath, nil)

	// Create CronTool if enabled
	var cronTool *tools.CronTool
	if cfg.Tools.IsToolEnabled("cron") {
		var err error
		cronTool, err = tools.NewCronTool(cronService, agentLoop, msgBus, workspace, restrict, execTimeout, cfg)
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
