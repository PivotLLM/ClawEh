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

// cachedRunnerFor returns the runner cached for agentID, or nil.
func cachedRunnerFor(agentID string) *runner.Runner {
	runners.mu.Lock()
	defer runners.mu.Unlock()
	return runners.byAgent[agentID].runner
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

func (r *registration) register(t *testing.T, sr global.SyncRunner) *maestroHarness {
	t.Helper()
	built := tools.NamespacedProvider("maestro", GlobalProvider).Build(tools.ToolDeps{
		Cfg:       r.cfg,
		AgentCfg:  &r.cfg.Agents.List[0],
		AgentID:   r.cfg.Agents.List[0].ID,
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

// TestRunnerCache_RunInProgressSurvivesReload: a reload while a run is going
// hands the rebuilt tools the same runner, so status still reports the run
// and a second run on the same project is refused.
func TestRunnerCache_RunInProgressSurvivesReload(t *testing.T) {
	const agentID, project = "alice-reload-running", "audit"
	reg := newRegistration(t, agentID)
	first := newGatedRunner()
	h1 := reg.register(t, first)
	runnerBefore := cachedRunnerFor(agentID)

	h1.call("maestro_playbook_create", map[string]any{"name": "pb"}, false)
	h1.call("maestro_file_put", map[string]any{
		"source": "playbook", "playbook": "pb", "path": "templates/worker-response.json",
		"content": `{"type": "object", "additionalProperties": true}`,
	}, false)
	h1.call("maestro_file_put", map[string]any{
		"source": "playbook", "playbook": "pb", "path": "templates/worker-report.md",
		"content": "## Worker Report\n\n{{.WorkResult}}",
	}, false)
	h1.call("maestro_project_create", map[string]any{"name": project, "title": "Audit", "disclaimer_template": "none"}, false)
	h1.createTaskSet(project, "main")
	h1.callJSON("maestro_task_create", map[string]any{"project": project, "path": "main", "title": "Worker", "prompt": "Check it."})
	// Start the run on the registration's runner directly rather than through
	// maestro_task_run: Maestro v0.5.3's task_run handler marshals the RunResult
	// while the run goroutine is still updating it (an upstream data race that
	// -race reports intermittently). The reload behaviour under test is the
	// same, and the second maestro_task_run below does go through the tool.
	run, err := runnerBefore.Run(context.Background(), &mglobal.RunRequest{Project: project, Path: "main"}, nil)
	if err != nil || run.TasksFound != 1 {
		t.Fatalf("Run = %+v, %v; want one task queued", run, err)
	}
	select {
	case <-first.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the run never reached the sub-agent runner")
	}

	// Config reload: the tools are rebuilt with a new sub-agent runner.
	h2 := reg.register(t, newGatedRunner())
	if got := cachedRunnerFor(agentID); got != runnerBefore {
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

	// Let the original run finish; it completes through the reused runner.
	close(first.release)
	task := h2.waitTerminal(project, "main")
	if work, ok := task["work"].(map[string]any); !ok || work["status"] != "done" {
		t.Fatalf("task did not complete: %+v", task["work"])
	}
	deadline := time.Now().Add(10 * time.Second)
	for runnerBefore.IsRunning() {
		if time.Now().After(deadline) {
			t.Fatal("the run did not release its in-progress guard")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRunnerCache_IdleRunnerReplacedOnReload: with no run in progress a reload
// builds a new runner from the current config, and the old one is dropped.
func TestRunnerCache_IdleRunnerReplacedOnReload(t *testing.T) {
	const agentID = "alice-reload-idle"
	reg := newRegistration(t, agentID)
	reg.register(t, newGatedRunner())
	before := cachedRunnerFor(agentID)
	if before == nil {
		t.Fatal("no runner cached after the first registration")
	}
	reg.register(t, newGatedRunner())
	after := cachedRunnerFor(agentID)
	if after == nil || after == before {
		t.Fatal("an idle runner was not replaced on reload")
	}
}

// TestRunnerCache_BaseChangeGetsNewRunner: a cached runner serving another base
// directory is never handed to the agent, even with a run in progress.
func TestRunnerCache_BaseChangeGetsNewRunner(t *testing.T) {
	const agentID = "alice-reload-base"
	reg := newRegistration(t, agentID)
	reg.register(t, newGatedRunner())
	before := cachedRunnerFor(agentID)

	reg.ws = t.TempDir()
	reg.register(t, newGatedRunner())
	if after := cachedRunnerFor(agentID); after == before {
		t.Fatal("a runner built for another base directory was reused")
	}
}
