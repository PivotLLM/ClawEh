// ClawEh
// License: MIT

package maestro

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	mconfig "github.com/PivotLLM/Maestro/config"
	mllm "github.com/PivotLLM/Maestro/llm"
	mlogging "github.com/PivotLLM/Maestro/logging"
	"github.com/PivotLLM/Maestro/playbooks"
	"github.com/PivotLLM/Maestro/projects"
	"github.com/PivotLLM/Maestro/reference"
	"github.com/PivotLLM/Maestro/runner"
	"github.com/PivotLLM/Maestro/tasks"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

// reasonChangedWhileRunning is why a runner is kept although the settings it
// was built from changed.
const reasonChangedWhileRunning = "configuration changed but a run is in progress"

// reuseNote is logged when a runner is kept across a settings change: what was
// baked into it is not refreshed until it is rebuilt.
const reuseNote = "Maestro runner settings and reference directories stay at the configuration the runner was built with until it is rebuilt"

// swapDispatcher is the dispatcher a runner is built with. It forwards to the
// dispatcher of the latest registration, so a reload's new sub-agent runner and
// turn timeout reach a runner that is kept across it.
type swapDispatcher struct {
	cur atomic.Pointer[dispatcher]
}

func newSwapDispatcher(d *dispatcher) *swapDispatcher {
	s := &swapDispatcher{}
	s.cur.Store(d)
	return s
}

func (s *swapDispatcher) set(d *dispatcher) { s.cur.Store(d) }

func (s *swapDispatcher) Dispatch(ctx context.Context, req *mllm.DispatchRequest) (*mllm.DispatchResult, error) {
	return s.cur.Load().Dispatch(ctx, req)
}

func (s *swapDispatcher) GetLLM(llmID string) *mconfig.LLM { return s.cur.Load().GetLLM(llmID) }

func (s *swapDispatcher) GetExecInfo(llmID string) *mllm.LLMExecInfo {
	return s.cur.Load().GetExecInfo(llmID)
}

func (s *swapDispatcher) TestLLM(llmID string) (bool, error) { return s.cur.Load().TestLLM(llmID) }

// runnerSpec is the part of an agent's config a runner is built from and
// cannot change afterwards.
type runnerSpec struct {
	base    string
	runCfg  mconfig.Runner
	refDirs []mconfig.ReferenceDir
}

// cachedRunner is an agent's Maestro runner, the spec it was built from and
// the dispatcher it dispatches through.
type cachedRunner struct {
	spec   runnerSpec
	runner *runner.Runner
	disp   *swapDispatcher
}

// runners holds one Maestro runner per agent for the life of the process.
//
// A run started by maestro_task_run executes in the background and outlives
// the reload that rebuilds the agent's tools, and the "run in progress" guard
// lives on the runner. So every registration of the agent's tools (at boot and
// on each reload) gets the cached runner, and tools a turn still holds from
// before a reload share it with the new ones, unless the runner has to be
// rebuilt: the base directory changed, or the Maestro runner settings or the
// reference directories (the agent's mounts) changed while no run was in
// progress. In that residual case a turn still holding the old tools can start
// a run on the old runner, which the new tools do not see.
var runners = struct {
	mu      sync.Mutex
	byAgent map[string]*cachedRunner
}{byAgent: map[string]*cachedRunner{}}

// runnerFor returns the Maestro runner and dispatcher the agent's tools use,
// and points that dispatcher at disp. The cached runner is kept unless the
// base directory changed, or its runner settings or reference directories
// changed while it is idle; then a new one is built outside the lock and the
// decision is re-checked before it is cached.
func runnerFor(agentID string, spec runnerSpec, disp *dispatcher, cfg *mconfig.Config, l *mlogging.Logger) (*runner.Runner, *swapDispatcher) {
	if r, sw, ok := reuseRunner(agentID, spec, disp); ok {
		return r, sw
	}

	sw := newSwapDispatcher(disp)
	r := newRunner(cfg, l, sw)

	runners.mu.Lock()
	prev, cached := runners.byAgent[agentID]
	keep, reason := keepRunner(prev, cached, spec)
	if keep {
		// Another registration cached a runner this one can use meanwhile (or
		// the old one started a run); ours is discarded unused.
		prev.disp.set(disp)
		runners.mu.Unlock()
		logReuse(agentID, spec.base, reason, prev.runner)
		return prev.runner, prev.disp
	}
	runners.byAgent[agentID] = &cachedRunner{spec: spec, runner: r, disp: sw}
	runners.mu.Unlock()

	fields := map[string]any{"agent": agentID, "base": spec.base, "reason": reason}
	if cached && prev.runner.IsRunning() {
		fields["previous_runner"] = "run in progress continues on the previous runner"
	}
	logger.InfoCF("maestro", "built new Maestro runner", fields)
	return r, sw
}

// reuseRunner returns the cached runner, pointed at disp, when it can be kept.
func reuseRunner(agentID string, spec runnerSpec, disp *dispatcher) (*runner.Runner, *swapDispatcher, bool) {
	runners.mu.Lock()
	prev, cached := runners.byAgent[agentID]
	keep, reason := keepRunner(prev, cached, spec)
	if keep {
		prev.disp.set(disp)
	}
	runners.mu.Unlock()
	if !keep {
		return nil, nil, false
	}
	logReuse(agentID, spec.base, reason, prev.runner)
	return prev.runner, prev.disp, true
}

// keepRunner decides whether the cached runner serves spec, and why.
func keepRunner(prev *cachedRunner, cached bool, spec runnerSpec) (bool, string) {
	switch {
	case !cached:
		return false, "first registration"
	case prev.spec.base != spec.base:
		return false, "base directory changed"
	case reflect.DeepEqual(prev.spec.runCfg, spec.runCfg) && slices.Equal(prev.spec.refDirs, spec.refDirs):
		return true, "configuration unchanged"
	case prev.runner.IsRunning():
		return true, reasonChangedWhileRunning
	default:
		return false, "configuration changed"
	}
}

func logReuse(agentID, base, reason string, r *runner.Runner) {
	fields := map[string]any{"agent": agentID, "base": base, "reason": reason, "run_in_progress": r.IsRunning()}
	if reason == reasonChangedWhileRunning {
		fields["note"] = reuseNote
	}
	logger.InfoCF("maestro", "reusing Maestro runner", fields)
}

// pruneRunners drops the idle runners of agents no longer in the config. A
// runner with a run in progress is kept until a later registration finds it
// idle.
func pruneRunners(c *config.Config) {
	runners.mu.Lock()
	defer runners.mu.Unlock()
	for id, e := range runners.byAgent {
		if c.AgentByID(id) != nil || e.runner.IsRunning() {
			continue
		}
		delete(runners.byAgent, id)
		logger.InfoCF("maestro", "dropped Maestro runner of an agent no longer configured",
			map[string]any{"agent": id, "base": e.spec.base})
	}
}

// newRunner builds a runner the way Maestro's Provider.RegisterTools does when
// no runner is injected, with the host dispatcher. Its services are separate
// instances from the provider's; both work on the same files under the base
// directory. The runner never imports files, so its projects service needs no
// file_import rule; file_import is served by the provider's own service.
func newRunner(cfg *mconfig.Config, l *mlogging.Logger, disp mllm.Dispatcher) *runner.Runner {
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
	tasksSvc := tasks.NewService(cfg, projectsSvc, l)
	return runner.New(cfg, l, nil, playbooksSvc, refSvc, disp, tasksSvc, projectsSvc)
}
