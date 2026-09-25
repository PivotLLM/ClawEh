// ClawEh
// License: MIT

package maestro

import (
	"os"
	"path/filepath"
	"reflect"

	mconfig "github.com/PivotLLM/Maestro/config"
	mlogging "github.com/PivotLLM/Maestro/logging"
	mmaestro "github.com/PivotLLM/Maestro/pkg/maestro"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/alerts"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
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

// Suite marks Maestro as an all-or-nothing tool suite gated by the per-agent
// `maestro` flag (default off), not the per-tool allowlist.
func (globalMaestroProvider) Suite() string { return "maestro" }

func (globalMaestroProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	c, ok := deps.Cfg.(*config.Config)
	var cd tools.ToolDeps
	if v, hostOK := deps.Host.(tools.ToolDeps); hostOK {
		cd = v
	}
	if !ok || c == nil {
		// Enumeration pass (no live config): Maestro is per-agent + all-or-nothing,
		// surfaced via a single agent toggle, so it is not listed in the catalog.
		return nil
	}

	// Gate on the per-agent Maestro flag.
	if !c.AgentHasMaestro(deps.AgentID) {
		return nil
	}

	// Dispatch Maestro tasks as ClawEh sub-agents (host owns model selection).
	// Without a sub-agent runner Maestro would fall back to its own (empty) LLM
	// config and fail every task, so refuse to expose the suite instead. Checked
	// before any directory or log file is created for the agent.
	sr, ok := deps.Spawn.(global.SyncRunner)
	if !ok || isNilRunner(sr) {
		logger.WarnCF("maestro", "no sub-agent runner for agent; maestro tools disabled",
			map[string]any{"agent": deps.AgentID})
		return nil
	}

	workspace := cd.Workspace
	if workspace == "" {
		logger.WarnCF("maestro", "no workspace for agent; maestro tools disabled",
			map[string]any{"agent": deps.AgentID})
		return nil
	}
	base := filepath.Join(workspace, "maestro")

	// Make sure the base dir exists before Maestro touches it (defensive: Prepare
	// also creates the subdirs, but never hand Maestro a missing base).
	if err := os.MkdirAll(base, 0o755); err != nil {
		logger.WarnCF("maestro", "failed to create maestro base dir; tools disabled",
			map[string]any{"agent": deps.AgentID, "base": base, "error": err.Error()})
		alertMaestroDisabled(deps.AgentID, base, err)
		return nil
	}

	agentCfg := cd.AgentCfg
	if agentCfg == nil {
		agentCfg = c.AgentByID(deps.AgentID)
	}
	refDirs := referenceDirsFromMounts(agentCfg, workspace)
	mcfg := mconfig.New(
		mconfig.WithBaseDir(base),
		mconfig.WithEmbeddedFS(mmaestro.EmbeddedReference),
		mconfig.WithRunner(runnerConfig(c.AgentMaestro(deps.AgentID))),
		mconfig.WithReferenceDirs(refDirs),
	)
	if err := mcfg.Prepare(); err != nil {
		logger.WarnCF("maestro", "failed to prepare maestro config; tools disabled",
			map[string]any{"agent": deps.AgentID, "base": base, "error": err.Error()})
		alertMaestroDisabled(deps.AgentID, base, err)
		return nil
	}
	for _, rd := range refDirs {
		logger.InfoCF("maestro", "mount available in maestro reference domain",
			map[string]any{"agent": deps.AgentID, "mount": rd.Mount, "path": rd.Path})
	}

	// Maestro's operational log goes to the central logger (component "maestro",
	// tagged with the agent) so it shows in the Web UI and rotates with claw.log.
	// Maestro's per-project logs are audit records and stay in the project.
	mlog := mlogging.NewWithWriter(&logWriter{agent: deps.AgentID})

	// Each dispatched prompt is one sub-agent run, bounded like a user turn.
	disp := &dispatcher{run: sr, timeout: c.Agents.Defaults.GetTurnTimeout()}

	p := &mmaestro.Provider{}
	defs := p.RegisterTools(global.Deps{
		Cfg:       mcfg,
		AgentID:   deps.AgentID,
		Workspace: workspace,
		Host: mmaestro.HostDeps{
			Logger:     mlog,
			Dispatcher: disp,
			// file_import may only read what the agent's own file tools can.
			ImportAllowed: importAllowed(c, agentCfg, workspace),
		},
	})

	// Maestro is available to sub-agents too (a worker may run its own taskset);
	// unbounded re-entry is prevented by MaxSpawnDepth in the Spawner, not by
	// withholding the tools.
	logger.InfoCF("maestro", "maestro tools enabled for agent",
		map[string]any{"agent": deps.AgentID, "tools": len(defs), "base": base, "timeout": disp.timeout.String()})
	return defs
}

// isNilRunner reports whether sr is nil, including a typed nil pointer stored
// in the interface (which a plain == nil comparison would miss).
func isNilRunner(sr global.SyncRunner) bool {
	if sr == nil {
		return true
	}
	v := reflect.ValueOf(sr)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// alertMaestroDisabled reports an agent whose Maestro tools could not be set
// up; it has them enabled and gets none. Repeats per agent collapse.
func alertMaestroDisabled(agentID, base string, err error) {
	alerts.Send(alerter.Alert{
		Title:       "Maestro tools disabled",
		Description: "agent " + agentID + ": " + base + " could not be prepared, so the agent has no Maestro tools",
		Details:     err.Error(),
		EventID:     "maestro:" + agentID,
	})
}
