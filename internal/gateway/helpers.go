package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/PivotLLM/cogmem/consolidate"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/alerts"
	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	_ "github.com/PivotLLM/ClawEh/channels/device"
	_ "github.com/PivotLLM/ClawEh/channels/discord"
	_ "github.com/PivotLLM/ClawEh/channels/line"
	_ "github.com/PivotLLM/ClawEh/channels/matrix"
	_ "github.com/PivotLLM/ClawEh/channels/secmsg"
	_ "github.com/PivotLLM/ClawEh/channels/slack"
	_ "github.com/PivotLLM/ClawEh/channels/telegram"
	"github.com/PivotLLM/ClawEh/channels/webui"
	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/cron"
	"github.com/PivotLLM/ClawEh/devices"
	"github.com/PivotLLM/ClawEh/forum"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/health"
	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/internal/layout"
	"github.com/PivotLLM/ClawEh/internal/perms"
	"github.com/PivotLLM/ClawEh/internal/pidfile"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/mcpserver"
	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/mountwatch"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/servicetoken"
	"github.com/PivotLLM/ClawEh/tools"
	toolschedule "github.com/PivotLLM/ClawEh/tools/schedule"
	"github.com/PivotLLM/ClawEh/utils"
	"github.com/PivotLLM/ClawEh/voice"
	webserver "github.com/PivotLLM/ClawEh/web/backend"
	webapi "github.com/PivotLLM/ClawEh/web/backend/api"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

// Timeout constants for service operations
const (
	serviceRestartTimeout   = 30 * time.Second
	serviceShutdownTimeout  = 30 * time.Second
	providerReloadTimeout   = 30 * time.Second
	gracefulShutdownTimeout = 15 * time.Second
	// shutdownDrainBudget is shared by closing the agent sessions and the MCP
	// servers at exit; what is still busy after it is abandoned. Keeps the
	// whole stop well inside systemd's TimeoutStopSec.
	shutdownDrainBudget = 10 * time.Second
)

// phaseTimer logs how long each step of a stop took, one INFO line per step.
// A nil *phaseTimer logs nothing.
type phaseTimer struct {
	prefix string
	start  time.Time
}

func newPhaseTimer(prefix string) *phaseTimer {
	return &phaseTimer{prefix: prefix, start: time.Now()}
}

// done logs what finished and how long it took since the previous step.
func (p *phaseTimer) done(what string) {
	if p == nil {
		return
	}
	logger.Infof("%s: %s in %.1fs", p.prefix, what, time.Since(p.start).Seconds())
	p.start = time.Now()
}

// openConfigStore seeds a default config at path on first run and opens the
// store on it. The file is read once, so each load-time warning (an unknown
// key, a legacy setting) is logged once per start.
func openConfigStore(path string) (*config.Store, error) {
	internal.SeedConfig(path)
	return config.NewStore(path)
}

// runtimeConfig returns the private copy of the store's configuration the
// gateway runs on. References to models that do not exist are first removed
// from config.json through the store (pruneStoredModelReferences); if that
// write fails the reason is logged at WARN and they are dropped from the
// running copy only, never aborting startup or a reload. Invalid
// providers/models (a stale/unknown protocol, a model pointing at a missing
// provider) are then dropped from the copy with a WARN and the rest is kept,
// rather than failing over one bad entry; those stay in the file so they can
// be repaired via the WebUI, and a reference to such a model is skipped in the
// copy only. Human agents breaking their rules are disabled in the copy and a
// human model is dropped from every site where a model must answer
// (config.PruneHumanProblems). What was removed or skipped is returned for
// modelRefAlerts. Boot,
// the config watcher and the forced reload all build their runtime copy here,
// so they cannot drift apart.
func runtimeConfig(store *config.Store) (*config.Config, modelRefPrune, error) {
	var prune modelRefPrune
	removed, perr := pruneStoredModelReferences(store)
	if perr != nil {
		logger.WarnCF("gateway", "could not remove references to unknown models from the config file; skipping them in the running config only", map[string]any{
			"path":  store.Path(),
			"error": perr.Error(),
		})
	}
	prune.removed = removed
	cfg, err := store.Current().Clone()
	if err != nil {
		return nil, modelRefPrune{}, err
	}
	if dp, dm := cfg.PruneInvalid(); dp > 0 || dm > 0 {
		logger.WarnCF("gateway", "ignored invalid config entries; continuing with the rest", map[string]any{
			"providers_dropped": dp,
			"models_dropped":    dm,
		})
	}
	// Human agents that break their rules are not run, and a human model is
	// dropped wherever a model must answer; the file keeps both for repair,
	// and the Agents page shows why.
	for _, p := range cfg.PruneHumanProblems() {
		logger.WarnCF("gateway", "ignored human-agent configuration", map[string]any{
			"agent":  p.Agent,
			"model":  p.Model,
			"reason": p.Message,
		})
	}
	prune.skipped = pruneModelReferences(cfg)
	return cfg, prune, nil
}

// newMergedWebServer constructs the in-process WebUI server bundle (API
// handler + embedded SPA) on the gateway's live config store and the merged
// binary's actual listen settings. The gateway host/port and IP allowlist in
// cfg.Gateway are the single source of truth.
func newMergedWebServer(store *config.Store, cfg *config.Config) *webserver.Server {
	// Public: a listener is reachable off-box, so the API advertises the
	// request's own host rather than the bind host.
	publicBind := cfg != nil && cfg.Gateway.ReachableOffBox()
	port := 0
	if cfg != nil {
		port = cfg.Gateway.EffectivePort()
	}
	srv := webserver.New(webserver.Options{
		Store:      store,
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
	// Route spawnllm's provider/dispatch logs into ClawEh's logger.
	installSpawnllmLogging()
	cogmemhost.InstallLogging()

	lockFile, err := acquireLock(baseDir)
	if err != nil {
		return fmt.Errorf("startup aborted: %w", err)
	}
	defer releaseLock(lockFile)

	logger.InfoCF("gateway", "Starting", map[string]any{"app": app.Name(), "version": app.Version()})

	configPath := internal.GetConfigPath()
	if permErr := enforceDataDirPerms(baseDir, configPath); permErr != nil {
		return permErr
	}
	// The store is the live configuration from here on, shared with the WebUI
	// API so a save through it and the gateway's own reads never disagree.
	store, err := openConfigStore(configPath)
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}
	// The runtime works on a pruned private copy; the store keeps the full
	// on-disk config so invalid entries can be repaired through the WebUI.
	// References to models that no longer exist are removed from the file.
	cfg, bootDanglingRefs, err := runtimeConfig(store)
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}
	// Shared by boot, the watcher and the forced reload so a missing-model
	// reference alerts once per process, not once per reload.
	refAlerts := &modelRefAlerts{}

	// Lay out the data directory before anything
	// below opens the files it holds.
	layout.Prepare(cfg)

	// Re-apply logging config (debug flag overrides level).
	if cfg.Logging.File {
		if logErr := logger.EnableFileLogging(logPath, cfg.Logging.JSON); logErr != nil {
			logger.WarnCF("gateway", "Failed to enable file logging", map[string]any{"path": logPath, "error": logErr.Error()})
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

	// Operator alerts: parked models, unreachable MCP servers, channels that
	// give up, failed jobs and reloads. Closed in shutdownGateway. Installed
	// before anything that can raise one at startup: the dispatcher below
	// alerts for a CLI provider running without its bypass flag, and an alert
	// sent before Set reaches the no-op default and is lost.
	operatorAlerter, alertsPath := newAlerter(baseDir)
	alerts.Set(operatorAlerter)
	refAlerts.report(operatorAlerter, bootDanglingRefs)

	dispatcher := providers.NewProviderDispatcher(cfg)
	openAuditLog(baseDir)
	// A core service that dies after startup takes the process down through
	// here, so the service manager restarts it; see fatal.go.
	fatal := newFatalNotifier(operatorAlerter)
	msgBus := bus.NewMessageBus()
	// The forum service exists before the loop builds the agents' tools.
	forumHost := agent.NewForumHost()
	forumSvc := newForumService(forumHost)
	// Closed here on any return before the services own it; from then on
	// shutdownGateway closes it.
	forumOwned := true
	defer func() {
		if forumOwned {
			closeForums(forumSvc)
		}
	}()
	agentLoop, err := agent.NewAgentLoop(cfg, msgBus, provider, dispatcher, agent.OwnsDataDir())
	if err != nil {
		return fmt.Errorf("error creating agent loop: %w", err)
	}
	forumHost.Bind(agentLoop)
	agentLoop.SetAlerter(operatorAlerter)

	dumpsDir := filepath.Join(internal.GetClawHome(), "logs", "dumps")
	agentLoop.SetDumpsDir(dumpsDir)

	startupInfo := agentLoop.GetStartupInfo()
	if len(startupInfo) == 0 {
		return errors.New("no default agent configured — add at least one entry to agents.list in your config")
	}
	var toolsInfo, skillsInfo map[string]any
	if v, ok := startupInfo["tools"].(map[string]any); ok {
		toolsInfo = v
	}
	if v, ok := startupInfo["skills"].(map[string]any); ok {
		skillsInfo = v
	}
	logger.InfoCF("agent", "Agent initialized",
		map[string]any{
			"tools_count":      toolsInfo["count"],
			"skills_total":     skillsInfo["total"],
			"skills_available": skillsInfo["available"],
		})

	// Record the pid so `claw status` can find THIS instance. Scoped to the data
	// directory because one binary runs several gateways on a host, and a CLI
	// command already resolves CLAW_HOME to find the config. Written before the
	// services start, so an instance that is accepting connections is always
	// visible through the file (the usual daemon order); the deferred removal
	// still cleans up if startup fails below. Non-fatal: a gateway that cannot
	// write the file should still serve.
	if werr := pidfile.Write(cfg.DataDir()); werr != nil {
		logger.WarnCF("gateway", "could not write the pid file; `claw status` will not see this instance",
			map[string]any{"error": werr.Error()})
	}
	defer pidfile.Remove(cfg.DataDir())

	// Setup and start all services
	services, err := setupAndStartServices(cfg, agentLoop, msgBus, configPath, store, fatal)
	if err != nil {
		return err
	}
	services.Forum = forumSvc
	forumOwned = false
	// The Logs page tails the alerts file the alerter writes.
	services.WebServer.APIHandler().SetAlertsPath(alertsPath)
	services.WebServer.APIHandler().SetAlerter(agentLoop.Alerter())
	// POST /api/system/restart: a clean shutdown with a non-zero exit, for the
	// service manager to start the gateway again.
	services.WebServer.APIHandler().SetRestart(fatal.requestRestart)

	logger.InfoF("claw started", map[string]any{"http": services.HTTPHost.HTTPAddrs(), "https": services.HTTPHost.HTTPSAddrs()})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Daily log rotation: roll claw.log/error.log at local midnight (and on
	// startup if claw.log predates today), pruning archives past retention.
	startLogRotation(ctx, logPath, cfg.Logging.RetentionDays, agentLoop.Alerter())

	go func() {
		if runErr := agentLoop.Run(ctx); runErr != nil {
			fatal.fatalService("agent-loop", runErr)
		}
	}()
	recoverForums(ctx, forumSvc, forumHost.Scopes, agentLoop.Started(), nil)

	// Setup config file watcher for hot reload
	reloadInterval := cfg.ConfigReloadInterval()
	logger.InfoF("Config reload watcher", map[string]any{"interval": reloadInterval.String()})
	configReloadChan, stopWatch, markConfigApplied := setupConfigWatcherPolling(store, reloadInterval,
		time.Duration(global.ConfigReloadDebounceSeconds)*time.Second, debug, agentLoop.Alerter(), refAlerts)
	defer stopWatch()

	// Force-reload channel: lets POST /api/gateway/reload apply config changes
	// immediately, bypassing the mtime-debounce (so the setup wizard / WebUI saves
	// don't leave the user in a ~10-15s window where the new config isn't live).
	// The trigger sends a response channel and blocks until the reload completes.
	forceReload := make(chan chan error, 1)
	if services.WebServer != nil {
		services.WebServer.APIHandler().SetReloadTrigger(func() error {
			done := make(chan error, 1)
			select {
			case forceReload <- done:
				return <-done
			case <-time.After(5 * time.Second):
				return errors.New("claw is busy; reload not accepted")
			}
		})
	}

	// Watch the service-token state file so `claw token` changes activate live
	// (writes are atomic, so no debounce is needed).
	svcTokenChan, stopSvcWatch := setupFileChangeWatcher(servicetoken.Path(cfg.DataDir()), reloadInterval)
	defer stopSvcWatch()

	// SIGTERM as well as SIGINT. systemd sends SIGTERM to stop a unit, and its
	// default disposition kills the process outright — so registering only
	// os.Interrupt meant every `systemctl stop` and `restart` skipped the
	// shutdown below entirely: channels were never stopped cleanly, in-flight
	// work was never drained, and gracefulShutdownTimeout was dead code on the
	// only path production actually uses.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Main event loop - wait for signals or config/token changes
	for {
		select {
		case <-sigChan:
			logger.Info("Shutting down...")
			shutdownGateway(services, agentLoop, provider, true)
			return nil

		case failure := <-fatal.failed:
			// Same shutdown as SIGTERM; the failure becomes the exit reason and
			// NewGatewayCommand exits non-zero on it. fatalService has armed a
			// timer that exits regardless should this hang.
			logger.Info("Shutting down after a core service stopped...")
			shutdownGateway(services, agentLoop, provider, true)
			return failure

		case restart := <-fatal.restart:
			// POST /api/system/restart: the same shutdown, and a non-zero exit
			// so the service manager starts the gateway again.
			logger.Info("Shutting down to restart...")
			shutdownGateway(services, agentLoop, provider, true)
			return restart

		case newCfg := <-configReloadChan:
			err := handleConfigReload(ctx, agentLoop, newCfg, &provider, services, msgBus)
			if err != nil {
				logger.Errorf("Config reload failed: %v", err)
				agentLoop.Alerter().Send(alerter.Alert{
					Title:       "Config reload failed",
					Description: "the reload was aborted part way; check the claw log, services may not all be running",
					Details:     err.Error(),
					EventID:     "config",
				})
			}

		case done := <-forceReload:
			logger.Info("Forced config reload requested via API")
			// Taken before reading, so a change written while this reload
			// runs is newer than what the watcher is told was applied.
			applied := configFileStateOf(store.Path())
			if _, lerr := store.Reload(); lerr != nil {
				done <- lerr
				break
			}
			newCfg, dangling, cerr := runtimeConfig(store)
			if cerr != nil {
				done <- cerr
				break
			}
			refAlerts.report(agentLoop.Alerter(), dangling)
			rerr := handleConfigReload(ctx, agentLoop, newCfg, &provider, services, msgBus)
			if rerr == nil {
				// Tell the watcher we've applied the current file so it doesn't
				// fire a second, disruptive reload for the same change.
				markConfigApplied(applied)
			}
			done <- rerr

		case <-svcTokenChan:
			logger.Info("🔑 Service-token file changed, reloading service tokens...")
			syncServiceTokensFromDisk(agentLoop.GetConfig(), agentLoop, services.MCPServer)
		}
	}
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

	// Ensure the shared "common" directory exists so the common_* tools have a
	// place to read/write. Idempotent.
	if commonDir := cfg.ResolveCommonDir(); commonDir != "" {
		if err := os.MkdirAll(commonDir, 0o700); err != nil {
			logger.WarnCF("gateway", "Failed to create common directory", map[string]any{"path": commonDir, "error": err.Error()})
		}
	}

	// Setup cron tool and service
	var cronTool *toolschedule.CronTool
	services.CronService, cronTool = setupCronTool(agentLoop, msgBus, cfg)
	if cronTool != nil {
		agentLoop.RegisterTool(cronTool)
	}
	if err := services.CronService.Start(); err != nil {
		return nil, fmt.Errorf("error starting cron service: %w", err)
	}
	logger.InfoC("cron", "Cron service started")
	services.CronTool = cronTool
	if cronTool != nil {
		cronTool.StartListeners(context.Background())
	}

	// Watch notify-enabled external mounts for new files (cron-style notices).
	services.MountWatcher = mountwatch.New(agentLoop.GetConfig, msgBus, 0, agentLoop.Alerter())
	services.MountWatcher.Start()
	logger.InfoC("mountwatch", "Mount watcher started")

	// Stage downloaded media under the instance data dir (e.g. ~/.claw/media,
	// /opt/claw/media) rather than shared /tmp, so files stay app-owned and two
	// instances on one host can't collide on a single dir.
	utils.SetMediaStagingDir(filepath.Join(cfg.DataDir(), "media"))

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
	services.ChannelManager.SetAlerter(agentLoop.Alerter())

	// Inject channel manager and media store into agent loop
	agentLoop.SetChannelManager(services.ChannelManager)
	agentLoop.SetMediaStore(services.MediaStore)

	// Let the device gateway answer operator-client reads (agents.list, chat.history).
	injectDeviceAgentQuerier(services.ChannelManager, agentLoop)
	// Let channels.device.tls serve the HTTPS listener's certificate.
	injectDeviceTLS(services.ChannelManager, services.TLSCerts)

	// Wire up voice transcription if a supported provider is configured.
	if transcriber := voice.DetectTranscriberWithAlerter(cfg, agentLoop.Alerter()); transcriber != nil {
		agentLoop.SetTranscriber(transcriber)
		logger.InfoCF("voice", "Transcription enabled (agent-level)", map[string]any{"provider": transcriber.Name()})
	}

	enabledChannels := services.ChannelManager.GetEnabledChannels()
	if len(enabledChannels) > 0 {
		logger.InfoCF("channels", "Channels enabled", map[string]any{"channels": enabledChannels})
	} else {
		logger.WarnC("channels", "No channels enabled")
	}

	// Setup the shared listeners: plain HTTP on gateway.host (loopback by
	// default) and HTTPS where gateway.tls.mode places it. They are owned by
	// httpHost and stay up across config reloads; only the handler mux is
	// swapped on reload, so WebUI WebSocket connections and channel webhooks
	// survive a Manager rebuild. See rebuildSharedHTTPServer for the swap seam.
	services.WebServer = newMergedWebServer(store, cfg)
	// Share the agent loop's named message-token store with the WebUI API so
	// mint/revoke operations mutate the exact instance the message route validates
	// against (no reload needed for a token to activate/deactivate).
	services.WebServer.APIHandler().SetMessageTokenLoop(agentLoop)
	// DELETE /api/sessions releases a live session before erasing its archive.
	services.WebServer.APIHandler().SetSessionReleaser(agentLoop.ReleaseSession)
	// Expose the live outbound-MCP connection state to the WebUI MCP page. Reads the
	// same manager the agent loop owns (reused in place across reloads).
	services.WebServer.APIHandler().SetMCPStatusLoop(agentLoop)
	// Let the WebUI QR pairing panel reach a live secmsg channel by name so it can
	// drive device linking on the running instance (resolved lazily to survive a
	// Manager rebuild on reload).
	services.WebServer.APIHandler().SetSecMsgLinker(func(name string) (webapi.SecMsgLinker, bool) {
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
	// device channel (resolved per call, like the linker above).
	services.WebServer.APIHandler().SetDeviceDisconnector(func(deviceID string) {
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
	// IP allowlist for the shared HTTP port. Empty means loopback only (see
	// GatewayConfig.EffectiveAllowedCIDRs), so the no-auth WebUI grants no
	// off-box access until an allowlist is configured, whatever the bind address.
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
		return nil, fmt.Errorf("invalid network allowlist %v: %w", allowedCIDRs, hostErr)
	}
	services.HTTPHost = httpHost
	logAllowlist(allowedCIDRs, cfg.Gateway.Host)
	if services.TLSCerts != nil {
		services.HTTPHost.SetCertificateNames(services.TLSCerts.Info().Names())
	}
	if err := services.HTTPHost.ApplyPolicy(cfg.Gateway, services.ChannelManager); err != nil {
		return nil, err
	}
	// WebUI login: one session store shared by the Auth middleware on the
	// listener, the login endpoints and the WebUI channel's WebSocket
	// handshake. Created once, like the listener; the poller picks up `claw
	// admin` changes to the credentials file for the life of the process.
	authStore := middleware.NewAuthStore(admin.Path(cfg.DataDir()))
	services.AuthStore = authStore
	services.HTTPHost.SetAuth(authStore)
	services.WebServer.APIHandler().SetAuth(authStore)
	if err := services.WebServer.APIHandler().SetLockoutExempt(cfg.Gateway.LockoutExempt); err != nil {
		return nil, err
	}
	webui.SetSessionValidator(authStore.HasSession)
	authCtx, stopAuthWatch := context.WithCancel(context.Background())
	services.stopAuthWatch = stopAuthWatch
	go authStore.Watch(authCtx, middleware.CredentialsPollInterval)
	rebuildSharedHTTPServer(services, "127.0.0.1", cfg.Gateway.EffectivePort(), services.ChannelManager, services.HTTPHost, agentLoop)
	services.HTTPHost.onFatal = fatal.fatalService
	if err := services.HTTPHost.Start(); err != nil {
		return nil, err
	}
	if services.TLSCerts != nil {
		startTLSWatch(services, agentLoop)
	}
	// The TLS API (GET /api/tls, regenerate) reads what this process bound
	// and drives the running certificate manager.
	services.bootListeners = cfg.Gateway.Listeners()
	services.WebServer.APIHandler().SetBootListeners(services.bootListeners)
	services.WebServer.APIHandler().SetTLSManager(services.TLSCerts)

	if err := services.ChannelManager.StartAll(context.Background()); err != nil {
		return nil, fmt.Errorf("error starting channels: %w", err)
	}

	// The listener is up and the channels are running, which is what /ready
	// means. It has to be set here rather than left to health.Server.Start():
	// that method marks ready as a side effect of starting the health server's
	// OWN listener, and the merged binary never uses it — the handlers are
	// registered onto the shared mux instead. Without this, /ready answered 503
	// for the entire life of the process.
	markReady(services, true)

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

	// Setup the device service
	services.DeviceService = devices.NewService(devices.Config{
		Enabled:    cfg.Devices.Enabled,
		MonitorUSB: cfg.Devices.MonitorUSB,
		Target:     defaultAgentTarget(agentLoop),
		Alerter:    agentLoop.Alerter(),
	})
	services.DeviceService.SetBus(msgBus)
	if err := services.DeviceService.Start(context.Background()); err != nil {
		logger.ErrorCF("device", "Error starting device service", map[string]any{"error": err.Error()})
	} else if cfg.Devices.Enabled {
		logger.InfoC("device", "Device event service started")
	}

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
		return nil, err
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

	return services, nil
}

// mcpVisibilityList resolves a per-endpoint tools/list visibility filter. Empty
// means "advertise the full union of every agent's allowed tools" ("*") — parity
// with the internal API path, gated per-agent at execution time (tools/call
// resolves the session_token and enforces that agent's config + ACL). A non-empty
// list coarsely narrows what the endpoint advertises; to expose NO tools, disable
// mcp_host instead.
func mcpVisibilityList(patterns []string) []string {
	if len(patterns) > 0 {
		return patterns
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
	defaultAgent := agentLoop.GetRegistry().Default()
	if defaultAgent == nil || defaultAgent.Tools == nil {
		logger.WarnC("mcpserver", "MCP host enabled but no default agent registry available — skipping start")
		return nil
	}

	agentRegistries := make(map[string]*tools.ToolRegistry)
	agentWorkspaces := make(map[string]string)
	for _, agentID := range agentLoop.GetRegistry().List() {
		a, ok := agentLoop.GetRegistry().Get(agentID)
		if !ok || a.Tools == nil {
			continue
		}
		agentRegistries[agentID] = a.Tools
		agentWorkspaces[agentID] = a.Workspace
	}

	if services.SessionTokens == nil {
		services.SessionTokens = mcpserver.NewSessionTokenStore()
		// A sub-agent clone's late async results go to its source agent.
		services.SessionTokens.SetHomeResolver(func(agentID string) string {
			reg := agentLoop.GetRegistry()
			if !reg.IsTemp(agentID) {
				return ""
			}
			return reg.HomeID(agentID)
		})
		agentLoop.SetSessionTokenIssuer(services.SessionTokens)
	}

	srv, err := mcpserver.New(
		mcpserver.WithSessionTokenStore(services.SessionTokens),
		mcpserver.WithAgentRegistries(agentRegistries),
		// A sub-agent clone on a CLI provider calls its tools back through the
		// host under its own id; clones are created after the host starts.
		mcpserver.WithAgentLookup(func(agentID string) (*tools.ToolRegistry, bool) {
			reg := agentLoop.GetRegistry()
			if !reg.IsTemp(agentID) {
				return nil, false
			}
			a, ok := reg.Get(agentID)
			if !ok || a.Tools == nil {
				return nil, false
			}
			return a.Tools, true
		}),
		mcpserver.WithAgentWorkspaces(agentWorkspaces),
		mcpserver.WithListen(cfg.MCPHost.Listen),
		mcpserver.WithEndpointPath(cfg.MCPHost.EndpointPath),
		mcpserver.WithInternalAllowlist(mcpVisibilityList(cfg.MCPHost.InternalTools)),
		mcpserver.WithExternalAllowlist(mcpVisibilityList(cfg.MCPHost.ExternalTools)),
		mcpserver.WithMessageBus(msgBus),
		mcpserver.WithToolActivityNotifier(agentLoop.ToolActivityLine),
		mcpserver.WithAlerter(agentLoop.Alerter()),
		mcpserver.WithOnServeError(services.fatal.handlerFor("mcpserver")),
	)
	if err != nil {
		return fmt.Errorf("error creating MCP server: %w", err)
	}
	if err := srv.Start(); err != nil {
		return fmt.Errorf("error starting MCP server: %w", err)
	}
	services.MCPServer = srv
	// The host catalogue follows the agent registries from here on: when an
	// external MCP server's tools are re-registered, the loop asks the host to
	// refresh, so a renamed tool reaches external clients without a restart.
	agentLoop.SetMCPHost(srv)

	if testTok := os.Getenv("CLAW_MCP_TEST_TOKEN"); testTok != "" {
		defaultAgentID := agentLoop.GetRegistry().DefaultID()
		if defaultAgentID == "" {
			logger.WarnC("mcpserver", "CLAW_MCP_TEST_TOKEN set but no default agent found — skipping registration")
		} else {
			if da, ok := agentLoop.GetRegistry().Get(defaultAgentID); ok && da != nil {
				archiveDir := da.SessionsDir()
				srv.SessionTokens().Register(testTok, defaultAgentID, "test-session", archiveDir)
				logger.InfoCF("mcpserver", "Test session token registered",
					map[string]any{"agent": defaultAgentID})
			}
		}
	}

	// Load persisted long-lived service tokens (claw token CLI) into the store.
	// Runs at boot and on every config reload (this function rebuilds the server);
	// the file watcher in the main loop also re-syncs on demand.
	syncServiceTokensFromDisk(cfg, agentLoop, srv)

	logger.InfoCF("mcpserver", "MCP host started",
		map[string]any{
			"listen":       srv.Listen(),
			"endpoint":     srv.EndpointPath(),
			"auto_enabled": autoStarted,
		})

	return nil
}

// syncServiceTokensFromDisk loads the persisted per-agent service tokens and
// reconciles them into the live store (registers present, revokes removed),
// each bound to its agent's dedicated headless service session. Unknown agents
// are skipped. Used at boot and by the service-token file watcher, so a
// `claw token` change activates without a restart. A missing/empty file is
// normal (it clears any previously-loaded service tokens).
func syncServiceTokensFromDisk(cfg *config.Config, agentLoop *agent.AgentLoop, srv *mcpserver.MCPServer) {
	if srv == nil || cfg == nil {
		return
	}
	path := servicetoken.Path(cfg.DataDir())
	tokens, err := servicetoken.Load(path)
	if err != nil {
		logger.WarnCF("mcpserver", "failed to load service tokens; skipping",
			map[string]any{"path": path, "error": err.Error()})
		agentLoop.Alerter().Send(alerter.Alert{
			Title:       "Service tokens not loaded",
			Description: path + ": external MCP clients using `claw token` credentials are rejected (or keep the previously loaded set) until the file is fixed",
			Details:     err.Error(),
			EventID:     "service-tokens",
		})
		return
	}
	srv.SessionTokens().SyncServiceTokens(tokens, func(agentID string) string {
		da, ok := agentLoop.GetRegistry().GetConfigured(agentID)
		if !ok || da == nil {
			logger.WarnCF("mcpserver", "service token for unknown agent; skipping",
				map[string]any{"agent": agentID})
			return ""
		}
		return da.SessionsDir()
	})
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
	// Get current config from agent loop (which has been updated if this is a reload)
	cfg := al.GetConfig()

	// Re-create and start cron service with new config, then re-register the
	// cron tool with all agents so it is available after the registry is rebuilt.
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

	// Re-create the mount watcher. stopAndCleanupServices stopped the old one, so
	// without this a reload would silently end mount notifications for the rest of
	// the process's life — and every service around it is rebuilt the same way.
	services.MountWatcher = mountwatch.New(al.GetConfig, msgBus, 0, al.Alerter())
	services.MountWatcher.Start() //nolint:contextcheck,nolintlint // background poller whose lifetime is Stop(), not the run context
	logger.InfoC("mountwatch", "Mount watcher restarted")

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
	services.ChannelManager, err = channels.NewManager(cfg, msgBus, services.MediaStore) //nolint:contextcheck // manager construction runs SecMsg account discovery on its own context; the initial setup path builds it the same way with no context in scope
	if err != nil {
		// Stop the media store if it's a FileMediaStore with cleanup
		if fms, ok := services.MediaStore.(*media.FileMediaStore); ok {
			fms.Stop()
		}
		return fmt.Errorf("error recreating channel manager: %w", err)
	}
	services.ChannelManager.SetAlerter(al.Alerter())
	al.SetChannelManager(services.ChannelManager)

	// Re-inject the device agent querier: the channel manager (and thus the device
	// channel) is rebuilt on every reload, so without this the device channel loses
	// its querier after the first config reload and sessionScopeKeyFor falls back to a
	// bogus "main" agent id — breaking device/ACP turn routing.
	injectDeviceAgentQuerier(services.ChannelManager, al)
	injectDeviceTLS(services.ChannelManager, services.TLSCerts)

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
	rebuildSharedHTTPServer(services, "127.0.0.1", cfg.Gateway.EffectivePort(), services.ChannelManager, services.HTTPHost, al) //nolint:contextcheck // the fusion engine is a process-wide singleton built once; its token store opens on a detached context
	warnListenerConfigChanged(services, cfg.Gateway)
	// Names saved since start (extra_names, external_url) reach the running
	// certificate manager, so a regeneration or renewal covers them.
	if services.TLSCerts != nil {
		opts := tlscert.OptionsFromConfig(cfg)
		services.TLSCerts.UpdateNames(opts.ExtraNames, opts.ExternalHost)
	}

	// Re-apply the IP allowlist on the live listener. This is what makes
	// `claw network` a recovery path: an operator locked out by an empty
	// allowlist can widen it and be let in within the reload interval, without a
	// restart. A rejected allowlist leaves the running one in place — a typo must
	// not silently drop access to loopback.
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
		// Same for the Host and cross-origin policy: external_url and the LINE
		// webhook path can change on reload, and a bad external_url keeps the
		// previous policy rather than dropping to loopback-only.
		if err := services.HTTPHost.ApplyPolicy(cfg.Gateway, services.ChannelManager); err != nil {
			logger.WarnF("Invalid gateway.external_url in reloaded config; keeping the previous host policy", map[string]any{"error": err.Error()})
		}
	}
	// The login limiter outlives the reload like the listener, so the lockout
	// exemption list is swapped into it here. The device channel is rebuilt
	// above and reads the list from the new config itself.
	if services.WebServer != nil {
		if err := services.WebServer.APIHandler().SetLockoutExempt(cfg.Gateway.LockoutExempt); err != nil {
			logger.WarnF("Invalid lockout exemption list in reloaded config; keeping the previous one", map[string]any{"error": err.Error()})
		}
	}

	if err := services.ChannelManager.StartAll(runCtx); err != nil {
		return fmt.Errorf("error restarting channels: %w", err)
	}
	// rebuildSharedHTTPServer creates a fresh health.Server, which starts
	// not-ready, so readiness is re-asserted after every reload.
	markReady(services, true)
	logger.InfoCF("channels", "Channels restarted", map[string]any{"health": "http://" + services.HTTPHost.LoopbackAddr() + "/health"})

	// Re-create device service with new config
	services.DeviceService = devices.NewService(devices.Config{
		Enabled:    cfg.Devices.Enabled,
		MonitorUSB: cfg.Devices.MonitorUSB,
		Target:     defaultAgentTarget(al),
		Alerter:    al.Alerter(),
	})
	services.DeviceService.SetBus(msgBus)
	if err := services.DeviceService.Start(runCtx); err != nil {
		logger.WarnCF("device", "Failed to restart device service", map[string]any{"error": err.Error()})
	} else if cfg.Devices.Enabled {
		logger.InfoC("device", "Device event service restarted")
	}

	// Wire up voice transcription with new config
	transcriber := voice.DetectTranscriberWithAlerter(cfg, al.Alerter())
	al.SetTranscriber(transcriber) // This will set it to nil if disabled
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

// enforceDataDirPerms makes CLAW_HOME private before anything in it is read:
// the directory and its secret-bearing files are tightened, and a config file
// other users can read is a refusal to start, with the chmod that fixes it.
func enforceDataDirPerms(baseDir, configPath string) error {
	if err := perms.Enforce(baseDir, configPath, func(msg string, f map[string]any) { logger.WarnCF("perms", msg, f) }); err != nil {
		return fmt.Errorf("startup aborted: %w", err)
	}
	return nil
}

// openAuditLog opens <CLAW_HOME>/internal/audit.db as the process-wide audit store. A
// gateway that cannot open it still serves, without an audit trail.
func openAuditLog(baseDir string) {
	if err := audit.Init(baseDir); err != nil {
		logger.WarnCF("gateway", "audit log disabled", map[string]any{"error": err.Error()})
	}
}
