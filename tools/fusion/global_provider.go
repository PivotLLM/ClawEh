// ClawEh
// License: MIT

package fusion

import (
	"sort"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
)

// GlobalProvider mounts MCPFusion's config-driven REST-API tools under the
// "fusion" namespace. It is gated by the per-agent Fusion flag (all-or-nothing):
// an agent without it enabled gets no fusion tools. Tools come from JSON config
// files under <dataDir>/fusion; OAuth tokens persist per-tenant (agent) in the
// shared fusion token store.
var GlobalProvider globalFusionProvider

type globalFusionProvider struct{}

func (globalFusionProvider) Namespace() string { return "fusion" }
func (globalFusionProvider) Description() string {
	return "Config-driven REST-API tools (MCPFusion): external APIs exposed as tools"
}
func (globalFusionProvider) Available(cfg any) (bool, string) { return true, "" }

// Suite marks Fusion as an all-or-nothing tool suite gated by the per-agent
// `fusion` flag (default off), not the per-tool allowlist.
func (globalFusionProvider) Suite() string { return "fusion" }

func (globalFusionProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	c, ok := deps.Cfg.(*config.Config)
	if !ok || c == nil {
		// Enumeration pass (no live config): Fusion is per-agent + all-or-nothing,
		// surfaced via a single agent toggle, so it is not listed in the catalog.
		return nil
	}

	// Gate on the per-agent Fusion flag.
	if !c.AgentHasFusion(deps.AgentID) {
		return nil
	}

	eng := sharedEngine(c)
	if eng == nil {
		return nil
	}

	// The tenant keys per-agent OAuth token storage; normalize it the same way
	// the rest of ClawEh does so tokens survive across restarts consistently.
	// ToolSpecDefinitions returns []toolspec.ToolDefinition, which is identical to
	// []global.ToolDefinition (both toolspec v0.2.0) — no conversion needed.
	tenant := routing.NormalizeAgentID(deps.AgentID)
	all := eng.ToolSpecDefinitions(tenant)
	defs := allowedDefs(all, c.AgentByID(deps.AgentID))

	// Fusion on with nothing granted is a configuration state, not an outage:
	// it is logged here and shown on the Check Up page, never alerted.
	logger.InfoCF("fusion", "fusion tools enabled for agent",
		map[string]any{"agent": deps.AgentID, "tools": len(defs), "available": len(all)})
	return defs
}

// allowedDefs keeps the Fusion tools the agent's mcp_tools entries admit
// (config.AgentConfig.FusionToolAllowed): Fusion on with no matching entry
// yields none. A nil agent yields none.
func allowedDefs(defs []global.ToolDefinition, a *config.AgentConfig) []global.ToolDefinition {
	var out []global.ToolDefinition
	for _, d := range defs {
		if a.FusionToolAllowed(d.Name) {
			out = append(out, d)
		}
	}
	return out
}

// ServiceNames lists the Fusion services defined in the fusion config folder,
// sorted, for the per-agent access list. Nil when the engine could not be
// built or no service is defined.
func ServiceNames(c *config.Config) []string {
	eng := sharedEngine(c)
	if eng == nil {
		return nil
	}
	names := eng.GetServiceNames()
	sort.Strings(names)
	return names
}
