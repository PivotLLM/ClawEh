// ClawEh - Cognitive Memory
// License: MIT

package gateway

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/agent"
	"github.com/PivotLLM/ClawEh/pkg/cogmem/consolidate"
	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/memory"
	cogmemtools "github.com/PivotLLM/ClawEh/pkg/tools/cogmem"
)

// archiveSource adapts a read-only pkg/memory archive to
// consolidate.MessageSource. It lives in the gateway (not in
// pkg/cogmem/consolidate) so the worker carries no dependency on pkg/memory.
type archiveSource struct {
	a *memory.ArchiveStore
}

func (s archiveSource) Bounds() (int64, int64, error) { return s.a.Bounds() }

func (s archiveSource) Range(minSeq, maxSeq int64) ([]consolidate.SourceMessage, error) {
	rows, err := s.a.QueryRange(minSeq, maxSeq)
	if err != nil {
		return nil, err
	}
	out := make([]consolidate.SourceMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, consolidate.SourceMessage{Seq: r.Seq, Role: r.Role, Text: r.Content})
	}
	return out, nil
}

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
		ar, err := memory.OpenReadOnly(j.ArchivePath)
		if err != nil {
			_ = st.Close()
			return nil, fmt.Errorf("cogmem: open archive: %w", err)
		}

		mem := cfg.Agents.Defaults.EffectiveMemory(inst.Config)
		caller := agentLoop.NewMemoryModelCaller(inst)
		opts := workerOptions(mem, j.Workspace)
		w := consolidate.NewWorker(st, archiveSource{a: ar}, caller, opts...)
		return w, nil
	}

	// Thresholds from the default agent's effective memory config. (Triggers are
	// process-global; per-agent batch levers are applied in the factory.)
	mem := defaultEffectiveMemory(cfg, agentLoop)
	var mopts []consolidate.ManagerOption
	if mem.Consolidation.EveryNMessages > 0 {
		mopts = append(mopts, consolidate.WithEveryNMessages(mem.Consolidation.EveryNMessages))
	}
	if mem.Consolidation.IdleMinutes > 0 {
		mopts = append(mopts, consolidate.WithIdle(time.Duration(mem.Consolidation.IdleMinutes)*time.Minute))
	}
	if mem.Consolidation.Nightly {
		at := mem.Consolidation.NightlyAt
		if at == "" {
			at = "03:00"
		}
		mopts = append(mopts, consolidate.WithNightlyAt(at), consolidate.WithNightlyJitter(15*time.Minute))
	}

	mgr := consolidate.NewManager(factory, mopts...)
	mgr.Start(context.Background())
	agentLoop.SetCogmemManager(mgr)

	// The cogmem_consolidate tool triggers a manual run for its session.
	cogmemtools.SetConsolidateTrigger(func(agentID, sessionKey string) {
		inst, ok := agentLoop.GetRegistry().GetAgent(agentID)
		if !ok || inst == nil {
			return
		}
		archivePath := filepath.Join(inst.Workspace, "sessions",
			store.SanitizeSessionKey(sessionKey)+".archive.db")
		mgr.Enqueue(consolidate.Job{
			AgentID:     agentID,
			SessionKey:  sessionKey,
			Workspace:   inst.Workspace,
			ArchivePath: archivePath,
		}, "manual")
	})

	logger.InfoC("cogmem", "cognitive-memory consolidation manager started")
	return mgr
}

// workerOptions translates a MemoryConfig into consolidate.Worker options.
func workerOptions(mem config.MemoryConfig, workspace string) []consolidate.Option {
	bo := consolidate.DefaultBatchOptions()
	if mem.Consolidation.MaxBatchMessages > 0 {
		bo.MaxMessages = mem.Consolidation.MaxBatchMessages
	}
	if mem.Consolidation.MaxInputTokens > 0 {
		bo.MaxInputTokens = mem.Consolidation.MaxInputTokens
	}
	if mem.Consolidation.PerMessageChars > 0 {
		bo.PerMessageChars = mem.Consolidation.PerMessageChars
	}
	opts := []consolidate.Option{
		consolidate.WithBatchOptions(bo),
		consolidate.WithProposeDomains(mem.Consolidation.ProposeDomains),
		consolidate.WithAutoPromote(mem.Consolidation.AutoPromote),
	}
	if mem.Consolidation.DebugDump {
		opts = append(opts, consolidate.WithDebugDump(filepath.Join(workspace, "cogmem-dumps")))
	}
	return opts
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
