package gateway

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PivotLLM/cogmem/consolidate"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/channels/webui"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/cron"
	"github.com/PivotLLM/ClawEh/devices"
	"github.com/PivotLLM/ClawEh/forum"
	"github.com/PivotLLM/ClawEh/health"
	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/mcpserver"
	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/mountwatch"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolschedule "github.com/PivotLLM/ClawEh/tools/schedule"
	"github.com/PivotLLM/ClawEh/utils"
	"github.com/PivotLLM/ClawEh/voice"
	webserver "github.com/PivotLLM/ClawEh/web/backend"
	webapi "github.com/PivotLLM/ClawEh/web/backend/api"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

// gatewayServices holds references to all running services
type gatewayServices struct {
	CronService    *cron.CronService
	CronTool       *toolschedule.CronTool
	MountWatcher   *mountwatch.Watcher
	MediaStore     media.MediaStore
	ChannelManager *channels.Manager
	DeviceService  *devices.Service
	HealthServer   *health.Server
	MCPServer      *mcpserver.MCPServer
	WebServer      *webserver.Server
	HTTPHost       *httpHost
	// TLSCerts is the HTTPS listener's certificate (nil when gateway.tls.mode
	// is "off"). Like HTTPHost it lives for the whole process; stopTLSWatch
	// ends its file watcher at shutdown.
	TLSCerts     *tlscert.Manager
	stopTLSWatch context.CancelFunc
	// SessionTokens is the MCP session-token store. Like HTTPHost it lives for
	// the whole process: every MCP server built (at start and on each config
	// reload) shares it, so the tokens rendered into running prompts and
	// Maestro workers keep resolving across a reload. Created by the first
	// startMCPServer that runs; nil while the MCP host has never been enabled.
	SessionTokens *mcpserver.SessionTokenStore
	// bootListeners are the listener settings the process bound at start;
	// a saved config that differs needs a restart (warnListenerConfigChanged,
	// GET /api/tls restart_required).
	bootListeners config.ListenerSettings

	CogmemManager *consolidate.Manager
	// Forum is the forum service. Like HTTPHost it lives for the whole
	// process; shutdownGateway closes it before the agent loop stops.
	Forum *forum.Service
	// AuthStore holds the WebUI login sessions and the admin credentials;
	// stopAuthWatch ends its credentials-file poller at shutdown.
	AuthStore     *middleware.AuthStore
	stopAuthWatch context.CancelFunc
	// fatal is the fail-fast path a dying core service reports to.
	fatal *fatalNotifier
	// store is the live configuration, shared with the WebUI API.
	store *config.Store
}

// setupAndStartServices initializes and starts all services
func setupAndStartServices(
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
	msgBus *bus.MessageBus,
	configPath string,
	store *config.Store,
	fatal *fatalNotifier,
) (*gatewayServices, error) {
	services := &gatewayServices{fatal: fatal, store: store}

	ensureCommonDir(cfg)

	if err := startCron(services, agentLoop, msgBus, cfg); err != nil {
		return nil, err
	}

	// Watch notify-enabled external mounts for new files (cron-style notices).
	services.MountWatcher = mountwatch.New(agentLoop.GetConfig, msgBus, 0, agentLoop.Alerter())
	services.MountWatcher.Start()
	logger.InfoC("mountwatch", "Mount watcher started")

	// Stage downloaded media under the instance data dir (e.g. ~/.claw/media,
	// /opt/claw/media) rather than shared /tmp, so files stay app-owned and two
	// instances on one host can't collide on a single dir.
	utils.SetMediaStagingDir(filepath.Join(cfg.DataDir(), "media"))
	services.MediaStore = startMediaStore(cfg)
	// Stopped here if setup fails before the channel manager takes it over.
	mediaOwned := true
	defer func() {
		if mediaOwned {
			stopMediaStore(services.MediaStore)
		}
	}()

	// The HTTPS listener's certificate, unless gateway.tls.mode is "off".
	// Loaded (a self-signed pair generated or renewed) BEFORE the channels are
	// built: the device channel reads the certificate's names for its own Host
	// allowlist (tlscert.NamesForConfig), so the file has to be current first.
	if cfg.Gateway.HTTPSEnabled() {
		var err error
		services.TLSCerts, err = tlscert.Load(tlsOptions(cfg, agentLoop.Alerter()))
		if err != nil {
			return nil, fmt.Errorf("HTTPS listener certificate: %w", err)
		}
	}

	var err error
	services.ChannelManager, err = channels.NewManager(cfg, msgBus, services.MediaStore)
	if err != nil {
		return nil, fmt.Errorf("error creating channel manager: %w", err)
	}
	mediaOwned = false
	wireChannelManager(services, agentLoop)
	agentLoop.SetMediaStore(services.MediaStore)

	// Wire up voice transcription if a supported provider is configured.
	if transcriber := voice.DetectTranscriberWithAlerter(cfg, agentLoop.Alerter()); transcriber != nil {
		agentLoop.SetTranscriber(transcriber)
		logger.InfoCF("voice", "Transcription enabled (agent-level)", map[string]any{"provider": transcriber.Name()})
	}
	logEnabledChannels(services.ChannelManager)

	// Setup the shared listeners: plain HTTP on gateway.host (loopback by
	// default) and HTTPS where gateway.tls.mode places it. They are owned by
	// httpHost and stay up across config reloads; only the handler mux is
	// swapped on reload, so WebUI WebSocket connections and channel webhooks
	// survive a Manager rebuild. See rebuildSharedHTTPServer for the swap seam.
	services.WebServer = newMergedWebServer(store, cfg)
	wireWebServer(services, agentLoop)
	if err := startHTTPHost(services, cfg, agentLoop, fatal); err != nil {
		return nil, err
	}

	if err := services.ChannelManager.StartAll(context.Background()); err != nil {
		return nil, fmt.Errorf("error starting channels: %w", err)
	}

	// The listener is up and the channels are running, which is what /ready
	// means. It has to be set here rather than left to health.Server.Start():
	// that method marks ready as a side effect of starting the health server's
	// OWN listener, and the merged binary never uses it — the handlers are
	// registered onto the shared mux instead.
	markReady(services, true)
	logHealthEndpoints(services, cfg)

	services.DeviceService = newDeviceService(cfg, agentLoop, msgBus)
	if err := services.DeviceService.Start(context.Background()); err != nil {
		logger.ErrorCF("device", "Error starting device service", map[string]any{"error": err.Error()})
	} else if cfg.Devices.Enabled {
		logger.InfoC("device", "Device event service started")
	}

	if err := startAgentServices(cfg, agentLoop, msgBus, configPath, services); err != nil {
		return nil, err
	}
	return services, nil
}

// startAgentServices starts what serves the agents once the channels run:
// external MCP servers, the MCP host, memory consolidation, the backup
// scheduler and session retention.
func startAgentServices(cfg *config.Config, agentLoop *agent.AgentLoop, msgBus *bus.MessageBus, configPath string, services *gatewayServices) error {
	// Connect external MCP servers and register their tools onto the agent
	// registries BEFORE the host server enumerates its catalogue — otherwise
	// CLI-based agents (and CLI fallbacks) would not see the mcp_* tools until
	// a later tool-list refresh brought the host catalogue back in step.
	if err := agentLoop.EnsureMCPInitialized(context.Background()); err != nil {
		logger.WarnCF("mcpserver", "MCP client initialization reported an error", map[string]any{"error": err.Error()})
	}

	// Start the MCP server so CLI providers (claude-cli/codex-cli/antigravity-cli/cursor-cli)
	// can call claw's host-side tools natively over MCP.
	if err := startMCPServer(cfg, agentLoop, msgBus, services); err != nil {
		return err
	}

	// Start cognitive-memory consolidation (inert unless an agent is allowed the
	// cogmem tools).
	services.CogmemManager = setupCogmemConsolidation(cfg, agentLoop)

	// Start the optional nightly backup scheduler. Boot-only: it reads live config
	// each tick (via agentLoop.GetConfig), so toggling/retiming it on reload takes
	// effect without restarting the loop. Inert until backup.enabled is set.
	startBackupScheduler(agentLoop.GetConfig, configPath, agentLoop.Alerter())
	// Nightly session retention (session.retention_days) and cogmem snapshot
	// pruning; boot-only like the backup, reads live config each pass.
	agentLoop.StartRetention()

	return nil
}

// ensureCommonDir creates the shared "common" directory so the common_*
// tools have a place to read and write. Idempotent.
func ensureCommonDir(cfg *config.Config) {
	if commonDir := cfg.ResolveCommonDir(); commonDir != "" {
		if err := os.MkdirAll(commonDir, 0o700); err != nil {
			logger.WarnCF("gateway", "Failed to create common directory", map[string]any{"path": commonDir, "error": err.Error()})
		}
	}
}

// startCron creates and starts the cron service and, when cron is enabled,
// registers the cron tool on every agent and starts its listeners.
func startCron(services *gatewayServices, agentLoop *agent.AgentLoop, msgBus *bus.MessageBus, cfg *config.Config) error {
	var cronTool *toolschedule.CronTool
	services.CronService, cronTool = setupCronTool(agentLoop, msgBus, cfg)
	if cronTool != nil {
		agentLoop.RegisterTool(cronTool)
	}
	if err := services.CronService.Start(); err != nil {
		return fmt.Errorf("error starting cron service: %w", err)
	}
	logger.InfoC("cron", "Cron service started")
	services.CronTool = cronTool
	if cronTool != nil {
		cronTool.StartListeners(context.Background())
	}
	return nil
}

// startMediaStore creates the media store for file lifecycle management and
// starts its TTL cleanup.
func startMediaStore(cfg *config.Config) media.MediaStore {
	store := media.NewFileMediaStoreWithCleanup(media.MediaCleanerConfig{
		Enabled:  cfg.Tools.MediaCleanup.Enabled,
		MaxAge:   time.Duration(cfg.Tools.MediaCleanup.MaxAge) * time.Minute,
		Interval: time.Duration(cfg.Tools.MediaCleanup.Interval) * time.Minute,
	})
	store.Start()
	return store
}

// stopMediaStore stops a media store's TTL cleanup.
func stopMediaStore(store media.MediaStore) {
	if fms, ok := store.(*media.FileMediaStore); ok {
		fms.Stop()
	}
}

// wireChannelManager hands the new channel manager to the agent loop and
// to the device channel's agent querier and TLS. Every reload rebuilds the
// manager, so this runs each time.
func wireChannelManager(services *gatewayServices, agentLoop *agent.AgentLoop) {
	services.ChannelManager.SetAlerter(agentLoop.Alerter())
	agentLoop.SetChannelManager(services.ChannelManager)
	// Let the device gateway answer operator-client reads (agents.list, chat.history).
	injectDeviceAgentQuerier(services.ChannelManager, agentLoop)
	// Let channels.device.tls serve the HTTPS listener's certificate.
	injectDeviceTLS(services.ChannelManager, services.TLSCerts)
}

// logEnabledChannels logs the channels the manager runs.
func logEnabledChannels(mgr *channels.Manager) {
	enabledChannels := mgr.GetEnabledChannels()
	if len(enabledChannels) > 0 {
		logger.InfoCF("channels", "Channels enabled", map[string]any{"channels": enabledChannels})
	} else {
		logger.WarnC("channels", "No channels enabled")
	}
}

// wireWebServer hands the WebUI API the live objects it drives. Channels are
// resolved per call, so a lookup survives a channel manager rebuild on
// reload.
func wireWebServer(services *gatewayServices, agentLoop *agent.AgentLoop) {
	api := services.WebServer.APIHandler()
	// Share the agent loop's named message-token store with the WebUI API so
	// mint/revoke operations mutate the exact instance the message route validates
	// against (no reload needed for a token to activate/deactivate).
	api.SetMessageTokenLoop(agentLoop)
	// DELETE /api/sessions releases a live session before erasing its archive.
	api.SetSessionReleaser(agentLoop.ReleaseSession)
	// Expose the live outbound-MCP connection state to the WebUI MCP page. Reads the
	// same manager the agent loop owns (reused in place across reloads).
	api.SetMCPStatusLoop(agentLoop)
	// The WebUI QR pairing panel drives device linking on a live secmsg
	// channel by name.
	api.SetSecMsgLinker(func(name string) (webapi.SecMsgLinker, bool) {
		mgr := services.ChannelManager
		if mgr == nil {
			return nil, false
		}
		ch, ok := mgr.GetChannel(name)
		if !ok {
			return nil, false
		}
		linker, ok := ch.(webapi.SecMsgLinker)
		return linker, ok
	})
	// Removing a device in the WebUI closes its open connections on the live
	// device channel.
	api.SetDeviceDisconnector(func(deviceID string) {
		mgr := services.ChannelManager
		if mgr == nil {
			return
		}
		ch, ok := mgr.GetChannel("device")
		if !ok {
			return
		}
		if d, ok := ch.(interface{ DisconnectDevice(deviceID string) }); ok {
			d.DisconnectDevice(deviceID)
		}
	})
}

// startHTTPHost creates the shared listeners with their IP allowlist, Host
// policy and login, mounts the mux and starts serving.
func startHTTPHost(services *gatewayServices, cfg *config.Config, agentLoop *agent.AgentLoop, fatal *fatalNotifier) error {
	// Empty means loopback only (see GatewayConfig.EffectiveAllowedCIDRs), so
	// the WebUI grants no off-box access until an allowlist is configured,
	// whatever the bind address.
	allowedCIDRs := cfg.Gateway.EffectiveAllowedCIDRs()
	hostOpts := hostOptions{
		HTTPHosts:      cfg.Gateway.HTTPBindHosts(),
		Port:           cfg.Gateway.EffectivePort(),
		AllowedCIDRs:   allowedCIDRs,
		TrustedProxies: cfg.Gateway.TrustedProxies,
	}
	if services.TLSCerts != nil {
		hostOpts.TLSHosts = cfg.Gateway.HTTPSBindHosts()
		hostOpts.TLSPort = cfg.Gateway.EffectiveTLSPort()
		hostOpts.TLSConfig = services.TLSCerts.TLSConfig()
		hostOpts.HSTS = services.TLSCerts.UserSupplied()
	}
	httpHost, hostErr := newHTTPHost(hostOpts)
	if hostErr != nil {
		return fmt.Errorf("invalid network allowlist %v: %w", allowedCIDRs, hostErr)
	}
	services.HTTPHost = httpHost
	logAllowlist(allowedCIDRs, cfg.Gateway.Host)
	if services.TLSCerts != nil {
		services.HTTPHost.SetCertificateNames(services.TLSCerts.Info().Names())
	}
	if err := services.HTTPHost.ApplyPolicy(cfg.Gateway, services.ChannelManager); err != nil {
		return err
	}
	if err := startWebAuth(services, cfg); err != nil {
		return err
	}
	rebuildSharedHTTPServer(services, "127.0.0.1", cfg.Gateway.EffectivePort(), services.ChannelManager, services.HTTPHost, agentLoop)
	services.HTTPHost.onFatal = fatal.fatalService
	if err := services.HTTPHost.Start(); err != nil {
		return err
	}
	if services.TLSCerts != nil {
		startTLSWatch(services, agentLoop)
	}
	// The TLS API (GET /api/tls, regenerate) reads what this process bound
	// and drives the running certificate manager.
	services.bootListeners = cfg.Gateway.Listeners()
	services.WebServer.APIHandler().SetBootListeners(services.bootListeners)
	services.WebServer.APIHandler().SetTLSManager(services.TLSCerts)
	return nil
}

// startWebAuth sets up the WebUI login: one session store shared by the Auth
// middleware on the listener, the login endpoints and the WebUI channel's
// WebSocket handshake. Created once, like the listener; its poller picks up
// `claw admin` changes to the credentials file for the life of the process.
func startWebAuth(services *gatewayServices, cfg *config.Config) error {
	authStore := middleware.NewAuthStore(admin.Path(cfg.DataDir()))
	services.AuthStore = authStore
	services.HTTPHost.SetAuth(authStore)
	services.WebServer.APIHandler().SetAuth(authStore)
	if err := services.WebServer.APIHandler().SetLockoutExempt(cfg.Gateway.LockoutExempt); err != nil {
		return err
	}
	webui.SetSessionValidator(authStore.HasSession)
	authCtx, stopAuthWatch := context.WithCancel(context.Background())
	services.stopAuthWatch = stopAuthWatch
	go authStore.Watch(authCtx, middleware.CredentialsPollInterval)
	return nil
}

// logHealthEndpoints logs where /health and /ready answer.
func logHealthEndpoints(services *gatewayServices, cfg *config.Config) {
	endpoints := map[string]any{
		"health": "http://" + services.HTTPHost.LoopbackAddr() + "/health",
		"ready":  "http://" + services.HTTPHost.LoopbackAddr() + "/ready",
	}
	if urls := cfg.Gateway.NetworkHTTPURLs(); len(urls) > 0 {
		endpoints["http_network"] = urls
	}
	if urls := cfg.Gateway.HTTPSURLs(); len(urls) > 0 {
		endpoints["https"] = urls
	}
	endpoints["external_url"] = cfg.Gateway.EffectiveExternalURL()
	logger.InfoF("Health endpoints available", endpoints)
}

// newDeviceService creates the device event service on the bus.
func newDeviceService(cfg *config.Config, agentLoop *agent.AgentLoop, msgBus *bus.MessageBus) *devices.Service {
	svc := devices.NewService(devices.Config{
		Enabled:    cfg.Devices.Enabled,
		MonitorUSB: cfg.Devices.MonitorUSB,
		Target:     defaultAgentTarget(agentLoop),
		Alerter:    agentLoop.Alerter(),
	})
	svc.SetBus(msgBus)
	return svc
}

// stopAndCleanupServices stops all services and cleans up resources. The agent
// loop (nil when there is none) is unhooked from the MCP host it stops, so a
// tool refresh between here and the host's restart is a no-op. phases, when
// not nil, logs how long each step took.
func stopAndCleanupServices(
	services *gatewayServices,
	agentLoop *agent.AgentLoop,
	shutdownTimeout time.Duration,
	phases *phaseTimer,
) {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if services.CogmemManager != nil {
		services.CogmemManager.Stop()
		phases.done("memory consolidation stopped")
	}
	if services.MCPServer != nil {
		if agentLoop != nil {
			agentLoop.SetMCPHost(nil)
		}
		if err := services.MCPServer.Shutdown(shutdownCtx); err != nil {
			logger.WarnCF("mcpserver", "MCP server shutdown error", map[string]any{"error": err.Error()})
		}
		phases.done("MCP host stopped")
	}
	markReady(services, false)
	if services.ChannelManager != nil {
		if stopErr := services.ChannelManager.StopAll(shutdownCtx); stopErr != nil {
			logger.WarnCF("channels", "Channel manager shutdown error", map[string]any{"error": stopErr.Error()})
		}
		phases.done("channels stopped")
	}
	if services.DeviceService != nil {
		services.DeviceService.Stop()
		phases.done("device service stopped")
	}
	if services.MountWatcher != nil {
		services.MountWatcher.Stop()
	}
	if services.CronTool != nil {
		services.CronTool.StopListeners()
	}
	if services.CronService != nil {
		services.CronService.Stop()
		phases.done("cron stopped")
	}
	if services.MediaStore != nil {
		// Stop the media store if it's a FileMediaStore with cleanup
		if fms, ok := services.MediaStore.(*media.FileMediaStore); ok {
			fms.Stop()
		}
	}
}

// shutdownGateway performs a complete gateway shutdown. MCP probes stop first,
// so a server the stop signal killed is not respawned. Once the channels have
// stopped delivering messages, the turns in flight are cancelled, so the
// session and MCP close that follows is not left waiting on them.
func shutdownGateway(
	services *gatewayServices,
	agentLoop *agent.AgentLoop,
	provider providers.LLMProvider,
	fullShutdown bool,
) {
	begin := time.Now()
	phases := newPhaseTimer("Shutdown")
	// Forum runs stop first, while the loop still runs: their asks end as a
	// shutdown, not as failed turns, and resume at the next start.
	if services.Forum != nil {
		closeForums(services.Forum)
		phases.done("forums stopped")
	}
	agentLoop.BeginMCPShutdown()

	if cp, ok := provider.(providers.StatefulProvider); ok && fullShutdown {
		cp.Close()
		phases.done("provider closed")
	}

	stopAndCleanupServices(services, agentLoop, gracefulShutdownTimeout, phases)
	agentLoop.Stop()

	if services.stopTLSWatch != nil {
		services.stopTLSWatch()
	}
	if services.stopAuthWatch != nil {
		services.stopAuthWatch()
	}
	if services.HTTPHost != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		if err := services.HTTPHost.Stop(shutdownCtx); err != nil {
			logger.WarnCF("gateway", "Shared HTTP listener shutdown error", map[string]any{"error": err.Error()})
		}
		cancel()
		phases.done("HTTP listeners stopped")
	}

	drainCtx, drainCancel := context.WithTimeout(context.Background(), shutdownDrainBudget)
	agentLoop.Close(drainCtx)
	drainCancel()
	phases.done("agent loop closed")
	if err := audit.Close(); err != nil {
		logger.WarnCF("gateway", "audit log close failed", map[string]any{"error": err.Error()})
	}

	logger.Infof("✓ claw stopped in %.1fs", time.Since(begin).Seconds())
	if fullShutdown {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := agentLoop.Alerter().Close(closeCtx); err != nil {
			logger.WarnCF("gateway", "alerts not fully flushed on shutdown", map[string]any{"error": err.Error()})
		}
		cancel()
	}
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
	stopAndCleanupServices(services, al, serviceShutdownTimeout, nil) //nolint:contextcheck // the old services stop on a fresh bounded context so their shutdown completes even if the run context ends mid-reload; shutdownGateway shares the helper with no context

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
	reloadCtx, reloadCancel := context.WithTimeout(context.WithoutCancel(ctx), providerReloadTimeout)
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
	// The agent loop's config has been updated if this is a reload.
	cfg := al.GetConfig()

	if err := restartCron(runCtx, al, services, msgBus, cfg); err != nil {
		return err
	}

	// Re-create the mount watcher. stopAndCleanupServices stopped the old one, so
	// without this a reload would silently end mount notifications for the rest of
	// the process's life — and every service around it is rebuilt the same way.
	services.MountWatcher = mountwatch.New(al.GetConfig, msgBus, 0, al.Alerter())
	services.MountWatcher.Start() //nolint:contextcheck,nolintlint // background poller whose lifetime is Stop(), not the run context
	logger.InfoC("mountwatch", "Mount watcher restarted")

	stopMediaStore(services.MediaStore)
	services.MediaStore = startMediaStore(cfg)
	al.SetMediaStore(services.MediaStore)
	// Stopped here if the channel manager cannot be built to take it over.
	mediaOwned := true
	defer func() {
		if mediaOwned {
			stopMediaStore(services.MediaStore)
		}
	}()

	var err error
	services.ChannelManager, err = channels.NewManager(cfg, msgBus, services.MediaStore) //nolint:contextcheck // manager construction runs SecMsg account discovery on its own context; the initial setup path builds it the same way with no context in scope
	if err != nil {
		return fmt.Errorf("error recreating channel manager: %w", err)
	}
	mediaOwned = false
	// The device channel is rebuilt with the manager, so without its querier
	// sessionScopeKeyFor falls back to a bogus "main" agent id after the first
	// reload, breaking device/ACP turn routing.
	wireChannelManager(services, al)
	logEnabledChannels(services.ChannelManager)

	// Rebuild the shared mux (channel webhooks, WebUI routes, callback route)
	// and swap it into the long-lived httpHost. The listener is NOT recreated:
	// keeping it alive is what lets WebUI WebSocket connections survive a
	// config reload.
	rebuildSharedHTTPServer(services, "127.0.0.1", cfg.Gateway.EffectivePort(), services.ChannelManager, services.HTTPHost, al) //nolint:contextcheck // the fusion engine is a process-wide singleton built once; its token store opens on a detached context
	warnListenerConfigChanged(services, cfg.Gateway)
	// Names saved since start (extra_names, external_url) reach the running
	// certificate manager, so a regeneration or renewal covers them.
	if services.TLSCerts != nil {
		opts := tlscert.OptionsFromConfig(cfg)
		services.TLSCerts.UpdateNames(opts.ExtraNames, opts.ExternalHost)
	}
	reapplyListenerPolicy(services, cfg)

	if err := services.ChannelManager.StartAll(runCtx); err != nil {
		return fmt.Errorf("error restarting channels: %w", err)
	}
	// rebuildSharedHTTPServer creates a fresh health.Server, which starts
	// not-ready, so readiness is re-asserted after every reload.
	markReady(services, true)
	logger.InfoCF("channels", "Channels restarted", map[string]any{"health": "http://" + services.HTTPHost.LoopbackAddr() + "/health"})

	services.DeviceService = newDeviceService(cfg, al, msgBus)
	if err := services.DeviceService.Start(runCtx); err != nil {
		logger.WarnCF("device", "Failed to restart device service", map[string]any{"error": err.Error()})
	} else if cfg.Devices.Enabled {
		logger.InfoC("device", "Device event service restarted")
	}

	// Set to nil when transcription is disabled.
	transcriber := voice.DetectTranscriberWithAlerter(cfg, al.Alerter())
	al.SetTranscriber(transcriber)
	if transcriber != nil {
		logger.InfoCF("voice", "Transcription re-enabled (agent-level)", map[string]any{"provider": transcriber.Name()})
	} else {
		logger.InfoCF("voice", "Transcription disabled", nil)
	}

	// Reconnect external MCP servers and re-register their tools onto the freshly
	// rebuilt registry BEFORE the host server re-enumerates. ReloadProviderAndConfig
	// builds a new registry (which has no MCP tools, and is not covered by the
	// startup initOnce), so without this a reload silently drops every agent's
	// mcp_* tools and webui edits to mcp_tools would not take effect.
	al.ReinitMCP(runCtx)

	// Restart MCP server — it was shut down as part of stopAndCleanupServices.
	if err := startMCPServer(cfg, al, msgBus, services); err != nil {
		return fmt.Errorf("error restarting MCP server: %w", err)
	}

	return nil
}

// restartCron re-creates and starts the cron service with the new config,
// and re-registers the cron tool with every agent so it is available after
// the registry is rebuilt.
func restartCron(runCtx context.Context, al *agent.AgentLoop, services *gatewayServices, msgBus *bus.MessageBus, cfg *config.Config) error {
	var cronTool *toolschedule.CronTool
	services.CronService, cronTool = setupCronTool(al, msgBus, cfg) //nolint:contextcheck // cron jobs are fired by the scheduler, not by the reload; ExecuteJob runs on a detached context by design, as on the initial setup path
	if cronTool != nil {
		al.RegisterTool(cronTool)
	}
	if err := services.CronService.Start(); err != nil {
		return fmt.Errorf("error restarting cron service: %w", err)
	}
	logger.InfoC("cron", "Cron service restarted")
	services.CronTool = cronTool
	if cronTool != nil {
		cronTool.StartListeners(runCtx)
	}
	return nil
}

// reapplyListenerPolicy pushes the reloaded IP allowlist, trusted proxies,
// Host policy and lockout exemptions onto the live listener and login
// limiter. This is what makes `claw network` a recovery path: an operator
// locked out by an empty allowlist can widen it and be let in within the
// reload interval, without a restart. A rejected value leaves the running
// one in place, so a typo never drops access to loopback.
func reapplyListenerPolicy(services *gatewayServices, cfg *config.Config) {
	if services.HTTPHost != nil {
		allowedCIDRs := cfg.Gateway.EffectiveAllowedCIDRs()
		if err := services.HTTPHost.SetAllowlist(allowedCIDRs); err != nil {
			logger.WarnF("Invalid network allowlist in reloaded config; keeping the previous one", map[string]any{"error": err.Error()})
		} else {
			logAllowlist(allowedCIDRs, cfg.Gateway.Host)
		}
		if err := services.HTTPHost.SetTrustedProxies(cfg.Gateway.TrustedProxies); err != nil {
			logger.WarnF("Invalid trusted proxy list in reloaded config; keeping the previous one", map[string]any{"error": err.Error()})
		}
		// external_url and the LINE webhook path can change on reload; a bad
		// external_url keeps the previous policy rather than dropping to
		// loopback-only.
		if err := services.HTTPHost.ApplyPolicy(cfg.Gateway, services.ChannelManager); err != nil {
			logger.WarnF("Invalid gateway.external_url in reloaded config; keeping the previous host policy", map[string]any{"error": err.Error()})
		}
	}
	// The login limiter outlives the reload like the listener, so the lockout
	// exemption list is swapped into it here. The device channel is rebuilt
	// with the manager and reads the list from the new config itself.
	if services.WebServer != nil {
		if err := services.WebServer.APIHandler().SetLockoutExempt(cfg.Gateway.LockoutExempt); err != nil {
			logger.WarnF("Invalid lockout exemption list in reloaded config; keeping the previous one", map[string]any{"error": err.Error()})
		}
	}
}

// setupCronTool creates the cron service and, if cron is enabled, the CronTool
// that agents use to manage scheduled jobs. Registration of the tool with the
// agent registry is the caller's responsibility so that reloads re-register on
// the freshly-rebuilt registry (matching the pattern used by registerSharedTools).
func setupCronTool(
	agentLoop *agent.AgentLoop,
	msgBus *bus.MessageBus,
	cfg *config.Config,
) (*cron.CronService, *toolschedule.CronTool) {
	cronStorePath := filepath.Join(cfg.CronPath(), "jobs.json")

	// Create cron service
	cronService := cron.NewCronService(cronStorePath, nil)
	cronService.SetAlerter(agentLoop.Alerter())
	if err := cronService.LoadError(); err != nil {
		agentLoop.Alerter().Send(alerter.Alert{
			Title:       "Cron store unreadable",
			Description: cronStorePath + ": no scheduled jobs run, and the next save overwrites the file",
			Details:     err.Error(),
			EventID:     "cron-store",
		})
	}

	// Create CronTool if enabled
	var cronTool *toolschedule.CronTool
	if cfg.Tools.Cron.Enabled {
		cronTool = toolschedule.NewCronTool(cronService, msgBus, agentLoop.GetConfig)
	}

	// Wire watch probes to the owning agent's tool registry, resolved per run so
	// a tool revoked in config stops being probed rather than staying live from
	// whenever the watch was created.
	if cronTool != nil {
		cronTool.SetAgentTools(func(agentID string) *tools.ToolRegistry {
			reg := agentLoop.GetRegistry()
			if reg == nil {
				return nil
			}
			inst, ok := reg.GetConfigured(agentID)
			if !ok || inst == nil {
				return nil
			}
			return inst.Tools
		})
		// A sub-agent (a temporary clone) schedules for the agent it copies.
		cronTool.SetHomeAgent(func(agentID string) string {
			return agentLoop.GetRegistry().HomeID(agentID)
		})
	}

	// Set onJob handler
	if cronTool != nil {
		cronService.SetOnJob(func(job *cron.CronJob) (string, error) {
			return cronTool.ExecuteJob(context.Background(), job)
		})
	}

	return cronService, cronTool
}

// markReady flips the readiness flag the /ready endpoint reports. Guarded
// because the health server is nil until the shared HTTP server is first built,
// and is replaced on every config reload.
func markReady(services *gatewayServices, ready bool) {
	if services != nil && services.HealthServer != nil {
		services.HealthServer.SetReady(ready)
	}
}

// logAllowlist reports the effective IP allowlist at startup and after a reload.
// Binding off-box without one is almost always a surprise: the listener accepts
// the connection and the allowlist then rejects it, which looks like the port
// being closed rather than a configuration choice — so say so, and say how to
// fix it without hunting through config.json.
func logAllowlist(allowedCIDRs []string, host string) {
	if len(allowedCIDRs) > 0 {
		logger.InfoF("Network allowlist active", map[string]any{"allowed_cidrs": allowedCIDRs, "loopback": "always allowed"})
		return
	}
	logger.InfoF("Network allowlist active", map[string]any{"allowed_cidrs": "none (loopback only)"})
	if host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return
	}
	// "*" rather than 0.0.0.0/0: the latter is an IPv4 prefix and still refuses
	// IPv6 clients, which on a dual-stack host reads as the allowlist being
	// broken. See middleware.AllowAnyAddress.
	logger.WarnF("The WebUI/API is bound to a network address but the network allowlist is empty, so only loopback will be served. "+
		"Run `"+internal.BinaryName+" network` to allow the private LAN ranges, "+
		"`"+internal.BinaryName+" network <cidr>` for a specific subnet, or "+
		"`"+internal.BinaryName+" network any` for any address (note 0.0.0.0/0 covers IPv4 only; use \"*\"). "+
		"A running "+internal.BinaryName+" applies the change on its next config reload, about 15 seconds.",
		map[string]any{"host": host})
}

// tlsOptions maps the gateway config onto the certificate manager's options,
// with the gateway's alerter for reload and expiry alerts.
func tlsOptions(cfg *config.Config, a alerter.Alerter) tlscert.Options {
	opts := tlscert.OptionsFromConfig(cfg)
	opts.Alerter = a
	return opts
}

// startTLSWatch logs the certificate in use and starts the manager's file
// watcher (poll every minute, expiry check daily). A swapped-in certificate
// re-applies the Host policy so the new names are answered to; the device
// gateway's own Host allowlist follows on the next config reload.
func startTLSWatch(services *gatewayServices, agentLoop *agent.AgentLoop) {
	info := services.TLSCerts.Info()
	logger.InfoCF("gateway", "HTTPS listener certificate", map[string]any{
		"source":      string(info.Source),
		"cert_file":   info.CertFile,
		"names":       info.Names(),
		"not_after":   info.NotAfter.Format(time.RFC3339),
		"fingerprint": info.Fingerprint,
		"hsts":        services.TLSCerts.UserSupplied(),
	})
	services.TLSCerts.OnChange(func(info tlscert.Info) {
		if services.AuthStore != nil {
			services.AuthStore.Reload()
		}
		services.HTTPHost.SetCertificateNames(info.Names())
		if err := services.HTTPHost.ApplyPolicy(agentLoop.GetConfig().Gateway, services.ChannelManager); err != nil {
			logger.WarnCF("gateway", "Host policy not re-applied after certificate change", map[string]any{"error": err.Error()})
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	services.stopTLSWatch = cancel
	services.TLSCerts.Watch(ctx, tlscert.DefaultPollInterval)
}

// warnListenerConfigChanged says so when a reloaded config would bind the
// listeners differently. They are created once at boot, so the change waits
// for a restart; without this line the operator sees the edit accepted and
// nothing happen.
func warnListenerConfigChanged(services *gatewayServices, gw config.GatewayConfig) {
	if services.HTTPHost == nil {
		return
	}
	if gw.Listeners() != services.bootListeners {
		logger.WarnCF("gateway", "gateway.host, port, tls_port, tls.mode or the certificate files changed; the listeners are bound at start, so restart "+internal.BinaryName+" to apply", nil)
	}
}
