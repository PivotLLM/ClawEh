// ClawEh
// License: MIT

package maestro

import (
	"sync"

	mconfig "github.com/PivotLLM/Maestro/config"
	mllm "github.com/PivotLLM/Maestro/llm"
	mlogging "github.com/PivotLLM/Maestro/logging"
	"github.com/PivotLLM/Maestro/playbooks"
	"github.com/PivotLLM/Maestro/projects"
	"github.com/PivotLLM/Maestro/reference"
	"github.com/PivotLLM/Maestro/runner"
	"github.com/PivotLLM/Maestro/tasks"

	"github.com/PivotLLM/ClawEh/logger"
)

// cachedRunner is the Maestro runner last built for an agent and the Maestro
// base directory it was built for.
type cachedRunner struct {
	base   string
	runner *runner.Runner
}

// runners holds one Maestro runner per agent id across config reloads.
//
// A run started by maestro_task_run executes in the background and outlives
// the reload that rebuilds the agent's tools, but the "run in progress" guard
// lives on the runner. Handing the same runner to the rebuilt tools while a run
// is going keeps maestro_task_status truthful and keeps a second
// maestro_task_run on the same project refused.
var runners = struct {
	mu      sync.Mutex
	byAgent map[string]cachedRunner
}{byAgent: map[string]cachedRunner{}}

// runnerFor returns the Maestro runner the agent's tools should use. The cached
// runner is reused when it serves the same base directory and has a run in
// progress; otherwise a new one is built from the current config and cached.
// A reused runner keeps the config, logger and dispatcher it was built with
// until a reload finds it idle.
func runnerFor(agentID, base string, cfg *mconfig.Config, l *mlogging.Logger, disp mllm.Dispatcher, importAllowed func(string) bool) *runner.Runner {
	runners.mu.Lock()
	defer runners.mu.Unlock()

	prev, cached := runners.byAgent[agentID]
	if cached && prev.base == base && prev.runner.IsRunning() {
		logger.InfoCF("maestro", "reusing Maestro runner with a run in progress across reload",
			map[string]any{"agent": agentID, "base": base})
		return prev.runner
	}

	r := newRunner(cfg, l, disp, importAllowed)
	runners.byAgent[agentID] = cachedRunner{base: base, runner: r}
	reason := "first registration"
	switch {
	case cached && prev.base != base:
		reason = "base directory changed"
	case cached:
		reason = "no run in progress"
	}
	logger.InfoCF("maestro", "built new Maestro runner",
		map[string]any{"agent": agentID, "base": base, "reason": reason})
	return r
}

// newRunner builds a runner the way Maestro's Provider.RegisterTools does when
// no runner is injected, with the host dispatcher. Its services are separate
// instances from the provider's; both work on the same files under the base
// directory.
func newRunner(cfg *mconfig.Config, l *mlogging.Logger, disp mllm.Dispatcher, importAllowed func(string) bool) *runner.Runner {
	refDirs := cfg.ReferenceDirs()
	externalDirs := make([]reference.ExternalDir, 0, len(refDirs))
	for _, rd := range refDirs {
		externalDirs = append(externalDirs, reference.ExternalDir{Path: rd.Path, Mount: rd.Mount})
	}
	refSvc := reference.NewService(
		reference.WithEmbeddedFS(cfg.EmbeddedFS()),
		reference.WithExternalDirs(externalDirs),
		reference.WithLogger(l),
	)
	playbooksSvc := playbooks.NewService(cfg.PlaybooksDir(), l)
	projectsSvc := projects.NewService(cfg, l)
	if importAllowed != nil {
		projectsSvc.SetImportAllowed(importAllowed)
	}
	tasksSvc := tasks.NewService(cfg, projectsSvc, l)
	return runner.New(cfg, l, nil, playbooksSvc, refSvc, disp, tasksSvc, projectsSvc)
}
