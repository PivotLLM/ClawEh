// ClawEh
// License: MIT

package maestro

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

// scriptedRunner stands in for the sub-agent runner: a known model alias (or
// none) answers with canned content and usage; any other alias is refused the
// way Spawner.RunSync refuses it.
type scriptedRunner struct {
	mu      sync.Mutex
	prompts []string
	models  []string
}

func (r *scriptedRunner) RunSync(_ context.Context, task, model string) (*global.SyncResult, error) {
	r.mu.Lock()
	r.prompts = append(r.prompts, task)
	r.models = append(r.models, model)
	r.mu.Unlock()
	if model != "" && !strings.EqualFold(model, "Pro") {
		return nil, fmt.Errorf("%w: model %q is not available for this agent", global.ErrModelNotAvailable, model)
	}
	return &global.SyncResult{
		Content:    `{"answer": "the worker answer"}`,
		Iterations: 2,
		TurnUsage:  global.TurnUsage{Model: "claude-x", Provider: "anthropic", InputTokens: 12, OutputTokens: 3, CostUSD: 0.01},
	}, nil
}

// maestroHarness builds the real Maestro suite through ClawEh's namespaced
// wrapper — the exact path the agent loop and MCP host use — for one agent
// with a mount and a scripted sub-agent runner.
type maestroHarness struct {
	t      *testing.T
	tools  map[string]tools.Tool
	runner *scriptedRunner
	ws     string
	mount  string
}

func newMaestroHarness(t *testing.T) *maestroHarness {
	t.Helper()
	ws := t.TempDir()
	mount := t.TempDir()
	if err := os.WriteFile(filepath.Join(mount, "standard.md"), []byte("# Standard\nrule one"), 0o644); err != nil {
		t.Fatal(err)
	}
	agent := config.AgentConfig{
		ID:      "alice",
		Maestro: &config.MaestroConfig{Enabled: true},
		Mounts:  []config.MountConfig{{Name: "standards", Path: mount}},
	}
	cfg := &config.Config{Agents: config.AgentsConfig{List: []config.AgentConfig{agent}}}
	cfg.Agents.Defaults.RestrictToWorkspace = true
	runner := &scriptedRunner{}

	built := tools.NamespacedProvider("maestro", GlobalProvider).Build(tools.ToolDeps{
		Cfg:       cfg,
		AgentCfg:  &cfg.Agents.List[0],
		AgentID:   "alice",
		Workspace: ws,
		Spawn:     runner,
	})
	if len(built) == 0 {
		t.Fatal("no maestro tools built")
	}
	h := &maestroHarness{t: t, tools: map[string]tools.Tool{}, runner: runner, ws: ws, mount: mount}
	for _, tl := range built {
		h.tools[tl.Name()] = tl
	}
	// Task sets need response/report templates, held in a playbook as in
	// Maestro's own suite.
	h.call("maestro_playbook_create", map[string]any{"name": "pb"}, false)
	h.call("maestro_file_put", map[string]any{"source": "playbook", "playbook": "pb", "path": "templates/worker-response.json",
		"content": `{"type": "object", "additionalProperties": true}`}, false)
	h.call("maestro_file_put", map[string]any{"source": "playbook", "playbook": "pb", "path": "templates/worker-report.md",
		"content": "## Worker Report\n\n{{.WorkResult}}"}, false)
	return h
}

// createTaskSet creates a validated task set using the harness playbook templates.
func (h *maestroHarness) createTaskSet(project, path string) {
	h.t.Helper()
	h.call("maestro_taskset_create", map[string]any{
		"project": project, "path": path, "title": path,
		"worker_response_template": "pb/templates/worker-response.json",
		"worker_report_template":   "pb/templates/worker-report.md",
	}, false)
}

// call executes a tool and returns its ForLLM text, failing on IsError unless
// wantErr is set (in which case the error text is returned).
func (h *maestroHarness) call(name string, args map[string]any, wantErr bool) string {
	h.t.Helper()
	tl, ok := h.tools[name]
	if !ok {
		h.t.Fatalf("tool %s not built", name)
	}
	res := tl.Execute(context.Background(), args)
	if res == nil {
		h.t.Fatalf("%s returned nil", name)
	}
	if res.IsError != wantErr {
		h.t.Fatalf("%s: IsError=%v (want %v): %s", name, res.IsError, wantErr, res.ForLLM)
	}
	return res.ForLLM
}

func (h *maestroHarness) callJSON(name string, args map[string]any) map[string]any {
	h.t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(h.call(name, args, false)), &out); err != nil {
		h.t.Fatalf("%s: not JSON: %v", name, err)
	}
	return out
}

// waitTerminal polls task_list until the single task in path is done or failed.
func (h *maestroHarness) waitTerminal(project, path string) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out := h.callJSON("maestro_task_list", map[string]any{"project": project, "path": path})
		if ts, _ := out["tasks"].([]any); len(ts) == 1 {
			task := ts[0].(map[string]any)
			work := task["work"].(map[string]any)
			if st := work["status"]; st == "done" || st == "failed" {
				return task
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("task in %s/%s did not reach a terminal state", project, path)
	return nil
}

// TestIntegration_TaskRunThroughClawEhWrappers drives the representative
// sequence through the namespaced wrappers: orientation, project, file, task
// set, task, run, result with usage, report; plus a wiring sample of other
// domains. It asserts the seam between ClawEh and Maestro, not Maestro itself.
func TestIntegration_TaskRunThroughClawEhWrappers(t *testing.T) {
	h := newMaestroHarness(t)

	// Every tool is namespaced and none carries the standalone callback_url.
	for name, tl := range h.tools {
		if !strings.HasPrefix(name, "maestro_") {
			t.Errorf("tool %q is not namespaced", name)
		}
		if props, _ := tl.Parameters()["properties"].(map[string]any); props != nil {
			if _, has := props["callback_url"]; has {
				t.Errorf("%s still exposes callback_url", name)
			}
		}
	}
	for _, absent := range []string{"maestro_llm_list", "maestro_llm_dispatch", "maestro_llm_test"} {
		if _, ok := h.tools[absent]; ok {
			t.Errorf("%s must not be built under host dispatch", absent)
		}
	}

	// Orientation carries the host-mode text.
	if sh := h.call("maestro_start_here", map[string]any{}, false); !strings.Contains(sh, "host-dispatched") {
		t.Errorf("start_here lacks host guidance")
	}
	// Health reports host dispatch.
	if hl := h.call("maestro_health", map[string]any{}, false); !strings.Contains(hl, `"dispatch":"host"`) {
		t.Errorf("health = %s", hl)
	}

	// The agent's mount is visible in Maestro's reference domain.
	ref := h.call("maestro_file_get", map[string]any{"source": "reference", "path": "standards/standard.md"}, false)
	if !strings.Contains(ref, "rule one") {
		t.Errorf("mount not mapped into reference domain: %s", ref)
	}
	// Maestro's own data directory is not a reference mount.
	if lst := h.call("maestro_file_list", map[string]any{"source": "reference"}, false); strings.Contains(lst, `"maestro/`) {
		t.Errorf("maestro base directory leaked into the reference domain: %s", lst)
	}

	const project = "seam"
	h.call("maestro_project_create", map[string]any{"name": project, "title": "Seam", "disclaimer_template": "none"}, false)
	h.call("maestro_file_put", map[string]any{"source": "project", "project": project, "path": "notes.md", "content": "hello"}, false)
	if got := h.call("maestro_file_get", map[string]any{"source": "project", "project": project, "path": "notes.md"}, false); !strings.Contains(got, "hello") {
		t.Errorf("project file round trip: %s", got)
	}
	// Wiring sample across the other domains.
	if pl := h.call("maestro_playbook_list", map[string]any{}, false); !strings.Contains(pl, `"pb"`) {
		t.Errorf("playbook_list: %s", pl)
	}
	h.call("maestro_list_create", map[string]any{"list": "items", "name": "Items", "project": project}, false)
	h.call("maestro_project_log_append", map[string]any{"project": project, "message": "seam test"}, false)
	if lg := h.call("maestro_project_log_get", map[string]any{"project": project}, false); !strings.Contains(lg, "seam test") {
		t.Errorf("project_log_get: %s", lg)
	}

	// Task set, task, run.
	h.createTaskSet(project, "main")
	created := h.callJSON("maestro_task_create", map[string]any{"project": project, "path": "main", "title": "Worker", "type": "test", "prompt": "Summarise the notes."})
	uuid, _ := created["uuid"].(string)
	if uuid == "" {
		t.Fatalf("task_create returned no uuid: %+v", created)
	}
	run := h.callJSON("maestro_task_run", map[string]any{"project": project, "path": "main"})
	if tf, _ := run["tasks_found"].(float64); tf != 1 {
		t.Fatalf("task_run tasks_found = %v, want 1: %+v", run["tasks_found"], run)
	}
	task := h.waitTerminal(project, "main")
	work := task["work"].(map[string]any)
	if work["status"] != "done" {
		t.Fatalf("task status = %v, error = %v", work["status"], work["error"])
	}

	// The worker prompt reached the sub-agent runner with the project context.
	h.runner.mu.Lock()
	prompts, models := append([]string(nil), h.runner.prompts...), append([]string(nil), h.runner.models...)
	h.runner.mu.Unlock()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "Summarise the notes.") || !strings.Contains(prompts[0], "Project: "+project) {
		t.Errorf("runner prompts = %q", prompts)
	}
	if models[0] != "" {
		t.Errorf("default task should run on the host default model, got %q", models[0])
	}

	// task_result_get returns the answer (it omits history by design); the
	// result file's history carries the served model and the usage.
	result := h.call("maestro_task_result_get", map[string]any{"project": project, "uuid": uuid}, false)
	if !strings.Contains(result, "the worker answer") {
		t.Errorf("task result lacks the answer:\n%s", result)
	}
	file, err := os.ReadFile(filepath.Join(h.ws, "maestro", "projects", project, "results", uuid+".json"))
	if err != nil {
		t.Fatalf("result file: %v", err)
	}
	var rec struct {
		History []struct {
			Type          string  `json:"type"`
			ProviderModel string  `json:"provider_model"`
			InputTokens   int     `json:"input_tokens"`
			OutputTokens  int     `json:"output_tokens"`
			CostUSD       float64 `json:"cost_usd"`
			NumTurns      int     `json:"num_turns"`
		} `json:"history"`
	}
	if err := json.Unmarshal(file, &rec); err != nil {
		t.Fatalf("parse result file: %v", err)
	}
	var found bool
	for _, m := range rec.History {
		if m.Type != "response" {
			continue
		}
		found = true
		if m.ProviderModel != "claude-x" || m.InputTokens != 12 || m.OutputTokens != 3 || m.CostUSD != 0.01 || m.NumTurns != 2 {
			t.Errorf("response history entry = %+v, want claude-x / 12 / 3 / 0.01 / 2 turns", m)
		}
	}
	if !found {
		t.Errorf("result file has no response history entry:\n%s", file)
	}
	if rep := h.call("maestro_task_report", map[string]any{"project": project, "format": "markdown"}, false); !strings.Contains(rep, "Report") {
		t.Errorf("task_report: %s", rep)
	}
}

// TestIntegration_ModelHintAndPermanentFailure: a known alias reaches the
// runner; an unknown alias fails the task once, permanently, and the
// completion notification is delivered through the async wrapper.
func TestIntegration_ModelHintAndPermanentFailure(t *testing.T) {
	h := newMaestroHarness(t)
	const project = "hints"
	h.call("maestro_project_create", map[string]any{"name": project, "title": "Hints", "disclaimer_template": "none"}, false)

	// Known alias passes through.
	h.createTaskSet(project, "ok")
	h.callJSON("maestro_task_create", map[string]any{"project": project, "path": "ok", "title": "W", "prompt": "go", "llm_model_id": "Pro"})
	h.callJSON("maestro_task_run", map[string]any{"project": project, "path": "ok"})
	if task := h.waitTerminal(project, "ok"); task["work"].(map[string]any)["status"] != "done" {
		t.Fatalf("known alias task: %+v", task["work"])
	}
	h.runner.mu.Lock()
	lastModel := h.runner.models[len(h.runner.models)-1]
	h.runner.mu.Unlock()
	if lastModel != "Pro" {
		t.Errorf("alias not passed through, runner saw %q", lastModel)
	}

	// Unknown alias via task_dispatch: permanent failure + notification.
	tl := h.tools["maestro_task_dispatch"]
	async, ok := tl.(tools.AsyncExecutor)
	if !ok {
		t.Fatal("task_dispatch must be an async tool (delivers a completion notification)")
	}
	notified := make(chan *tools.ToolResult, 1)
	res := async.ExecuteAsync(context.Background(), map[string]any{
		"project": project, "path": "bad", "prompt": "nest", "llm_model_id": "no-such-alias",
	}, func(_ context.Context, r *tools.ToolResult) { notified <- r })
	if res == nil || res.IsError {
		t.Fatalf("task_dispatch: %+v", res)
	}
	var disp map[string]any
	if err := json.Unmarshal([]byte(res.ForLLM), &disp); err != nil || disp["status"] != "running" {
		t.Fatalf("task_dispatch result = %s", res.ForLLM)
	}
	select {
	case n := <-notified:
		if n == nil || !strings.Contains(n.ForLLM, "TASK NOTIFICATION") || !strings.Contains(strings.ToLower(n.ForLLM), "fail") {
			t.Errorf("notification = %+v", n)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no completion notification for the dispatched task")
	}
	task := h.waitTerminal(project, "bad")
	work := task["work"].(map[string]any)
	if work["status"] != "failed" || !strings.Contains(fmt.Sprint(work["error"]), "permanent") {
		t.Errorf("unknown alias task = %+v", work)
	}
	if inv, _ := work["invocations"].(float64); inv != 1 {
		t.Errorf("invocations = %v, want 1 (no retry)", work["invocations"])
	}
	result := h.call("maestro_task_result_get", map[string]any{"project": project, "uuid": disp["uuid"].(string)}, false)
	if !strings.Contains(result, "dispatch_permanent_error") {
		t.Errorf("result lacks error_code: %s", result)
	}
}

// TestIntegration_ImportConfinedToAgentReach: file_import through the wrappers
// obeys the agent's read reach — a mount is importable, an arbitrary host
// path is refused.
func TestIntegration_ImportConfinedToAgentReach(t *testing.T) {
	h := newMaestroHarness(t)
	const project = "imp"
	h.call("maestro_project_create", map[string]any{"name": project, "title": "Imp", "disclaimer_template": "none"}, false)

	ok := h.call("maestro_file_import", map[string]any{"project": project, "source": filepath.Join(h.mount, "standard.md")}, false)
	if !strings.Contains(ok, `"files_imported":1`) {
		t.Errorf("import from mount: %s", ok)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused := h.call("maestro_file_import", map[string]any{"project": project, "source": outside}, true)
	if !strings.Contains(refused, "import refused") {
		t.Errorf("import from outside: %s", refused)
	}
	if strings.Contains(h.call("maestro_file_list", map[string]any{"source": "project", "project": project}, false), "secret.txt") {
		t.Error("refused file appeared in the project")
	}
}
