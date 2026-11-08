package agent

import (
	"github.com/PivotLLM/ClawEh/agentreg"
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
	return parent.Subagents.Allows(targetAgentID)
}
