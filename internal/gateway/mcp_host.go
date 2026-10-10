package gateway

import (
	"fmt"
	"os"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/mcpserver"
	"github.com/PivotLLM/ClawEh/servicetoken"
	"github.com/PivotLLM/ClawEh/tools"
)

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
