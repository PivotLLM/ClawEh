// ClawEh
// License: MIT

package maestro

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	mglobal "github.com/PivotLLM/Maestro/global"
	"github.com/PivotLLM/Maestro/runner"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

// cachedEntry returns the cache entry for agentID, or nil.
func cachedEntry(agentID string) *cachedRunner {
	runners.mu.Lock()
	defer runners.mu.Unlock()
	return runners.byAgent[agentID]
}

// cachedRunnerFor returns the runner cached for agentID, or nil.
func cachedRunnerFor(agentID string) *runner.Runner {
	if e := cachedEntry(agentID); e != nil {
		return e.runner
	}
	return nil
}

// gatedRunner is a SyncRunner whose runs block until the test releases them,
// standing in for a sub-agent still working when the config is reloaded.
type gatedRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedRunner() *gatedRunner {
	return &gatedRunner{started: make(chan struct{}), release: make(chan struct{})}
}

func (r *gatedRunner) RunSync(ctx context.Context, _, _ string) (*global.SyncResult, error) {
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &global.SyncResult{Content: `{"answer": "done"}`, Iterations: 1}, nil
}

// registration builds Maestro's tools for one agent the way the agent loop does
// at boot and on every config reload, over a shared config and workspace.
type registration struct {
	cfg *config.Config
	ws  string
}

func newRegistration(t *testing.T, agentID string) *registration {
	t.Helper()
	agent := config.AgentConfig{ID: agentID, Maestro: &config.MaestroConfig{Enabled: true}}
	return &registration{
		cfg: &config.Config{Agents: config.AgentsConfig{List: []config.AgentConfig{agent}}},
		ws:  t.TempDir(),
	}
}

func (r *registration) agentID() string { return r.cfg.Agents.List[0].ID }

func (r *registration) register(t *testing.T, sr global.SyncRunner) *maestroHarness {
	t.Helper()
	built := tools.NamespacedProvider("maestro", GlobalProvider).Build(tools.ToolDeps{
		Cfg:       r.cfg,
		AgentCfg:  &r.cfg.Agents.List[0],
		AgentID:   r.agentID(),
		Workspace: r.ws,
		Spawn:     sr,
	})
	if len(built) == 0 {
		t.Fatal("no maestro tools built")
	}
	h := &maestroHarness{t: t, tools: map[string]tools.Tool{}, ws: r.ws}
	for _, tl := range built {
		h.tools[tl.Name()] = tl
	}
	return h
}

// prepareProject creates a playbook with templates, a project and one task in
// task set "main", ready to run.
func prepareProject(h *maestroHarness, project string) {
	h.t.Helper()
	h.call("maestro_playbook_create", map[string]any{"name": "pb"}, false)
	h.call("maestro_file_put", map[string]any{
		"source": "playbook", "playbook": "pb", "path": "templates/worker-response.json",
		"content": `{"type": "object", "additionalProperties": true}`,
	}, false)
	h.call("maestro_file_put", map[string]any{
		"source": "playbook", "playbook": "pb", "path": "templates/worker-report.md",
		"content": "## Worker Report\n\n{{.WorkResult}}",
	}, false)
	h.call("maestro_project_create", map[string]any{"name": project, "title": project, "disclaimer_template": "none"}, false)
	h.createTaskSet(project, "main")
	h.callJSON("maestro_task_create", map[string]any{"project": project, "path": "main", "title": "Worker", "prompt": "Check it."})
}

// startRun starts a run of project on r and waits until it reaches gate.
//
// The run is started on the runner directly rather than through
// maestro_task_run: Maestro v0.5.3's task_run handler marshals the RunResult
// while the run goroutine is still updating it (an upstream data race that
// -race reports intermittently).
func startRun(t *testing.T, r *runner.Runner, project string, gate *gatedRunner) {
	t.Helper()
	run, err := r.Run(context.Background(), &mglobal.RunRequest{Project: project, Path: "main"}, nil)
	if err != nil || run.TasksFound != 1 {
		t.Fatalf("Run = %+v, %v; want one task queued", run, err)
	}
	if gate == nil {
		return
	}
	select {
	case <-gate.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the run never reached the sub-agent runner")
	}
}

// waitIdle waits until r has no run in progress.
func waitIdle(t *testing.T, r *runner.Runner) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for r.IsRunning() {
		if time.Now().After(deadline) {
			t.Fatal("the run did not release its in-progress guard")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRunnerCache_RunInProgressSurvivesReload: a reload while a run is going
// hands the rebuilt tools the same runner, so status still reports the run
// and a second run on the same project is refused.
func TestRunnerCache_RunInProgressSurvivesReload(t *testing.T) {
	const project = "audit"
	reg := newRegistration(t, "alice-reload-running")
	first := newGatedRunner()
	h1 := reg.register(t, first)
	runnerBefore := cachedRunnerFor(reg.agentID())
	prepareProject(h1, project)
	startRun(t, runnerBefore, project, first)

	// Config reload: the tools are rebuilt with a new sub-agent runner.
	h2 := reg.register(t, newGatedRunner())
	if got := cachedRunnerFor(reg.agentID()); got != runnerBefore {
		t.Fatal("reload replaced the Maestro runner while a run was in progress")
	}
	status := h2.callJSON("maestro_task_status", map[string]any{"project": project})
	if status["run_in_progress"] != true {
		t.Errorf("task_status after reload: run_in_progress = %v, want true", status["run_in_progress"])
	}
	second := h2.callJSON("maestro_task_run", map[string]any{"project": project, "path": "main"})
	if msg, ok := second["message"].(string); !ok || !strings.Contains(msg, "a run is already in progress") {
		t.Errorf("second task_run after reload = %+v, want it refused as already in progress", second)
	}

	// Let the original run finish; it completes through the kept runner.
	close(first.release)
	task := h2.waitTerminal(project, "main")
	if work, ok := task["work"].(map[string]any); !ok || work["status"] != "done" {
		t.Fatalf("task did not complete: %+v", task["work"])
	}
	waitIdle(t, runnerBefore)
}

// TestRunnerCache_IdleRunnerWithUnchangedConfigKept: a reload that changes
// nothing the runner is built from keeps it, so tools a turn still holds from
// before the reload share its run guard with the new ones.
func TestRunnerCache_IdleRunnerWithUnchangedConfigKept(t *testing.T) {
	reg := newRegistration(t, "alice-reload-idle")
	reg.register(t, newGatedRunner())
	before := cachedRunnerFor(reg.agentID())
	if before == nil {
		t.Fatal("no runner cached after the first registration")
	}
	reg.register(t, newGatedRunner())
	if after := cachedRunnerFor(reg.agentID()); after != before {
		t.Fatal("an idle runner with unchanged config was replaced on reload")
	}
}

// TestRunnerCache_IdleRunnerWithChangedConfigRebuilt: changed runner settings
// on an idle runner build a new runner from the current config.
func TestRunnerCache_IdleRunnerWithChangedConfigRebuilt(t *testing.T) {
	reg := newRegistration(t, "alice-reload-changed")
	reg.register(t, newGatedRunner())
	before := cachedRunnerFor(reg.agentID())

	reg.cfg.Agents.List[0].Maestro.MaxConcurrent = 3
	reg.register(t, newGatedRunner())
	if after := cachedRunnerFor(reg.agentID()); after == nil || after == before {
		t.Fatal("an idle runner was kept although its settings changed")
	}
}

// TestRunnerCache_ChangedConfigDuringRunKept: changed runner settings do not
// replace a runner with a run in progress.
func TestRunnerCache_ChangedConfigDuringRunKept(t *testing.T) {
	const project = "busy"
	reg := newRegistration(t, "alice-reload-changed-busy")
	gate := newGatedRunner()
	h := reg.register(t, gate)
	before := cachedRunnerFor(reg.agentID())
	prepareProject(h, project)
	startRun(t, before, project, gate)

	reg.cfg.Agents.List[0].Maestro.MaxConcurrent = 3
	reg.register(t, newGatedRunner())
	if after := cachedRunnerFor(reg.agentID()); after != before {
		t.Error("a runner with a run in progress was replaced on a settings change")
	}
	close(gate.release)
	waitIdle(t, before)
}

// TestRunnerCache_BaseChangeGetsNewRunner: a new base directory gets a new
// runner even while the old one has a run in progress; that run continues.
func TestRunnerCache_BaseChangeGetsNewRunner(t *testing.T) {
	const project = "moving"
	reg := newRegistration(t, "alice-reload-base")
	gate := newGatedRunner()
	h := reg.register(t, gate)
	before := cachedRunnerFor(reg.agentID())
	prepareProject(h, project)
	startRun(t, before, project, gate)

	reg.ws = t.TempDir()
	reg.register(t, newGatedRunner())
	after := cachedRunnerFor(reg.agentID())
	if after == nil || after == before {
		t.Fatal("a runner built for another base directory was reused")
	}
	if !before.IsRunning() {
		t.Error("the previous runner's run was lost on the base change")
	}
	close(gate.release)
	waitIdle(t, before)
}

// TestRunnerCache_KeptRunnerUsesNewDispatcher: after a re-registration with a
// new sub-agent runner and turn timeout, the kept runner dispatches through
// the new ones.
func TestRunnerCache_KeptRunnerUsesNewDispatcher(t *testing.T) {
	const project = "swap"
	reg := newRegistration(t, "alice-reload-dispatch")
	reg.cfg.Agents.Defaults.TurnTimeout = 60
	oldRunner := &scriptedRunner{}
	h := reg.register(t, oldRunner)
	before := cachedRunnerFor(reg.agentID())
	prepareProject(h, project)

	reg.cfg.Agents.Defaults.TurnTimeout = 120
	newRunner := &scriptedRunner{}
	h2 := reg.register(t, newRunner)
	entry := cachedEntry(reg.agentID())
	if entry.runner != before {
		t.Fatal("an idle runner with unchanged Maestro settings was replaced")
	}
	if cur := entry.disp.cur.Load(); cur.timeout != 120*time.Second || cur.run != newRunner {
		t.Errorf("dispatcher = timeout %s, runner %p; want 2m0s and the new runner", cur.timeout, cur.run)
	}

	startRun(t, before, project, nil)
	h2.waitTerminal(project, "main")
	waitIdle(t, before)
	oldRunner.mu.Lock()
	oldCalls := len(oldRunner.prompts)
	oldRunner.mu.Unlock()
	newRunner.mu.Lock()
	newCalls := len(newRunner.prompts)
	newRunner.mu.Unlock()
	if oldCalls != 0 || newCalls != 1 {
		t.Errorf("dispatches: old sub-agent runner %d, new %d; want 0 and 1", oldCalls, newCalls)
	}
}

// TestRunnerCache_PrunesRemovedAgents: an idle runner of an agent no longer in
// the config is dropped; one with a run in progress is kept.
func TestRunnerCache_PrunesRemovedAgents(t *testing.T) {
	const project = "gone"
	idle := newRegistration(t, "alice-removed-idle")
	idle.register(t, newGatedRunner())
	busy := newRegistration(t, "alice-removed-busy")
	gate := newGatedRunner()
	h := busy.register(t, gate)
	busyRunner := cachedRunnerFor(busy.agentID())
	prepareProject(h, project)
	startRun(t, busyRunner, project, gate)

	pruneRunners(&config.Config{})
	if cachedRunnerFor(idle.agentID()) != nil {
		t.Error("the idle runner of a removed agent was kept")
	}
	if cachedRunnerFor(busy.agentID()) != busyRunner {
		t.Error("a runner with a run in progress was dropped")
	}

	close(gate.release)
	waitIdle(t, busyRunner)
	pruneRunners(&config.Config{})
	if cachedRunnerFor(busy.agentID()) != nil {
		t.Error("the removed agent's runner was kept after its run finished")
	}
}
