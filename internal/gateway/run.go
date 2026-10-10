package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/alerts"
	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/internal/clock"
	"github.com/PivotLLM/ClawEh/internal/layout"
	"github.com/PivotLLM/ClawEh/internal/pidfile"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/servicetoken"
	"github.com/PivotLLM/ClawEh/tools"
)

// gatewayCmd runs the gateway until it is stopped: a signal, a core service
// that died, or a restart request.
func gatewayCmd(debug bool) error {
	baseDir := internal.GetClawHome()
	logPath := startFileLogging(baseDir)

	// The PID lock is taken before connecting to any external service: if
	// another instance is already running this exits at once with a clear
	// error.
	lockFile, err := acquireLock(baseDir)
	if err != nil {
		return fmt.Errorf("startup aborted: %w", err)
	}
	defer releaseLock(lockFile)

	logger.InfoCF("gateway", "Starting", map[string]any{"app": app.Name(), "version": app.Version()})

	configPath := internal.GetConfigPath()
	store, cfg, bootDanglingRefs, err := openBootConfig(baseDir, configPath)
	if err != nil {
		return err
	}
	// Shared by boot, the watcher and the forced reload so a missing-model
	// reference alerts once per process, not once per reload.
	refAlerts := &modelRefAlerts{}

	// Lay out the data directory before anything
	// below opens the files it holds.
	layout.Prepare(cfg)
	applyLoggingConfig(cfg, logPath, debug)
	provider := bootProvider(cfg)

	registerToolProviders()
	// Default per-agent allowlist = every DefaultEnabled tool (single source of
	// truth; the MCP-host default uses the same set).
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
	agentLoop, err := newGatewayAgentLoop(cfg, msgBus, provider, dispatcher, forumHost, operatorAlerter)
	if err != nil {
		return err
	}

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

	services, err := setupAndStartServices(cfg, agentLoop, msgBus, configPath, store, fatal)
	if err != nil {
		return err
	}
	services.Forum = forumSvc
	forumOwned = false
	wireGatewayAPI(services, agentLoop, fatal, alertsPath)

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

	run, stopWatchers := startGatewayRun(cfg, debug, agentLoop, services, msgBus, provider, store, refAlerts, fatal)
	defer stopWatchers()
	return run.loop(ctx)
}

// openBootConfig makes the data directory private, then opens the config
// store and builds the runtime copy the gateway starts on. The store is the
// live configuration from here on, shared with the WebUI API so a save
// through it and the gateway's own reads never disagree. The runtime works on
// a pruned private copy; the store keeps the full on-disk config so invalid
// entries can be repaired through the WebUI. References to models that no
// longer exist are removed from the file.
func openBootConfig(baseDir, configPath string) (*config.Store, *config.Config, modelRefPrune, error) {
	if err := enforceDataDirPerms(baseDir, configPath); err != nil {
		return nil, nil, modelRefPrune{}, err
	}
	store, err := openConfigStore(configPath)
	if err != nil {
		return nil, nil, modelRefPrune{}, fmt.Errorf("error loading config: %w", err)
	}
	cfg, dangling, err := runtimeConfig(store)
	if err != nil {
		return nil, nil, modelRefPrune{}, fmt.Errorf("error loading config: %w", err)
	}
	return store, cfg, dangling, nil
}

// startGatewayRun prepares the main loop and starts what it waits on: the
// config watcher, the reload API trigger and the service-token file watcher.
// The returned function stops the watchers.
func startGatewayRun(
	cfg *config.Config,
	debug bool,
	agentLoop *agent.AgentLoop,
	services *gatewayServices,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	store *config.Store,
	refAlerts *modelRefAlerts,
	fatal *fatalNotifier,
) (*gatewayRun, func()) {
	run := &gatewayRun{
		agentLoop: agentLoop,
		services:  services,
		msgBus:    msgBus,
		provider:  provider,
		store:     store,
		refAlerts: refAlerts,
		fatal:     fatal,
	}
	reloadInterval := cfg.ConfigReloadInterval()
	logger.InfoF("Config reload watcher", map[string]any{"interval": reloadInterval.String()})
	var stopWatch func()
	run.configReloads, stopWatch, run.markConfigApplied = setupConfigWatcherPolling(store, reloadInterval,
		time.Duration(global.ConfigReloadDebounceSeconds)*time.Second, debug, agentLoop.Alerter(), refAlerts)
	run.forceReloads = setReloadTrigger(services)

	// Watch the service-token state file so `claw token` changes activate live
	// (writes are atomic, so no debounce is needed).
	var stopSvcWatch func()
	run.serviceTokenChanges, stopSvcWatch = setupFileChangeWatcher(clock.Real, servicetoken.Path(cfg.DataDir()), reloadInterval)
	return run, func() {
		stopSvcWatch()
		stopWatch()
	}
}

// startFileLogging enables file logging as early as possible, so startup,
// config load, prune warnings and any fatal error are captured in claw.log,
// not just on the console or journal, and routes the shared modules' logs
// into ClawEh's logger. It returns the log file's path; the configured
// format and level are applied once the config is loaded
// (applyLoggingConfig).
func startFileLogging(baseDir string) string {
	logPath := filepath.Join(baseDir, "logs", "claw.log")
	logger.SetErrorLogLevel(logger.ParseLevel(global.ErrorLogLevel))
	if err := logger.EnableFileLogging(logPath, false); err != nil {
		logger.WarnCF("gateway", "Failed to enable file logging", map[string]any{"path": logPath, "error": err.Error()})
	}
	installSpawnllmLogging()
	cogmemhost.InstallLogging()
	return logPath
}

// applyLoggingConfig applies the configured logging; the debug flag
// overrides the level.
func applyLoggingConfig(cfg *config.Config, logPath string, debug bool) {
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
}

// bootProvider creates the default provider, or the unconfigured one when no
// model is configured, and records the resolved default model on cfg.
func bootProvider(cfg *config.Config) providers.LLMProvider {
	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		logger.WarnCF("gateway", "No model configured, starting in unconfigured state", map[string]any{"detail": err.Error()})
		provider = providers.NewUnconfiguredProvider()
		modelID = ""
	}
	if modelID != "" {
		cfg.Agents.Defaults.SetDefaultModel(modelID)
	}
	return provider
}

// newGatewayAgentLoop creates the agent loop, binds the forum host to it and
// logs what it started with. It refuses a configuration without a default
// agent.
func newGatewayAgentLoop(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	dispatcher *providers.ProviderDispatcher,
	forumHost *agent.ForumHost,
	operatorAlerter alerter.Alerter,
) (*agent.AgentLoop, error) {
	agentLoop, err := agent.NewAgentLoop(cfg, msgBus, provider, dispatcher, agent.OwnsDataDir())
	if err != nil {
		return nil, fmt.Errorf("error creating agent loop: %w", err)
	}
	forumHost.Bind(agentLoop)
	agentLoop.SetAlerter(operatorAlerter)
	agentLoop.SetDumpsDir(filepath.Join(internal.GetClawHome(), "logs", "dumps"))

	startupInfo := agentLoop.GetStartupInfo()
	if len(startupInfo) == 0 {
		return nil, errors.New("no default agent configured — add at least one entry to agents.list in your config")
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
	return agentLoop, nil
}

// wireGatewayAPI hands the WebUI API what only the running gateway has: the
// alerts file the Logs page tails, the alerter, and the restart request
// (POST /api/system/restart: a clean shutdown with a non-zero exit, for the
// service manager to start the gateway again).
func wireGatewayAPI(services *gatewayServices, agentLoop *agent.AgentLoop, fatal *fatalNotifier, alertsPath string) {
	api := services.WebServer.APIHandler()
	api.SetAlertsPath(alertsPath)
	api.SetAlerter(agentLoop.Alerter())
	api.SetRestart(fatal.requestRestart)
}

// setReloadTrigger lets POST /api/gateway/reload apply config changes at
// once, bypassing the watcher's debounce, so a WebUI save is not left out of
// the running config for the debounce window. The trigger sends a response
// channel on the returned channel and blocks until the reload completes.
func setReloadTrigger(services *gatewayServices) chan chan error {
	forceReload := make(chan chan error, 1)
	if services.WebServer == nil {
		return forceReload
	}
	services.WebServer.APIHandler().SetReloadTrigger(func() error {
		done := make(chan error, 1)
		select {
		case forceReload <- done:
			return <-done
		case <-time.After(5 * time.Second):
			return errors.New("claw is busy; reload not accepted")
		}
	})
	return forceReload
}

// gatewayRun is the running gateway's main loop: what it waits on and what
// it acts on.
type gatewayRun struct {
	agentLoop *agent.AgentLoop
	services  *gatewayServices
	msgBus    *bus.MessageBus
	// provider is the default provider, replaced by each successful reload.
	provider  providers.LLMProvider
	store     *config.Store
	refAlerts *modelRefAlerts
	fatal     *fatalNotifier

	configReloads       chan *config.Config
	markConfigApplied   func(configFileState)
	forceReloads        chan chan error
	serviceTokenChanges <-chan struct{}
}

// loop waits for signals, failures and config or token changes until the
// gateway stops; the error is the exit reason.
func (r *gatewayRun) loop(ctx context.Context) error {
	// SIGTERM as well as SIGINT: systemd stops a unit with SIGTERM, whose
	// default disposition kills the process without the clean shutdown below.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case <-sigChan:
			logger.Info("Shutting down...")
			shutdownGateway(r.services, r.agentLoop, r.provider, true) //nolint:contextcheck // shutdown runs on its own bounded contexts, so it completes whatever the run context does
			return nil

		case failure := <-r.fatal.failed:
			// Same shutdown as SIGTERM; the failure becomes the exit reason and
			// NewGatewayCommand exits non-zero on it. fatalService has armed a
			// timer that exits regardless should this hang.
			logger.Info("Shutting down after a core service stopped...")
			shutdownGateway(r.services, r.agentLoop, r.provider, true) //nolint:contextcheck // shutdown runs on its own bounded contexts, so it completes whatever the run context does
			return failure

		case restart := <-r.fatal.restart:
			// POST /api/system/restart: the same shutdown, and a non-zero exit
			// so the service manager starts the gateway again.
			logger.Info("Shutting down to restart...")
			shutdownGateway(r.services, r.agentLoop, r.provider, true) //nolint:contextcheck // shutdown runs on its own bounded contexts, so it completes whatever the run context does
			return restart

		case newCfg := <-r.configReloads:
			r.reload(ctx, newCfg)

		case done := <-r.forceReloads:
			done <- r.forceReload(ctx)

		case <-r.serviceTokenChanges:
			logger.Info("🔑 Service-token file changed, reloading service tokens...")
			syncServiceTokensFromDisk(r.agentLoop.GetConfig(), r.agentLoop, r.services.MCPServer)
		}
	}
}

// reload applies a config the watcher validated, alerting when that fails.
func (r *gatewayRun) reload(ctx context.Context, newCfg *config.Config) {
	err := handleConfigReload(ctx, r.agentLoop, newCfg, &r.provider, r.services, r.msgBus)
	if err == nil {
		return
	}
	logger.Errorf("Config reload failed: %v", err)
	r.agentLoop.Alerter().Send(alerter.Alert{
		Title:       "Config reload failed",
		Description: "the reload was aborted part way; check the claw log, services may not all be running",
		Details:     err.Error(),
		EventID:     "config",
	})
}

// forceReload applies the config file now, for the reload API.
func (r *gatewayRun) forceReload(ctx context.Context) error {
	logger.Info("Forced config reload requested via API")
	// Taken before reading, so a change written while this reload runs is
	// newer than what the watcher is told was applied.
	applied := configFileStateOf(r.store.Path())
	if _, err := r.store.Reload(); err != nil {
		return err
	}
	newCfg, dangling, err := runtimeConfig(r.store)
	if err != nil {
		return err
	}
	r.refAlerts.report(r.agentLoop.Alerter(), dangling)
	if err := handleConfigReload(ctx, r.agentLoop, newCfg, &r.provider, r.services, r.msgBus); err != nil {
		return err
	}
	// The watcher must not fire a second, disruptive reload for the same
	// change.
	r.markConfigApplied(applied)
	return nil
}
