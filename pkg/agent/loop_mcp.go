// ClawEh - Personal AI Assistant
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
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
	if old := al.mcp.takeManager(); old != nil {
		if err := old.Close(); err != nil {
			logger.WarnCF("agent", "Failed to close previous MCP manager on reload",
				map[string]any{"error": err.Error()})
		}
	}
	al.mcp.setInitErr(nil)
	if mgr := al.connectAndRegisterMCP(ctx); mgr != nil {
		al.mcp.setManager(mgr)
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

	defaultAgent := al.registry.GetDefaultAgent()
	workspacePath := al.cfg.WorkspacePath()
	if defaultAgent != nil && defaultAgent.Workspace != "" {
		workspacePath = defaultAgent.Workspace
	}

	if err := mcpManager.LoadFromMCPConfig(ctx, al.cfg.Tools.MCP, workspacePath); err != nil {
		logger.WarnCF("agent", "Failed to load MCP servers, MCP tools will not be available",
			map[string]any{"error": err.Error()})
		if closeErr := mcpManager.Close(); closeErr != nil {
			logger.ErrorCF("agent", "Failed to close MCP manager", map[string]any{"error": closeErr.Error()})
		}
		return nil
	}

	// Register MCP tools for all agents.
	servers := mcpManager.GetServers()
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
				mcpTool := tools.NewMCPTool(mcpManager, serverName, tool)

				if !agent.Config.MCPToolAllowed(mcpTool.Name()) {
					continue
				}

				if al.cfg.Tools.MCP.Discovery.Enabled {
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

	// Initialize Discovery Tools only if enabled by configuration.
	if al.cfg.Tools.MCPClientEffectivelyEnabled() && al.cfg.Tools.MCP.Discovery.Enabled {
		useBM25 := al.cfg.Tools.MCP.Discovery.UseBM25
		useRegex := al.cfg.Tools.MCP.Discovery.UseRegex

		// Fail fast: discovery enabled but no search method turned on.
		if !useBM25 && !useRegex {
			al.mcp.setInitErr(fmt.Errorf(
				"tool discovery is enabled but neither 'use_bm25' nor 'use_regex' is set to true in the configuration",
			))
			if closeErr := mcpManager.Close(); closeErr != nil {
				logger.ErrorCF("agent", "Failed to close MCP manager", map[string]any{"error": closeErr.Error()})
			}
			return nil
		}

		ttl := al.cfg.Tools.MCP.Discovery.TTL
		if ttl <= 0 {
			ttl = 5 // Default value
		}

		maxSearchResults := al.cfg.Tools.MCP.Discovery.MaxSearchResults
		if maxSearchResults <= 0 {
			maxSearchResults = 5 // Default value
		}

		logger.InfoCF("agent", "Initializing tool discovery", map[string]any{
			"bm25": useBM25, "regex": useRegex, "ttl": ttl, "max_results": maxSearchResults,
		})

		for _, agentID := range agentIDs {
			agent, ok := al.registry.GetAgent(agentID)
			if !ok {
				continue
			}

			if useRegex && agent.Config.IsToolAllowed("find_tools_regex") {
				agent.Tools.Register(tools.NewRegexSearchTool(agent.Tools, ttl, maxSearchResults))
			}
			if useBM25 && agent.Config.IsToolAllowed("find_tools_bm25") {
				agent.Tools.Register(tools.NewBM25SearchTool(agent.Tools, ttl, maxSearchResults))
			}
		}
	}

	return mcpManager
}
