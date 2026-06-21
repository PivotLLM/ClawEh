// ClawEh
// License: MIT

package maestro

import (
	"path/filepath"

	mconfig "github.com/PivotLLM/Maestro/config"
	mllm "github.com/PivotLLM/Maestro/llm"
	mlogging "github.com/PivotLLM/Maestro/logging"
	mmaestro "github.com/PivotLLM/Maestro/pkg/maestro"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider mounts Maestro under the "maestro" namespace. It is gated by the
// per-agent Maestro flag (all-or-nothing): an agent without it enabled gets no
// Maestro tools. Per-agent data lives under <workspace>/maestro.
var GlobalProvider globalMaestroProvider

type globalMaestroProvider struct{}

func (globalMaestroProvider) Namespace() string { return "maestro" }
func (globalMaestroProvider) Description() string {
	return "Maestro task orchestration: projects, playbooks, tasks, and reports"
}
func (globalMaestroProvider) Available(cfg any) (bool, string) { return true, "" }

func (globalMaestroProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	c, _ := deps.Cfg.(*config.Config)
	cd, _ := deps.Host.(tools.ToolDeps)
	if c == nil {
		// Enumeration pass (no live config): Maestro is per-agent + all-or-nothing,
		// surfaced via a single agent toggle, so it is not listed in the catalog.
		return nil
	}

	// Gate on the per-agent Maestro flag.
	if !c.AgentHasMaestro(deps.AgentID) {
		return nil
	}

	workspace := cd.Workspace
	if workspace == "" {
		logger.WarnCF("maestro", "no workspace for agent; maestro tools disabled",
			map[string]any{"agent": deps.AgentID})
		return nil
	}
	base := filepath.Join(workspace, "maestro")

	mcfg := mconfig.New(
		mconfig.WithBaseDir(base),
		mconfig.WithEmbeddedFS(mmaestro.EmbeddedReference),
	)
	if err := mcfg.Prepare(); err != nil {
		logger.WarnCF("maestro", "failed to prepare maestro config; tools disabled",
			map[string]any{"agent": deps.AgentID, "base": base, "error": err.Error()})
		return nil
	}

	// Maestro logs to its own per-agent file (it always logged separately).
	mlog, err := mlogging.New(filepath.Join(base, "maestro.log"))
	if err != nil {
		logger.WarnCF("maestro", "failed to open maestro log",
			map[string]any{"agent": deps.AgentID, "error": err.Error()})
	}

	// Dispatch Maestro tasks as ClawEh sub-agents (host owns model selection).
	var disp mllm.Dispatcher
	if sr, ok := deps.Spawn.(global.SyncRunner); ok {
		disp = &dispatcher{run: sr}
	}

	p := &mmaestro.Provider{}
	defs := p.RegisterTools(global.Deps{
		Cfg:       mcfg,
		AgentID:   deps.AgentID,
		Workspace: workspace,
		Host:      mmaestro.HostDeps{Logger: mlog, Dispatcher: disp},
	})

	// A Maestro task worker is itself a sub-agent, so it must not be able to
	// re-enter Maestro — mark the whole suite PrimaryOnly (excluded from sub-agent
	// tool registries, like agent_spawn).
	for i := range defs {
		defs[i].PrimaryOnly = true
	}
	logger.InfoCF("maestro", "maestro tools enabled for agent",
		map[string]any{"agent": deps.AgentID, "tools": len(defs), "base": base, "host_dispatch": disp != nil})
	return defs
}
