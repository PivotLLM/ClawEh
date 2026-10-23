//go:build !windows

/******************************************************************************
 * Copyright (c) 2026 Tenebris Technologies Inc.                              *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

package perms

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// place creates rel under dir with mode (a directory when rel ends in "/"),
// pinning the mode against the umask. Parents are created 0700.
func place(t *testing.T, dir, rel string, m fs.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if rel[len(rel)-1] == '/' {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(p, m); err != nil {
		t.Fatal(err)
	}
	return p
}

// agentsPolicy is the operator's setup: everything owner-only except the
// agents area, which a group may read (0750/0640), with internal/ private.
func agentsPolicy(dir string) Policy {
	return Policy{
		DirMode: 0o700,
		Areas: []Area{
			{Root: filepath.Join(dir, "agents"), DirMode: 0o750, FileMode: 0o640, Databases: true},
			{Root: filepath.Join(dir, "internal"), DirMode: 0o700, FileMode: 0o600},
		},
	}
}

// TestEnforceAreaModes: within the agents area a directory is tightened to
// the area's mode and never loosened, databases follow the area's file mode,
// token files stay 0600, and databases elsewhere stay secrets.
func TestEnforceAreaModes(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := place(t, dir, "config.json", 0o600)
	cases := []struct {
		rel        string
		from, want fs.FileMode
	}{
		{"agents/", 0o750, 0o750},
		{"agents/alice/", 0o755, 0o750},                             // tightened to the area mode
		{"agents/bob/", 0o750, 0o750},                               // already within it
		{"agents/carol/", 0o700, 0o700},                             // never loosened
		{"agents/alice/sessions/", 0o777, 0o750},                    // group write and other cleared
		{"agents/alice/sessions/chat.archive.db", 0o644, 0o640},     // follows the area
		{"agents/alice/sessions/chat.archive.db-wal", 0o666, 0o640}, // side files too
		{"agents/bob/cogmem/cogmem.db", 0o640, 0o640},               // left alone
		{"agents/bob/cogmem/old.db", 0o600, 0o600},                  // never loosened
		{"agents/alice/state/message-tokens.json", 0o644, 0o600},    // a token stays a secret
		{"agents/alice/tokens.db", 0o644, 0o600},                    // a token-named database too
		{"agents/alice/notes.md", 0o644, 0o644},                     // not sensitive: untouched
		{"internal/", 0o750, 0o700},                                 // private whatever the default
		{"internal/gateway.db", 0o640, 0o600},                       // databases outside the area are secrets
		{"cli/", 0o755, 0o700},                                      // the default directory mode
	}
	for _, c := range cases {
		place(t, dir, c.rel, c.from)
	}
	if err := Enforce(dir, config, agentsPolicy(dir), nil); err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	for _, c := range cases {
		if got := mode(t, filepath.Join(dir, filepath.FromSlash(c.rel))); got != c.want {
			t.Errorf("%s: mode %04o, want %04o (from %04o)", c.rel, got, c.want, c.from)
		}
	}
	findings, err := Check(dir, config, agentsPolicy(dir))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("Check after Enforce found %+v", findings)
	}
}

// TestEnforceDataDirMode: the data directory follows the default directory
// mode: a looser one is tightened to it, a tighter one is left alone, and a
// missing one is created with it.
func TestEnforceDataDirMode(t *testing.T) {
	for _, c := range []struct{ from, want fs.FileMode }{{0o755, 0o750}, {0o700, 0o700}, {0o750, 0o750}} {
		dir := t.TempDir()
		if err := os.Chmod(dir, c.from); err != nil {
			t.Fatal(err)
		}
		if err := Enforce(dir, filepath.Join(dir, "config.json"), Policy{DirMode: 0o750}, nil); err != nil {
			t.Fatalf("Enforce: %v", err)
		}
		if got := mode(t, dir); got != c.want {
			t.Errorf("data dir from %04o: %04o, want %04o", c.from, got, c.want)
		}
	}
	fresh := filepath.Join(t.TempDir(), "fresh")
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	if err := Enforce(fresh, filepath.Join(fresh, "config.json"), Policy{DirMode: 0o750}, nil); err != nil {
		t.Fatalf("Enforce fresh: %v", err)
	}
	if got := mode(t, fresh); got != 0o750 {
		t.Errorf("created data dir mode = %04o, want 0750", got)
	}
}

// TestCheckConfig: a group-readable config is refused before anything is read,
// with the fix named; a private or missing one passes.
func TestCheckConfig(t *testing.T) {
	dir := t.TempDir()
	if err := CheckConfig(filepath.Join(dir, "missing.json")); err != nil {
		t.Errorf("missing config refused: %v", err)
	}
	cfg := place(t, dir, "config.json", 0o600)
	if err := CheckConfig(cfg); err != nil {
		t.Errorf("private config refused: %v", err)
	}
	if err := os.Chmod(cfg, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := CheckConfig(cfg); err == nil {
		t.Error("a group-readable config was accepted")
	}
}

// TestPolicyFor: the configured modes reach the policy, the agents area
// covers base_dir and an agent's own workspace, and internal/ stays private.
func TestPolicyFor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	cfg := config.DefaultConfig()
	ws := filepath.Join(t.TempDir(), "alice-ws")
	cfg.Agents.List = []config.AgentConfig{{ID: "alice", Workspace: ws}}
	cfg.Security.Permissions = config.PermissionsConfig{
		DirMode: "0750",
		Logs:    config.AreaPermissions{DirMode: "0750", FileMode: "0640"},
		Agents:  config.AreaPermissions{DirMode: "0750", FileMode: "0640"},
	}
	p := PolicyFor(cfg)
	if p.DirMode != 0o750 {
		t.Errorf("DirMode = %04o, want 0750", p.DirMode)
	}
	for _, c := range []struct {
		path      string
		dir, file fs.FileMode
		databases bool
	}{
		{filepath.Join(home, "agents", "bob", "sessions"), 0o750, 0o640, true},
		{filepath.Join(ws, "sessions"), 0o750, 0o640, true},
		{filepath.Join(home, "logs", "dumps"), 0o750, 0o640, false},
		{filepath.Join(home, "common"), 0o750, 0o600, false}, // falls back to the default pair
		{filepath.Join(home, "internal", "temp"), 0o700, 0o600, false},
	} {
		a := p.area(c.path)
		if a == nil {
			t.Errorf("%s: no area", c.path)
			continue
		}
		if a.DirMode != c.dir || a.FileMode != c.file || a.Databases != c.databases {
			t.Errorf("%s: area %04o/%04o databases=%v, want %04o/%04o databases=%v",
				c.path, a.DirMode, a.FileMode, a.Databases, c.dir, c.file, c.databases)
		}
	}
	if a := p.area(filepath.Join(home, "cli")); a != nil {
		t.Errorf("cli/ is governed by area %+v, want the default", *a)
	}
}

// TestEnsureFileMode: a new file gets the mode, an existing looser one is
// tightened to it, and a tighter one is never loosened.
func TestEnsureFileMode(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	dir := t.TempDir()
	fresh := filepath.Join(dir, "alerts.log")
	if err := EnsureFileMode(fresh, 0o640); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, fresh); got != 0o640 {
		t.Errorf("fresh mode = %04o, want 0640", got)
	}
	loose := place(t, dir, "loose.log", 0o666)
	if err := EnsureFileMode(loose, 0o640); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, loose); got != 0o640 {
		t.Errorf("loose mode = %04o, want 0640", got)
	}
	tight := place(t, dir, "tight.log", 0o600)
	if err := EnsureFileMode(tight, 0o640); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, tight); got != 0o600 {
		t.Errorf("tight mode = %04o, want 0600 left alone", got)
	}
}
