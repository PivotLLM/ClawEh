// ClawEh - Personal AI Assistant
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"sync"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/mcp"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

type mcpRuntime struct {
	initOnce sync.Once
	mu       sync.Mutex
	manager  *mcp.Manager
	initErr  error
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
// server enumerates its catalogue — otherwise CLI-based agents never see the
// mcp_* tools (the host catalogue is a one-shot snapshot of the registries).
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

	if err := mcpManager.LoadFromMCPConfig(ctx, al.cfg.Tools.MCP, al.mcpWorkspacePath()); err != nil {
		logger.WarnCF("agent", "Failed to load MCP servers, MCP tools will not be available",
			map[string]any{"error": err.Error()})
		if closeErr := mcpManager.Close(); closeErr != nil {
			logger.ErrorCF("agent", "Failed to close MCP manager", map[string]any{"error": closeErr.Error()})
		}
		return nil
	}

	if err := al.registerMCPToolsFromManager(mcpManager); err != nil {
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
	agentIDs := al.registry.ListAgentIDs()
	agentCount := len(agentIDs)

	for serverName, conn := range servers {
		uniqueTools += len(conn.Tools)
		for _, tool := range conn.Tools {
			for _, agentID := range agentIDs {
				agent, ok := al.registry.GetAgent(agentID)
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
				if agent.DiscoveryActive {
					agent.Tools.RegisterHidden(mcpTool)
				} else {
					agent.Tools.Register(mcpTool)
				}

				totalRegistrations++
				logger.DebugCF("agent", "Registered MCP tool",
					map[string]any{
						"agent_id": agentID,
						"server":   serverName,
						"tool":     tool.Name,
						"name":     mcpTool.Name(),
					})
			}
		}
	}
	logger.InfoCF("agent", "MCP tools registered successfully",
		map[string]any{
			"server_count":        len(servers),
			"unique_tools":        uniqueTools,
			"total_registrations": totalRegistrations,
			"agent_count":         agentCount,
		})

	return nil
}
