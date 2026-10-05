package agent

import (
	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
)

// AgentRegistry is the agent registry (package agentreg) holding this
// package's instances.
type AgentRegistry = agentreg.Registry[*AgentInstance]

// canSpawnSubagent reports whether parentAgentID may spawn targetAgentID: the
// parent's subagents.allow_agents lists the target or "*".
func canSpawnSubagent(registry *AgentRegistry, parentAgentID, targetAgentID string) bool {
	parent, ok := registry.Get(parentAgentID)
	if !ok {
		return false
	}
	if parent.Subagents == nil || parent.Subagents.AllowAgents == nil {
		return false
	}
	targetNorm := routing.NormalizeAgentID(targetAgentID)
	for _, allowed := range parent.Subagents.AllowAgents {
		if allowed == "*" {
			return true
		}
		if routing.NormalizeAgentID(allowed) == targetNorm {
			return true
		}
	}
	return false
}

// forEachTool calls fn for every tool registered under name across all
// agents, temporary ones included. Used to propagate dependencies (e.g. the
// MediaStore) to tools after they are built.
func forEachTool(registry *AgentRegistry, name string, fn func(tools.Tool)) {
	for _, id := range registry.All() {
		if agent, ok := registry.Get(id); ok {
			if t, ok := agent.Tools.Get(name); ok {
				fn(t)
			}
		}
	}
}
