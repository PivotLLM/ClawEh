// ClawEh
// License: MIT

package agent

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/mcp"
	"github.com/PivotLLM/ClawEh/tools"
)

type mcpRuntime struct {
	initOnce sync.Once
	mu       sync.Mutex
	manager  *mcp.Manager
	initErr  error
	// host is the MCP host whose catalogue is refreshed after a server's tools
	// are re-registered; nil while the host is not running.
	host CatalogueRefresher
	// refreshMu serializes per-server re-registration (the tools-changed handler
	// and RefreshMCPServer), so two refreshes of one server cannot interleave
	// their remove and register passes.
	refreshMu sync.Mutex
}

// CatalogueRefresher is the MCP host as the agent loop sees it: something whose
// published tool catalogue can be brought back in step with the agent registries.
type CatalogueRefresher interface {
	RefreshCatalogue()
}

// SetMCPHost wires (or, with nil, clears) the MCP host whose catalogue is
// refreshed when an external server's tools are re-registered. The gateway sets
// it when the host starts and clears it when the host is stopped on reload.
func (al *AgentLoop) SetMCPHost(r CatalogueRefresher) {
	al.mcp.mu.Lock()
	al.mcp.host = r
	al.mcp.mu.Unlock()
}

// refreshMCPHost asks the running MCP host, if any, to re-derive its catalogue
// from the agent registries.
func (al *AgentLoop) refreshMCPHost() {
	al.mcp.mu.Lock()
	host := al.mcp.host
	al.mcp.mu.Unlock()
	if host == nil {
		return
	}
	host.RefreshCatalogue()
}

func (r *mcpRuntime) setManager(manager *mcp.Manager) {
	r.mu.Lock()
	r.manager = manager
	r.initErr = nil
	r.mu.Unlock()
}

func (r *mcpRuntime) setInitErr(err error) {
	r.mu.Lock()
	r.initErr = err
	r.mu.Unlock()
}

func (r *mcpRuntime) getInitErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.initErr
}

func (r *mcpRuntime) takeManager() *mcp.Manager {
	r.mu.Lock()
	defer r.mu.Unlock()
	manager := r.manager
	r.manager = nil
	return manager
}

// peekManager returns the live manager without clearing it, so a reload can
// reconcile connections in place instead of tearing everything down.
func (r *mcpRuntime) peekManager() *mcp.Manager {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manager
}

func (r *mcpRuntime) hasManager() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manager != nil
}

// MCPStatus returns the live connection status of every outbound MCP server the
// loop's manager currently knows about (connected / reconnecting / cooldown).
// Returns nil when MCP is disabled or not yet initialized; callers merge this
// against configured servers to show never-connected ones as disconnected.
func (al *AgentLoop) MCPStatus() []mcp.ServerStatus {
	mgr := al.mcp.peekManager()
	if mgr == nil {
		return nil
	}
	return mgr.Status()
}

// ensureMCPInitialized loads MCP servers/tools exactly once (the startup path),
// so both Run() and direct agent mode share one initialization. Config reloads
// use ReinitMCP, which bypasses the once-guard.
func (al *AgentLoop) ensureMCPInitialized(ctx context.Context) error {
	al.mcp.initOnce.Do(func() {
		if mgr := al.connectAndRegisterMCP(ctx); mgr != nil {
			al.mcp.setManager(mgr)
		}
	})
	return al.mcp.getInitErr()
}

// EnsureMCPInitialized is the exported entry point the gateway calls at startup
// to connect external MCP servers and register their tools BEFORE the MCP host
// server enumerates its catalogue — otherwise CLI-based agents would not see
// the mcp_* tools until a tool-list refresh brought the host catalogue back in
// step (see RefreshMCPServer and the tools-changed handler).
func (al *AgentLoop) EnsureMCPInitialized(ctx context.Context) error {
	return al.ensureMCPInitialized(ctx)
}

// ReinitMCP tears down the current MCP manager and reconnects/re-registers
// against the live config and registry. The gateway calls it on config reload,
// where the agent registry is rebuilt fresh (dropping the once-registered MCP
// tools) and where server definitions or per-agent mcp_tools allow-lists may
// have changed. Unlike ensureMCPInitialized it is NOT guarded by initOnce, so a
// webui edit to an agent's MCP access takes effect on reload without a restart.
// Must be called after al.cfg/al.registry have been swapped to the new values
// and before the MCP host server re-enumerates its catalogue.
func (al *AgentLoop) ReinitMCP(ctx context.Context) {
	// MCP fully disabled now: tear down whatever is running and clear.
	if !al.cfg.Tools.MCPClientEffectivelyEnabled() {
		if old := al.mcp.takeManager(); old != nil {
			if err := old.Close(); err != nil {
				logger.WarnCF("agent", "Failed to close previous MCP manager on reload",
					map[string]any{"error": err.Error()})
			}
		}
		al.mcp.setInitErr(nil)
		return
	}

	al.mcp.setInitErr(nil)

	mgr := al.mcp.peekManager()
	if mgr == nil {
		// No live manager (first enable, or previously disabled): full connect.
		if fresh := al.connectAndRegisterMCP(ctx); fresh != nil {
			al.mcp.setManager(fresh)
		}
		return
	}

	// Reuse the live manager: reconcile connections so unchanged servers keep
	// running (no relaunch, no profile-lock race), then re-register tools onto the
	// freshly-rebuilt agent registry.
	if err := mgr.Sync(ctx, al.cfg.Tools.MCP, al.mcpWorkspacePath()); err != nil {
		logger.WarnCF("agent", "Some MCP servers failed to reconcile on reload",
			map[string]any{"error": err.Error()})
	}
	if err := al.registerMCPToolsFromManager(mgr); err != nil {
		al.mcp.setInitErr(err)
		if old := al.mcp.takeManager(); old != nil {
			if closeErr := old.Close(); closeErr != nil {
				logger.ErrorCF("agent", "Failed to close MCP manager", map[string]any{"error": closeErr.Error()})
			}
		}
	}
}

// connectAndRegisterMCP applies the MCP enablement guards, connects to the
// configured servers, and registers each server's tools onto every agent whose
// mcp_tools allow-list admits them. It returns the live manager, or nil when MCP
// is disabled/unconfigured or the connection failed. It reads al.cfg/al.registry,
// which the caller must have already pointed at the desired (current) values.
func (al *AgentLoop) connectAndRegisterMCP(ctx context.Context) *mcp.Manager {
	if !al.cfg.Tools.MCPClientEffectivelyEnabled() {
		return nil
	}
	if len(al.cfg.Tools.MCP.Servers) == 0 {
		logger.WarnCF("agent", "MCP is enabled but no servers are configured, skipping MCP initialization", nil)
		return nil
	}
	findValidServer := false
	for _, serverCfg := range al.cfg.Tools.MCP.Servers {
		if serverCfg.Enabled {
			findValidServer = true
		}
	}
	if !findValidServer {
		logger.WarnCF("agent", "MCP is enabled but no valid servers are configured, skipping MCP initialization", nil)
		return nil
	}

	mcpManager := mcp.NewManager()
	mcpManager.SetAlerter(al.Alerter())
	// A server whose tool list changed (notification, probe, reconnect) is
	// re-registered onto every agent, and the host catalogue follows.
	mcpManager.SetToolsChangedHandler(func(server string) {
		al.refreshMCPServerTools(mcpManager, server)
	})

	if err := mcpManager.LoadFromMCPConfig(ctx, al.cfg.Tools.MCP, al.mcpWorkspacePath()); err != nil {
		// A failed initial connect is NOT fatal: keep the manager alive so the
		// background retry loop (mcpRetryLoop) can reconnect these servers without a
		// restart. Its desired set was recorded before the connect attempts, and
		// registerMCPToolsFromManager below simply registers nothing for now.
		logger.WarnCF("agent", "Some MCP servers failed initial connect; retrying in background",
			map[string]any{"error": err.Error()})
	}

	// The manager is live from here: a tools-changed handler that fires during
	// registration (a probe tick) must find it, or its refresh would be skipped.
	al.mcp.setManager(mcpManager)

	if err := al.registerMCPToolsFromManager(mcpManager); err != nil {
		al.mcp.takeManager()
		al.mcp.setInitErr(err)
		if closeErr := mcpManager.Close(); closeErr != nil {
			logger.ErrorCF("agent", "Failed to close MCP manager", map[string]any{"error": closeErr.Error()})
		}
		return nil
	}

	return mcpManager
}

// mcpWorkspacePath is the workspace used to resolve relative MCP envFile paths:
// the default agent's workspace when set, otherwise the global workspace.
func (al *AgentLoop) mcpWorkspacePath() string {
	workspacePath := al.cfg.WorkspacePath()
	if defaultAgent := al.registry.GetDefaultAgent(); defaultAgent != nil && defaultAgent.Workspace != "" {
		workspacePath = defaultAgent.Workspace
	}
	return workspacePath
}

// registerMCPToolsFromManager registers every connected server's tools onto each
// agent whose mcp_tools allow-list admits them, and (when enabled) wires the tool
// discovery helpers. It runs on both first init and after a reload's Sync, since a
// reload rebuilds the agent registry and the new agents carry no MCP tools until
// re-registered against the live manager. Returns an error only for an invalid
// discovery configuration; the caller then tears the manager down.
func (al *AgentLoop) registerMCPToolsFromManager(mgr *mcp.Manager) error {
	servers := mgr.GetServers()
	uniqueTools := 0
	totalRegistrations := 0

	for serverName, conn := range servers {
		uniqueTools += len(conn.Tools)
		_, added := al.registerMCPServerTools(mgr, serverName, conn)
		totalRegistrations += added
	}
	logger.InfoCF("agent", "MCP tools registered successfully",
		map[string]any{
			"server_count":        len(servers),
			"unique_tools":        uniqueTools,
			"total_registrations": totalRegistrations,
			"agent_count":         len(al.GetRegistry().ListAgentIDs()),
		})

	return nil
}

// registerMCPServerTools replaces one server's tools on every agent: the
// server's previous registrations are removed first, then the current list is
// registered onto each agent whose mcp_tools allow-list admits it, so a renamed
// or removed tool disappears and an unchanged one carries a fresh definition.
// Returns the registrations removed and added. Split out so the background
// retry loop and the tools-changed handler can register just the server
// concerned (registering all servers would re-register — and log-warn over —
// live ones).
//
// Removal is by name prefix ("mcp_<server>_"), which a server whose sanitized
// name extends this one's ("alice" and "alice_docs") shares, so such servers
// are put back afterwards, shortest prefix first so a later pass never undoes
// an earlier one.
func (al *AgentLoop) registerMCPServerTools(
	mgr *mcp.Manager,
	serverName string,
	conn *mcp.ServerConnection,
) (removed, added int) {
	removed, added = al.replaceMCPServerTools(mgr, serverName, conn)

	prefix := tools.MCPServerPrefix(serverName)
	servers := mgr.GetServers()
	var siblings []string
	for name := range servers {
		if name != serverName && strings.HasPrefix(tools.MCPServerPrefix(name), prefix) {
			siblings = append(siblings, name)
		}
	}
	sort.Slice(siblings, func(i, j int) bool {
		return len(tools.MCPServerPrefix(siblings[i])) < len(tools.MCPServerPrefix(siblings[j]))
	})
	for _, name := range siblings {
		al.replaceMCPServerTools(mgr, name, servers[name])
	}
	return removed, added
}

// removeMCPServerTools drops one server's tools from every agent registry and
// returns how many registrations went.
func (al *AgentLoop) removeMCPServerTools(serverName string) int {
	removed := 0
	reg := al.GetRegistry()
	prefix := tools.MCPServerPrefix(serverName)
	for _, agentID := range reg.ListAgentIDs() {
		if agent, ok := reg.GetAgent(agentID); ok && agent.Tools != nil {
			removed += agent.Tools.RemoveByPrefix(prefix)
		}
	}
	return removed
}

// replaceMCPServerTools is registerMCPServerTools for one server alone: remove
// its previous registrations, then register its current list.
func (al *AgentLoop) replaceMCPServerTools(
	mgr *mcp.Manager,
	serverName string,
	conn *mcp.ServerConnection,
) (removed, added int) {
	removed = al.removeMCPServerTools(serverName)
	reg := al.GetRegistry()
	for _, tool := range conn.Tools {
		for _, agentID := range reg.ListAgentIDs() {
			agent, ok := reg.GetAgent(agentID)
			if !ok {
				continue
			}

			// Gate on the dedicated per-agent MCP allow-list (mcp_tools), which
			// matches <server>_<tool> by equality-or-prefix. This is separate
			// from the generic Tools allowlist so MCP access is per-tool rather
			// than all-or-nothing per server.
			mcpTool := tools.NewMCPTool(mgr, serverName, tool)

			if !agent.Config.MCPToolAllowed(mcpTool.Name()) {
				continue
			}

			// MCP tools are discovery-eligible: when the agent's effective
			// discovery is on (decided during provider registration and stored on
			// the instance), hide them behind search_tools; otherwise advertise.
			// A namespace pinned via always_shown_namespaces stays visible.
			if discoveryHidesTool(agent.DiscoveryActive, agent.AlwaysShownNamespaces, mcpTool.Name()) {
				// Group by server so a reveal-together server unlocks as a set.
				agent.Tools.RegisterHiddenGroup(mcpTool, serverName, conn.RevealTogether())
			} else {
				agent.Tools.Register(mcpTool)
			}

			added++
			logger.DebugCF("agent", "Registered MCP tool",
				map[string]any{
					"agent_id": agentID,
					"server":   serverName,
					"tool":     tool.Name,
					"name":     mcpTool.Name(),
				})
		}
	}
	return removed, added
}

// refreshMCPServerTools is the manager's tools-changed handler: it re-registers
// one server's current tools onto every agent (or removes them, when the server
// is gone), then brings the MCP host catalogue in step. Refreshes are serialized
// so two for one server cannot interleave their remove and register passes. A
// manager that is no longer the live one (closed on reload or shutdown, or
// superseded by a fresh one that registered everything afresh) is ignored.
func (al *AgentLoop) refreshMCPServerTools(mgr *mcp.Manager, server string) {
	al.mcp.refreshMu.Lock()
	defer al.mcp.refreshMu.Unlock()
	if al.mcp.peekManager() != mgr {
		return
	}

	var removed, added, current int
	if conn, ok := mgr.GetServer(server); ok {
		current = len(conn.Tools)
		removed, added = al.registerMCPServerTools(mgr, server, conn)
	} else {
		removed = al.removeMCPServerTools(server)
	}
	logger.InfoCF("agent", "MCP server tools re-registered",
		map[string]any{
			"server":               server,
			"tools":                current,
			"registrations_before": removed,
			"registrations_after":  added,
		})
	al.refreshMCPHost()
}

// RefreshMCPServer disconnects and reconnects one external MCP server, then
// re-registers its tools onto every agent and refreshes the MCP host catalogue.
// Returns mcp.ErrUnknownServer when name is not a configured, enabled server.
func (al *AgentLoop) RefreshMCPServer(ctx context.Context, name string) error {
	mgr := al.mcp.peekManager()
	if mgr == nil {
		return mcp.ErrUnknownServer
	}
	if err := mgr.Reconnect(ctx, name); err != nil {
		return err
	}
	al.refreshMCPServerTools(mgr, name)
	return nil
}

// mcpRetryInterval is how often the background loop retries connecting desired MCP
// servers that are not currently connected. The per-server reconnect cooldown gates
// the actual attempts, so this only needs to be responsive, not aggressive.
const mcpRetryInterval = 15 * time.Second

// mcpRetryLoop periodically reconnects desired MCP servers that are not currently
// connected — e.g. a server whose initial connect failed because the upstream was
// briefly down or the row was saved mid-edit — and registers the tools of any that
// come up. Runs until mcpRetryStop is closed. Complements the probe-driven
// reconnect, which only covers servers that were once connected.
func (al *AgentLoop) mcpRetryLoop(ctx context.Context) {
	ticker := time.NewTicker(mcpRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-al.mcpRetryStop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			mgr := al.mcp.peekManager()
			if mgr == nil {
				continue
			}
			connected := mgr.RetryDisconnected(ctx)
			for _, name := range connected {
				if conn, ok := mgr.GetServer(name); ok {
					al.registerMCPServerTools(mgr, name, conn)
				}
			}
			if len(connected) > 0 {
				al.refreshMCPHost()
			}
		}
	}
}
