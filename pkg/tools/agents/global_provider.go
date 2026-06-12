package agents

import (
	"context"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the subagent-spawn tool through the transport-neutral
// global layer with the BARE name "spawn". The aggregator mounts it under the
// "agent" namespace → published as "agent_spawn". It reuses the existing
// SpawnTool (an async tool); the global def is flagged Async so the bridge routes
// completion callbacks through ToolCall.Notify.
var GlobalProvider globalAgentProvider

type globalAgentProvider struct{}

func (globalAgentProvider) Namespace() string   { return "agent" }
func (globalAgentProvider) Description() string { return "Subagent spawning" }

// Available gates the package on the subagent capability being enabled, matching
// the old Build-time check. When disabled the host reports the tool as blocked
// with reason "requires_subagent".
func (globalAgentProvider) Available(cfg any) (bool, string) {
	c, ok := cfg.(*config.Config)
	if !ok || c == nil {
		return true, ""
	}
	if !c.Tools.IsToolEnabled("subagent") {
		return false, "requires_subagent"
	}
	return true, ""
}

func (globalAgentProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	cd, _ := deps.Host.(tools.ToolDeps)

	// Construct the real spawn tool only when the runtime deps are present
	// (registerRuntimeTools path). The phase-1 build and deps-free enumeration
	// leave it nil; the handler guards that case.
	var spawnTool *SpawnTool
	if cd.Provider != nil && cd.CandidateResolver != nil {
		mgr := NewSubagentManager(SubagentManagerConfig{
			Provider:          cd.Provider,
			Workspace:         cd.Workspace,
			Dispatcher:        cd.Dispatcher,
			Fallback:          cd.Fallback,
			SelfCandidates:    cd.Candidates,
			CallerAgentID:     cd.AgentID,
			CandidateResolver: cd.CandidateResolver,
		})
		spawnTool = NewSpawnTool(mgr)
		if cd.SpawnAllowlist != nil {
			callerID := cd.AgentID
			spawnTool.SetAllowlistChecker(func(targetAgentID string) bool {
				return cd.SpawnAllowlist(callerID, targetAgentID)
			})
		}
	}

	meta := &SpawnTool{} // static metadata (Description/Parameters touch no fields)
	return []global.ToolDefinition{
		{
			Name:        "spawn",
			Description: meta.Description(),
			RawSchema:   meta.Parameters(),
			Category:    "agents",
			Async:       true,
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if spawnTool == nil {
					return &global.Result{IsError: true, ForLLM: "spawn tool not available"}, nil
				}
				if call.Notify != nil {
					cb := func(_ context.Context, r *tools.ToolResult) {
						call.Notify(tools.ResultToGlobal(r))
					}
					return tools.ResultToGlobal(spawnTool.ExecuteAsync(call.Ctx, call.Args, cb)), nil
				}
				return tools.ResultToGlobal(spawnTool.Execute(call.Ctx, call.Args)), nil
			},
		},
	}
}
