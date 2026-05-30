package agents

import (
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// Provider is the singleton ToolProvider for agent spawn tools.
var Provider agentsProvider

type agentsProvider struct{}

func (p agentsProvider) Namespace() string   { return "agents" }
func (p agentsProvider) Description() string { return "Agent spawn and subagent management" }
func (p agentsProvider) Category() string    { return "agents" }
func (p agentsProvider) ConfigKey() string   { return "spawn" }

func (p agentsProvider) Available(cfg *config.Config) (bool, string) {
	return true, ""
}

func (p agentsProvider) Describe() []tools.ToolDescriptor {
	return []tools.ToolDescriptor{
		{Name: "agents_spawn", Description: "Launch a background subagent for long-running or delegated work.", Category: "agents", ConfigKey: "agents_spawn", DefaultEnabled: true},
	}
}

func (p agentsProvider) Build(deps tools.ToolDeps) []tools.Tool {
	cfg := deps.Cfg
	if cfg == nil {
		return nil
	}
	agentCfg := deps.AgentCfg

	// Require runtime deps (Provider, Dispatcher) — return nil in phase 1 build.
	if deps.Provider == nil {
		return nil
	}

	if !cfg.Tools.IsToolEnabled("agents_spawn") || !isToolAllowed(agentCfg, "agents_spawn") {
		return nil
	}
	if !cfg.Tools.IsToolEnabled("subagent") {
		logger.WarnCF("agent", "agents_spawn tool requires subagent to be enabled", nil)
		return nil
	}

	currentAgentID := deps.AgentID
	candidateResolver := deps.CandidateResolver
	if candidateResolver == nil {
		logger.WarnCF("agent", "agents_spawn: no candidate resolver provided", nil)
		return nil
	}

	subagentManager := NewSubagentManager(SubagentManagerConfig{
		Provider:          deps.Provider,
		DefaultModel:      "",
		Workspace:         deps.Workspace,
		Dispatcher:        deps.Dispatcher,
		Fallback:          deps.Fallback,
		SelfCandidates:    deps.Candidates,
		CallerAgentID:     currentAgentID,
		CandidateResolver: candidateResolver,
	})

	spawnTool := NewSpawnTool(subagentManager)
	if deps.SpawnAllowlist != nil {
		spawnTool.SetAllowlistChecker(func(targetAgentID string) bool {
			return deps.SpawnAllowlist(currentAgentID, targetAgentID)
		})
	}

	return []tools.Tool{spawnTool}
}

// isToolAllowed checks whether the agent config permits the named tool.
func isToolAllowed(agentCfg *config.AgentConfig, name string) bool {
	if agentCfg == nil {
		return true
	}
	return agentCfg.IsToolAllowed(name)
}
