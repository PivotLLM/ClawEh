// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package layout

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// newConfig returns the default config with CLAW_HOME at a fresh temp dir.
func newConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "claw-home")
	t.Setenv("CLAW_HOME", home)
	return config.DefaultConfig(), home
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s still exists (err %v)", path, err)
	}
}

// oldLayout writes an install as the previous release left it.
func oldLayout(t *testing.T, home string) {
	t.Helper()
	write(t, filepath.Join(home, "state", "gateway.db"), "devices")
	write(t, filepath.Join(home, "state", "service-tokens.json"), "service")
	write(t, filepath.Join(home, "state", "message-api-tokens.json"), "named")
	write(t, filepath.Join(home, "state", "fusion-tokens.db"), "fusion")
	write(t, filepath.Join(home, "state", "acp-bridge", "identity.json"), "acp")
	write(t, filepath.Join(home, "agents", "default", "state", "state.json"), `{"last_channel":"telegram"}`)
	write(t, filepath.Join(home, "agents", "default", "skills", "weather", "SKILL.md"), "weather")
	write(t, filepath.Join(home, "agents", "default", "AGENTS.md"), "seeded")
	write(t, filepath.Join(home, "agents", "default", "sessions", "s.db"), "old")
	write(t, filepath.Join(home, "agents", "common", "notes.txt"), "shared")
}

func TestPrepareFreshStartCreatesDirectories(t *testing.T) {
	cfg, home := newConfig(t)

	Prepare(cfg)

	for _, dir := range []string{home, "internal", "cli", "skills", "common"} {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(home, dir)
		}
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !fi.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", dir, fi.Mode().Perm())
		}
	}
	assertGone(t, filepath.Join(home, "agents", "default"))
	assertGone(t, filepath.Join(home, "state"))
}

func TestPrepareMovesOldLayout(t *testing.T) {
	cfg, home := newConfig(t)
	oldLayout(t, home)

	Prepare(cfg)

	for rel, want := range map[string]string{
		"internal/gateway.db":               "devices",
		"internal/service-tokens.json":      "service",
		"internal/message-api-tokens.json":  "named",
		"internal/fusion-tokens.db":         "fusion",
		"internal/acp-bridge/identity.json": "acp",
		"internal/state.json":               `{"last_channel":"telegram"}`,
		"skills/weather/SKILL.md":           "weather",
		"common/notes.txt":                  "shared",
	} {
		if got := read(t, filepath.Join(home, rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	assertGone(t, filepath.Join(home, "state"))
	assertGone(t, filepath.Join(home, "agents", "default"))
	assertGone(t, filepath.Join(home, "agents", "common"))
}

// A second start finds everything in place and changes nothing.
func TestPrepareIsIdempotent(t *testing.T) {
	cfg, home := newConfig(t)
	oldLayout(t, home)

	Prepare(cfg)
	write(t, filepath.Join(home, "internal", "state.json"), "updated")
	Prepare(cfg)

	if got := read(t, filepath.Join(home, "internal", "state.json")); got != "updated" {
		t.Errorf("state.json = %q after a second start, want it untouched", got)
	}
	if got := read(t, filepath.Join(home, "skills", "weather", "SKILL.md")); got != "weather" {
		t.Errorf("skill = %q after a second start", got)
	}
	if got := read(t, filepath.Join(home, "common", "notes.txt")); got != "shared" {
		t.Errorf("common file = %q after a second start", got)
	}
	assertGone(t, filepath.Join(home, "agents", "default"))
}

// agents/default that is a configured agent's workspace is left alone.
func TestPrepareLeavesAConfiguredDefaultAgentAlone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent config.AgentConfig
	}{
		{"id default", config.AgentConfig{ID: "default"}},
		{"id main", config.AgentConfig{ID: "main"}},
		{"explicit workspace", config.AgentConfig{ID: "alice"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, home := newConfig(t)
			oldLayout(t, home)
			agent := tc.agent
			if agent.ID == "alice" {
				agent.Workspace = filepath.Join(home, "agents", "default")
			}
			cfg.Agents.List = []config.AgentConfig{agent}

			Prepare(cfg)

			for _, rel := range []string{
				"agents/default/state/state.json",
				"agents/default/skills/weather/SKILL.md",
				"agents/default/AGENTS.md",
				"agents/default/sessions/s.db",
			} {
				read(t, filepath.Join(home, rel))
			}
			// claw's state.json is copied out; the agent keeps its own copy.
			if got := read(t, filepath.Join(home, "internal", "state.json")); got != `{"last_channel":"telegram"}` {
				t.Errorf("internal/state.json = %q, want the copied state", got)
			}
			assertGone(t, filepath.Join(home, "skills", "weather"))
			// The rest of the move still happens.
			if got := read(t, filepath.Join(home, "internal", "gateway.db")); got != "devices" {
				t.Errorf("gateway.db = %q", got)
			}
			if got := read(t, filepath.Join(home, "common", "notes.txt")); got != "shared" {
				t.Errorf("common file = %q", got)
			}
		})
	}
}

// Files the new layout already has win; the old ones stay where they are.
func TestPrepareKeepsExistingFiles(t *testing.T) {
	cfg, home := newConfig(t)
	oldLayout(t, home)
	write(t, filepath.Join(home, "internal", "gateway.db"), "current")
	write(t, filepath.Join(home, "skills", "weather", "SKILL.md"), "current skill")
	write(t, filepath.Join(home, "agents", "default", "skills", "news", "SKILL.md"), "news")

	Prepare(cfg)

	if got := read(t, filepath.Join(home, "internal", "gateway.db")); got != "current" {
		t.Errorf("internal/gateway.db = %q, want the existing file", got)
	}
	if got := read(t, filepath.Join(home, "state", "gateway.db")); got != "devices" {
		t.Errorf("state/gateway.db = %q, want it left in place", got)
	}
	if got := read(t, filepath.Join(home, "internal", "service-tokens.json")); got != "service" {
		t.Errorf("service-tokens.json = %q, want it moved", got)
	}
	if got := read(t, filepath.Join(home, "skills", "weather", "SKILL.md")); got != "current skill" {
		t.Errorf("shared weather skill = %q, want the existing one", got)
	}
	if got := read(t, filepath.Join(home, "skills", "news", "SKILL.md")); got != "news" {
		t.Errorf("news skill = %q, want it moved", got)
	}
	// The colliding skill is moved under a new name and agents/default goes.
	if got := read(t, filepath.Join(home, "skills", "weather-default", "SKILL.md")); got != "weather" {
		t.Errorf("renamed skill = %q, want the agents/default copy", got)
	}
	assertGone(t, filepath.Join(home, "agents", "default"))
}

// A renamed skill skips names that are taken as well.
func TestPrepareRenamesCollidingSkills(t *testing.T) {
	cfg, home := newConfig(t)
	write(t, filepath.Join(home, "agents", "default", "skills", "weather", "SKILL.md"), "old")
	for _, name := range []string{"weather", "weather-default", "weather-default-2"} {
		write(t, filepath.Join(home, "skills", name, "SKILL.md"), name)
	}

	Prepare(cfg)

	if got := read(t, filepath.Join(home, "skills", "weather-default-3", "SKILL.md")); got != "old" {
		t.Errorf("weather-default-3 = %q, want old", got)
	}
	for _, name := range []string{"weather", "weather-default", "weather-default-2"} {
		if got := read(t, filepath.Join(home, "skills", name, "SKILL.md")); got != name {
			t.Errorf("%s = %q, want it untouched", name, got)
		}
	}
	assertGone(t, filepath.Join(home, "agents", "default"))
}

// A second start does not overwrite internal/state.json from a real
// agent's workspace.
func TestPrepareConfiguredAgentStateCopiedOnce(t *testing.T) {
	cfg, home := newConfig(t)
	cfg.Agents.List = []config.AgentConfig{{ID: "default"}}
	write(t, filepath.Join(home, "agents", "default", "state", "state.json"), "agent")

	Prepare(cfg)
	write(t, filepath.Join(home, "internal", "state.json"), "claw")
	Prepare(cfg)

	if got := read(t, filepath.Join(home, "internal", "state.json")); got != "claw" {
		t.Errorf("internal/state.json = %q, want claw's own", got)
	}
	if got := read(t, filepath.Join(home, "agents", "default", "state", "state.json")); got != "agent" {
		t.Errorf("agent state.json = %q, want it untouched", got)
	}
}

// With agents.common_dir set, the old default common directory is not moved.
func TestPrepareCommonDirOverride(t *testing.T) {
	cfg, home := newConfig(t)
	oldLayout(t, home)
	custom := filepath.Join(t.TempDir(), "shared")
	cfg.Agents.CommonDir = custom

	Prepare(cfg)

	if got := read(t, filepath.Join(home, "agents", "common", "notes.txt")); got != "shared" {
		t.Errorf("old common file = %q, want it left in place", got)
	}
	if fi, err := os.Stat(custom); err != nil || !fi.IsDir() {
		t.Errorf("configured common dir not created: %v", err)
	}
	assertGone(t, filepath.Join(home, "common"))
}

// A state.json from before the state/ subdirectory existed is moved too.
func TestPrepareMovesRootStateFile(t *testing.T) {
	cfg, home := newConfig(t)
	write(t, filepath.Join(home, "agents", "default", "state.json"), "root")

	Prepare(cfg)

	if got := read(t, filepath.Join(home, "internal", "state.json")); got != "root" {
		t.Errorf("state.json = %q, want root", got)
	}
	assertGone(t, filepath.Join(home, "agents", "default"))
}

// The cross-filesystem fallback copies a tree, a file and a symlink.
func TestCopyAny(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	write(t, filepath.Join(src, "a", "b.txt"), "b")
	if err := os.Symlink("a/b.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst")

	if err := copyAny(src, dst); err != nil {
		t.Fatalf("copyAny(dir): %v", err)
	}
	if err := copyAny(filepath.Join(src, "a", "b.txt"), filepath.Join(dst, "c.txt")); err != nil {
		t.Fatalf("copyAny(file): %v", err)
	}
	if got := read(t, filepath.Join(dst, "c.txt")); got != "b" {
		t.Errorf("copied single file = %q", got)
	}
	if err := copyAny(filepath.Join(src, "link"), filepath.Join(dst, "link2")); err != nil {
		t.Fatalf("copyAny(symlink): %v", err)
	}
	if link, err := os.Readlink(filepath.Join(dst, "link2")); err != nil || link != "a/b.txt" {
		t.Errorf("copied symlink = %q, %v", link, err)
	}
	if got := read(t, filepath.Join(dst, "a", "b.txt")); got != "b" {
		t.Errorf("copied file = %q", got)
	}
	if link, err := os.Readlink(filepath.Join(dst, "link")); err != nil || link != "a/b.txt" {
		t.Errorf("symlink = %q, %v", link, err)
	}
}
