// ClawEh - Cognitive Memory
// License: MIT

package gateway

import (
	"context"
	"fmt"

	"github.com/PivotLLM/cogmem/consolidate"
	"github.com/PivotLLM/cogmem/store"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	cogmemtools "github.com/PivotLLM/ClawEh/tools/cogmem"
)

// setupCogmemConsolidation builds and starts the cognitive-memory consolidation
// manager and installs the cogmem_consolidate tool trigger. It is inert unless
// at least one agent is allowed the cogmem tools, and every job it runs is
// scoped to a cognitive agent's session.
func setupCogmemConsolidation(cfg *config.Config, agentLoop *agent.AgentLoop) *consolidate.Manager {
	// Only set up if at least one agent is cognitive.
	if !anyCognitiveAgent(cfg, agentLoop) {
		return nil
	}

	factory := func(j consolidate.Job) (*consolidate.Worker, error) {
		inst, ok := agentLoop.GetRegistry().GetAgent(j.AgentID)
		if !ok || inst == nil || inst.Config == nil || !inst.Config.CognitiveMemoryEnabled() {
			return nil, fmt.Errorf("cogmem: agent %q not cognitive", j.AgentID)
		}
		st, err := store.Open(store.SessionDBPath(j.Workspace, j.SessionKey))
		if err != nil {
			return nil, fmt.Errorf("cogmem: open store: %w", err)
		}

		settings := cogmemhost.Settings(cfg.Agents.Defaults.EffectiveMemory(inst.Config))
		caller := agentLoop.NewMemoryModelCaller(inst)
		return consolidate.NewWorker(st, caller, settings.WorkerOptions(j.Workspace)...), nil
	}

	// Thresholds from the default agent's effective memory config. (Triggers are
	// process-global; per-agent batch levers are applied in the factory.)
	mopts := cogmemhost.Settings(defaultEffectiveMemory(cfg, agentLoop)).ManagerOptions()

	mgr := consolidate.NewManager(factory, mopts...)
	mgr.Start(context.Background())
	agentLoop.SetCogmemManager(mgr)

	// The cogmem_consolidate tool triggers a manual run for its session.
	cogmemtools.SetConsolidateTrigger(func(agentID, sessionKey string) {
		// Tool calls arrive over MCP without an AgentID populated on the call,
		// so derive it from the session key ("agent:<id>:…"). Without this the
		// GetAgent below silently fails and the manual trigger is a no-op.
		if agentID == "" {
			if pk := routing.ParseAgentSessionKey(sessionKey); pk != nil {
				agentID = pk.AgentID
			}
		}
		inst, ok := agentLoop.GetRegistry().GetAgent(agentID)
		if !ok || inst == nil {
			logger.WarnCF("cogmem", "manual consolidate ignored; agent not found", map[string]any{
				"agent_id": agentID, "session_key": sessionKey,
			})
			return
		}
		mgr.Enqueue(consolidate.Job{
			AgentID:    agentID,
			SessionKey: sessionKey,
			Workspace:  inst.Workspace,
		}, "manual")
	})

	logger.InfoC("cogmem", "cognitive-memory consolidation manager started")
	return mgr
}

// anyCognitiveAgent reports whether at least one registered agent is allowed the
// cogmem tools.
func anyCognitiveAgent(cfg *config.Config, agentLoop *agent.AgentLoop) bool {
	reg := agentLoop.GetRegistry()
	for _, id := range reg.ListAgentIDs() {
		if inst, ok := reg.GetAgent(id); ok && inst != nil && inst.Config != nil &&
			inst.Config.CognitiveMemoryEnabled() {
			return true
		}
	}
	return false
}

// defaultEffectiveMemory returns the effective memory config for the default
// agent, used to source the process-global consolidation triggers.
func defaultEffectiveMemory(cfg *config.Config, agentLoop *agent.AgentLoop) config.MemoryConfig {
	if da := agentLoop.GetRegistry().GetDefaultAgent(); da != nil {
		return cfg.Agents.Defaults.EffectiveMemory(da.Config)
	}
	return cfg.Agents.Defaults.Memory
}
