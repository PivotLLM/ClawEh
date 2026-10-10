package gateway

import (
	"fmt"
	"net/http"
	"time"

	_ "github.com/PivotLLM/ClawEh/channels/device"
	_ "github.com/PivotLLM/ClawEh/channels/discord"
	_ "github.com/PivotLLM/ClawEh/channels/line"
	_ "github.com/PivotLLM/ClawEh/channels/matrix"
	_ "github.com/PivotLLM/ClawEh/channels/secmsg"
	_ "github.com/PivotLLM/ClawEh/channels/slack"
	_ "github.com/PivotLLM/ClawEh/channels/telegram"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/internal/perms"
	"github.com/PivotLLM/ClawEh/logger"
	webserver "github.com/PivotLLM/ClawEh/web/backend"
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
