// ClawEh
// License: MIT

package maestro

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mconfig "github.com/PivotLLM/Maestro/config"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// ---- file_import: negative and positive sandbox cases -----------------------

func writeSecret(t *testing.T) string {
	t.Helper()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("s3cret"), 0o644); err != nil {
		t.Fatal(err)
	}
	return outside
}

func (h *maestroHarness) projectHasSecret(project string) bool {
	h.t.Helper()
	lst := h.call("maestro_file_list", map[string]any{"source": "project", "project": project}, false)
	return strings.Contains(lst, "secret.txt")
}

// TestSecurity_ImportTraversalFromMountRefused: a source that starts inside a
// mount but climbs out with ".." is refused (paths are resolved before the
// sandbox check).
func TestSecurity_ImportTraversalFromMountRefused(t *testing.T) {
	h := newMaestroHarness(t)
	const project = "sec"
	h.call("maestro_project_create", map[string]any{"name": project, "title": "Sec", "disclaimer_template": "none"}, false)
	outside := writeSecret(t)
	// t.TempDir() directories are siblings under one parent, so ".." from the
	// mount reaches the secret's directory.
	traversal := filepath.Join(h.mount, "..", filepath.Base(filepath.Dir(outside)), "secret.txt")
	if _, err := os.Stat(traversal); err != nil {
		t.Fatalf("traversal path does not resolve: %v", err)
	}
	res := h.call("maestro_file_import", map[string]any{"project": project, "source": traversal}, true)
	if !strings.Contains(res, "import refused") {
		t.Errorf("traversal import = %s", res)
	}
	if h.projectHasSecret(project) {
		t.Error("secret reached the project via traversal")
	}
}

// TestSecurity_ImportSymlinkEscapeRefused: a symlink inside a mount that points
// outside the agent's reach is refused as a source and skipped inside a
// directory import.
func TestSecurity_ImportSymlinkEscapeRefused(t *testing.T) {
	h := newMaestroHarness(t)
	const project = "sec"
	h.call("maestro_project_create", map[string]any{"name": project, "title": "Sec", "disclaimer_template": "none"}, false)
	outside := writeSecret(t)
	link := filepath.Join(h.mount, "leak.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	res := h.call("maestro_file_import", map[string]any{"project": project, "source": link}, true)
	if !strings.Contains(res, "import refused") {
		t.Errorf("symlink source import = %s", res)
	}

	dir := h.call("maestro_file_import", map[string]any{"project": project, "source": h.mount, "recursive": true}, false)
	var out map[string]any
	if err := json.Unmarshal([]byte(dir), &out); err != nil {
		t.Fatalf("import result: %v", err)
	}
	if out["files_imported"] != float64(1) || out["files_refused"] != float64(1) {
		t.Errorf("directory import = %s, want 1 imported (standard.md) and 1 refused (leak.txt)", dir)
	}
	if _, err := os.Lstat(filepath.Join(h.ws, "maestro", "projects", project, "files", "imported", filepath.Base(h.mount), "leak.txt")); !os.IsNotExist(err) {
		t.Error("escaping symlink was copied into the project")
	}
}

// TestSecurity_ImportBadSources: relative, empty and non-existent sources are
// errors, never imports.
func TestSecurity_ImportBadSources(t *testing.T) {
	h := newMaestroHarness(t)
	const project = "sec"
	h.call("maestro_project_create", map[string]any{"name": project, "title": "Sec", "disclaimer_template": "none"}, false)
	for _, src := range []string{"", "relative/path.txt", "../../etc/passwd", "/nonexistent/anywhere.txt"} {
		tl := h.tools["maestro_file_import"]
		res := tl.Execute(context.Background(), map[string]any{"project": project, "source": src})
		if res == nil || !res.IsError {
			t.Errorf("source %q: expected an error, got %+v", src, res)
		}
	}
	if h.projectHasSecret(project) {
		t.Error("a bad source produced a project file")
	}
}

// TestSecurity_ImportInsideWorkspaceAllowed: the agent's own workspace,
// including Maestro's project files, is importable.
func TestSecurity_ImportInsideWorkspaceAllowed(t *testing.T) {
	h := newMaestroHarness(t)
	const project = "sec"
	h.call("maestro_project_create", map[string]any{"name": project, "title": "Sec", "disclaimer_template": "none"}, false)
	own := filepath.Join(h.ws, "files", "notes.md")
	if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := h.call("maestro_file_import", map[string]any{"project": project, "source": own}, false)
	if !strings.Contains(res, `"files_imported":1`) {
		t.Errorf("workspace import = %s", res)
	}
}

// ---- reference domain isolation and mount handling ---------------------------

// TestSecurity_ReferenceDomainIsPerAgent: one agent's mounts do not appear in
// another agent's Maestro reference domain.
func TestSecurity_ReferenceDomainIsPerAgent(t *testing.T) {
	a := newMaestroHarness(t)
	otherMount := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherMount, "policy.md"), []byte("p"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Agents: config.AgentsConfig{List: []config.AgentConfig{{
		ID: "bob", Maestro: &config.MaestroConfig{Enabled: true},
		Mounts: []config.MountConfig{{Name: "policies", Path: otherMount}},
	}}}}
	built := tools.NamespacedProvider("maestro", GlobalProvider).Build(tools.ToolDeps{
		Cfg: cfg, AgentCfg: &cfg.Agents.List[0], AgentID: "bob", Workspace: t.TempDir(), Spawn: &scriptedRunner{},
	})
	var bobList tools.Tool
	for _, tl := range built {
		if tl.Name() == "maestro_file_list" {
			bobList = tl
		}
	}
	res := bobList.Execute(context.Background(), map[string]any{"source": "reference"})
	if strings.Contains(res.ForLLM, "standards/") || !strings.Contains(res.ForLLM, "policies/") {
		t.Errorf("bob's reference domain = %s (must show policies/, not alice's standards/)", res.ForLLM)
	}
	aliceRes := a.call("maestro_file_list", map[string]any{"source": "reference"}, false)
	if strings.Contains(aliceRes, "policies/") {
		t.Error("alice's reference domain shows bob's mount")
	}
}

// TestReferenceDirs_UserMaestroMountAndInvalidNamesSkipped: an operator mount
// literally named "maestro" (any case) and a mount with an invalid name are
// both left out of the reference domain instead of breaking registration.
func TestReferenceDirs_UserMaestroMountAndInvalidNamesSkipped(t *testing.T) {
	ws := t.TempDir()
	good := t.TempDir()
	a := &config.AgentConfig{ID: "a", Maestro: &config.MaestroConfig{Enabled: true}, Mounts: []config.MountConfig{
		{Name: "MAESTRO", Path: t.TempDir()},
		{Name: "bad/name", Path: t.TempDir()},
		{Name: "docs", Path: good},
	}}
	dirs := referenceDirsFromMounts(a, ws)
	if len(dirs) != 1 || dirs[0].Mount != "docs" {
		t.Fatalf("dirs = %+v, want only docs", dirs)
	}
	// And Maestro accepts what we hand it.
	c := mconfig.New(mconfig.WithBaseDir(filepath.Join(ws, "maestro")), mconfig.WithReferenceDirs(dirs))
	if err := c.Prepare(); err != nil {
		t.Fatalf("Prepare with mapped dirs: %v", err)
	}
}

// ---- orchestration state and runner settings end to end ---------------------

// TestSecurity_SubagentDepthReachesRunner: the depth carried by the tool call
// survives Maestro's asynchronous run, so the recursion bound applies to
// workers a Maestro task spawns.
func TestSecurity_SubagentDepthReachesRunner(t *testing.T) {
	h := newMaestroHarness(t)
	const project = "depth"
	h.call("maestro_project_create", map[string]any{"name": project, "title": "Depth", "disclaimer_template": "none"}, false)
	h.createTaskSet(project, "main")
	h.callJSON("maestro_task_create", map[string]any{"project": project, "path": "main", "title": "W", "prompt": "go"})

	ctx := toolsagents.WithSpawnDepth(context.Background(), 2)
	h.callCtx(ctx, "maestro_task_run", map[string]any{"project": project, "path": "main"})
	h.waitTerminal(project, "main")

	h.runner.mu.Lock()
	depths := append([]int(nil), h.runner.depths...)
	h.runner.mu.Unlock()
	if len(depths) != 1 || depths[0] != 2 {
		t.Errorf("runner saw depths %v, want [2] (depth lost across task_run)", depths)
	}
}

// TestRunner_AllowParallelFalse_EndToEnd: with allow_parallel=false a task
// set created with parallel=true still runs, sequentially, and the project
// log records why.
func TestRunner_AllowParallelFalse_EndToEnd(t *testing.T) {
	f := false
	h := newMaestroHarnessWith(t, &config.MaestroConfig{Enabled: true, AllowParallel: &f})
	const project = "par"
	h.call("maestro_project_create", map[string]any{"name": project, "title": "Par", "disclaimer_template": "none"}, false)
	h.call("maestro_taskset_create", map[string]any{
		"project": project, "path": "main", "title": "main", "parallel": true,
		"worker_response_template": "pb/templates/worker-response.json",
		"worker_report_template":   "pb/templates/worker-report.md",
	}, false)
	for range 2 {
		h.callJSON("maestro_task_create", map[string]any{"project": project, "path": "main", "title": "W", "prompt": "go"})
	}
	h.callJSON("maestro_task_run", map[string]any{"project": project, "path": "main"})

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st := h.callJSON("maestro_task_status", map[string]any{"project": project, "path": "main"})
		if done, ok := st["done"].(float64); ok && done == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	log := h.call("maestro_project_log_get", map[string]any{"project": project}, false)
	if !strings.Contains(log, "not allowed by runner configuration") {
		t.Errorf("project log lacks the sequential-fallback notice:\n%s", log)
	}
	h.runner.mu.Lock()
	n := len(h.runner.prompts)
	h.runner.mu.Unlock()
	if n != 2 {
		t.Errorf("runner calls = %d, want 2 (tasks still executed)", n)
	}
}

// TestRunnerConfig_NegativeValuesTakeMaestroDefaults: a nonsensical block
// value cannot disable a limit; Maestro replaces non-positive values.
func TestRunnerConfig_NegativeValuesTakeMaestroDefaults(t *testing.T) {
	r := runnerConfig(&config.MaestroConfig{Enabled: true, MaxConcurrent: -3, RateLimitRequests: 0, RateLimitPeriod: -1})
	c := mconfig.New(mconfig.WithBaseDir(t.TempDir()), mconfig.WithRunner(r))
	if err := c.Prepare(); err != nil {
		t.Fatal(err)
	}
	got := c.Runner()
	if got.MaxConcurrent != 5 || got.RateLimit.MaxRequests != 10 || got.RateLimit.PeriodSeconds != 60 {
		t.Errorf("runner after defaults = %+v, want 5 / 10 per 60", got)
	}
}

// ---- registration negatives -------------------------------------------------

func registerFor(t *testing.T, cfg *config.Config, agentID, workspace string, spawn any) []global.ToolDefinition {
	t.Helper()
	return GlobalProvider.RegisterTools(global.Deps{
		Cfg: cfg, AgentID: agentID, Host: tools.ToolDeps{Workspace: workspace}, Spawn: spawn,
	})
}

func TestRegisterTools_Negatives(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{List: []config.AgentConfig{
		{ID: "on", Maestro: &config.MaestroConfig{Enabled: true}},
		{ID: "off", Maestro: &config.MaestroConfig{Enabled: false}},
		{ID: "none"},
	}}}
	runner := &scriptedRunner{}
	if defs := registerFor(t, cfg, "on", "", runner); defs != nil {
		t.Errorf("empty workspace: got %d tools, want none", len(defs))
	}
	if defs := registerFor(t, cfg, "off", t.TempDir(), runner); defs != nil {
		t.Errorf("disabled block: got %d tools, want none", len(defs))
	}
	if defs := registerFor(t, cfg, "none", t.TempDir(), runner); defs != nil {
		t.Errorf("no block: got %d tools, want none", len(defs))
	}
	if defs := registerFor(t, cfg, "stranger", t.TempDir(), runner); defs != nil {
		t.Errorf("unknown agent: got %d tools, want none", len(defs))
	}
	if defs := registerFor(t, cfg, "on", t.TempDir(), runner); len(defs) == 0 {
		t.Error("enabled agent with workspace and runner must get the suite")
	}
}

// TestRegisterTools_LegacyBooleanConfigGetsNoTools: a config file still using
// "maestro": true loads, but that agent gets no Maestro tools.
func TestRegisterTools_LegacyBooleanConfigGetsNoTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"agents":{"list":[{"id":"legacy","maestro":true}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if defs := registerFor(t, cfg, "legacy", t.TempDir(), &scriptedRunner{}); defs != nil {
		t.Errorf("legacy boolean: got %d tools, want none", len(defs))
	}
}

// ---- log forwarding through the real logger ---------------------------------

func TestLogWriter_RealLoggerCarriesComponentAndAgent(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	w := &logWriter{agent: "alice"}
	if _, err := w.Write([]byte("2026-09-19 10:00:00 [WARN] [1] Task 3: retrying after error\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"maestro", "alice", "Task 3: retrying after error"} {
		if !strings.Contains(out, want) {
			t.Errorf("central log lacks %q:\n%s", want, out)
		}
	}
}

// TestLogWriter_LevelFromPrefixOnly: a level token inside the message body
// must not change the level; only the line prefix decides it.
func TestLogWriter_LevelFromPrefixOnly(t *testing.T) {
	var got []string
	w := &logWriter{agent: "a", emit: func(level, msg string, _ map[string]any) { got = append(got, level+"|"+msg) }}
	if _, err := w.Write([]byte("2026-09-19 10:00:00 [INFO] [1] model said [ERROR] in its reply\n[FATAL] bare line\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(got) != 2 || got[0] != "INFO|model said [ERROR] in its reply" || got[1] != "INFO|[FATAL] bare line" {
		t.Errorf("forwarded = %v", got)
	}
}

// ---- dispatcher metadata stubs (documented behaviour) -----------------------

func TestDispatcher_MetadataStubs(t *testing.T) {
	d := &dispatcher{}
	if l := d.GetLLM(""); l == nil || l.ID != hostProviderModel || l.RecoveryConfig != nil {
		t.Errorf("GetLLM(\"\") = %+v, want synthetic %q with no recovery config", l, hostProviderModel)
	}
	if l := d.GetLLM("Pro"); l == nil || l.ID != "Pro" {
		t.Errorf("GetLLM(Pro) = %+v", l)
	}
	if ok, err := d.TestLLM("anything"); !ok || err != nil {
		t.Errorf("TestLLM = %v, %v; want always available (host fallback chain decides)", ok, err)
	}
	if d.GetExecInfo("x") == nil {
		t.Error("GetExecInfo must not be nil")
	}
}
