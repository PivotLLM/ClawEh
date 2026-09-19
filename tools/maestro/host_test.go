// ClawEh
// License: MIT

package maestro

import (
	"os"
	"path/filepath"
	"testing"

	mconfig "github.com/PivotLLM/Maestro/config"

	"github.com/PivotLLM/ClawEh/config"
)

func TestRunnerConfig_Mapping(t *testing.T) {
	if got := runnerConfig(nil); got != (mconfig.Runner{}) {
		t.Errorf("nil block must map to zero runner (Maestro defaults), got %+v", got)
	}
	f := false
	got := runnerConfig(&config.MaestroConfig{MaxConcurrent: 2, RateLimitRequests: 4, RateLimitPeriod: 30, AllowParallel: &f})
	if got.MaxConcurrent != 2 || got.RateLimit.MaxRequests != 4 || got.RateLimit.PeriodSeconds != 30 || got.ParallelAllowed() {
		t.Errorf("runner = %+v", got)
	}
}

func TestReferenceDirsFromMounts(t *testing.T) {
	ws := t.TempDir()
	docs := t.TempDir()
	a := &config.AgentConfig{ID: "a", Maestro: &config.MaestroConfig{Enabled: true}, Mounts: []config.MountConfig{
		{Name: "docs", Path: docs, Writable: true},
		{Name: "gone", Path: filepath.Join(t.TempDir(), "missing")},
	}}
	dirs := referenceDirsFromMounts(a, ws)
	if len(dirs) != 1 || dirs[0].Mount != "docs" || dirs[0].Path != docs {
		t.Fatalf("dirs = %+v, want only docs→%s (missing dir skipped, auto maestro mount skipped)", dirs, docs)
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Error("a missing mount directory must not be created")
	}
	if referenceDirsFromMounts(nil, ws) != nil {
		t.Error("nil agent must map to no dirs")
	}
}

func TestImportAllowed_RestrictedAgent(t *testing.T) {
	ws := t.TempDir()
	mount := t.TempDir()
	allowed := t.TempDir()
	cfg := &config.Config{}
	cfg.Agents.Defaults.RestrictToWorkspace = true
	cfg.Tools.AllowReadPaths = []string{"^" + allowed + "/"}
	a := &config.AgentConfig{ID: "a", Maestro: &config.MaestroConfig{Enabled: true}, Mounts: []config.MountConfig{{Name: "m", Path: mount}}}
	ok := importAllowed(cfg, a, ws)

	for _, p := range []string{
		filepath.Join(ws, "files", "x.md"),
		filepath.Join(ws, "maestro", "projects", "p", "files", "y.md"),
		filepath.Join(mount, "doc.pdf"),
		filepath.Join(mount, "sub", "deep.txt"),
		filepath.Join(allowed, "ref.md"),
	} {
		if !ok(p) {
			t.Errorf("%s should be importable", p)
		}
	}
	for _, p := range []string{
		"/etc/passwd",
		filepath.Join(t.TempDir(), "secret.txt"),
		ws + "-sibling/x",
		mount + "2/x",
	} {
		if ok(p) {
			t.Errorf("%s must be refused", p)
		}
	}
}

func TestImportAllowed_UnrestrictedAgentReadsAnywhere(t *testing.T) {
	cfg := &config.Config{} // RestrictToWorkspace false: the agent's file tools read host paths
	ok := importAllowed(cfg, &config.AgentConfig{ID: "a"}, t.TempDir())
	if !ok("/etc/hostname") {
		t.Error("an unrestricted agent may import anywhere, matching its file tools")
	}
}

func TestImportAllowed_ResolvesSymlinkedRoots(t *testing.T) {
	realWS := t.TempDir()
	link := filepath.Join(t.TempDir(), "ws-link")
	if err := os.Symlink(realWS, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := &config.Config{}
	cfg.Agents.Defaults.RestrictToWorkspace = true
	ok := importAllowed(cfg, &config.AgentConfig{ID: "a"}, link)
	// Maestro hands the predicate the resolved path; the root must resolve too.
	if !ok(filepath.Join(realWS, "files", "a.txt")) {
		t.Error("workspace given as a symlink must still permit its real path")
	}
}

func TestLogWriter_ForwardsLevelsAndBuffersPartialLines(t *testing.T) {
	var got []string
	w := &logWriter{agent: "alice", emit: func(level, msg string, fields map[string]any) {
		got = append(got, level+"|"+msg+"|"+fields["agent"].(string))
	}}
	_, _ = w.Write([]byte("2026-09-19 10:00:00 [INFO] [123] Task 1: started\n2026-09-19 10:00:01 [WARN] [123] retrying\n"))
	_, _ = w.Write([]byte("2026-09-19 10:00:02 [ERROR] [123] fai"))
	_, _ = w.Write([]byte("led hard\n"))
	_, _ = w.Write([]byte("2026-09-19 10:00:03 [DEBUG] [123] noisy\nunformatted line\n"))
	want := []string{
		"INFO|Task 1: started|alice",
		"WARN|retrying|alice",
		"ERROR|failed hard|alice",
		"DEBUG|noisy|alice",
		"INFO|unformatted line|alice",
	}
	if len(got) != len(want) {
		t.Fatalf("forwarded %d lines, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}
