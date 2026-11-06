//go:build !windows

/******************************************************************************
 * Copyright (c) 2026 Tenebris Technologies Inc.                              *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

package perms

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// place creates rel under dir with mode m (a directory when rel ends in "/"),
// pinning the mode against the umask. Missing parents are created 0700.
func place(t *testing.T, dir, rel string, m fs.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(rel, "/")))
	if strings.HasSuffix(rel, "/") {
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

func modeOf(t *testing.T, p string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// recorder collects what Enforce logs, one line per call with its path.
type recorder struct{ info, warn []string }

func (r *recorder) logger() Logger {
	line := func(msg string, f map[string]any) string {
		if p, ok := f["path"].(string); ok {
			return msg + " " + p
		}
		return msg
	}
	return Logger{
		Info: func(msg string, f map[string]any) { r.info = append(r.info, line(msg, f)) },
		Warn: func(msg string, f map[string]any) { r.warn = append(r.warn, line(msg, f)) },
	}
}

// noExecutable makes the running executable unfindable, so a test's tree is
// walked without it.
func noExecutable(t *testing.T) {
	t.Helper()
	old := executable
	executable = func() (string, error) { return "", errors.New("none") }
	t.Cleanup(func() { executable = old })
}

// operatorConfig is a config for the data directory home whose areas all
// differ, so each path's expected mode shows which area governed it: a group
// may read the root, logs, agents and common areas, the secrets stay
// owner-only, bin is the default 0700/0700 and media grants nothing to the
// group.
func operatorConfig(t *testing.T, home string) *config.Config {
	t.Helper()
	t.Setenv("CLAW_HOME", home)
	cfg := config.DefaultConfig()
	cfg.Security.Permissions = config.PermissionsConfig{
		Root:    config.AreaPermissions{DirMode: "0750", FileMode: "0640"},
		Secrets: config.AreaPermissions{DirMode: "0700", FileMode: "0600"},
		Default: config.AreaPermissions{DirMode: "0710", FileMode: "0604"},
		Logs:    config.AreaPermissions{DirMode: "0750", FileMode: "0640"},
		Agents:  config.AreaPermissions{DirMode: "0770", FileMode: "0660"},
		Media:   config.AreaPermissions{DirMode: "0700", FileMode: "0600"},
		Common:  config.AreaPermissions{DirMode: "0775", FileMode: "0664"},
		Backup:  config.AreaPermissions{DirMode: "0700", FileMode: "0640"},
	}
	return cfg
}

// tree is the fixture: each path, its mode before Enforce, and the mode
// Enforce must leave it with under operatorConfig.
var tree = []struct {
	rel        string
	before     fs.FileMode
	want       fs.FileMode
	whyChanged string
}{
	{"config.json", 0o600, 0o640, "root file widened"},
	{"credentials.json", 0o644, 0o600, "a secret directly in the data dir"},
	{"notes.txt", 0o755, 0o640, "root file loses its exec bit"},
	{"internal/", 0o755, 0o700, "secrets folder"},
	{"internal/gateway.db", 0o644, 0o600, "everything in internal/ is a secret"},
	{"internal/nested/", 0o755, 0o700, "whole subtree"},
	{"internal/nested/state.txt", 0o644, 0o600, "whole subtree"},
	{"tls/", 0o755, 0o700, "secrets folder"},
	{"tls/server.crt", 0o644, 0o600, "every file in tls/"},
	{"tokens/", 0o711, 0o700, "secrets folder"},
	{"tokens/webui", 0o400, 0o600, "secret widened to the secrets mode"},
	{"bin/", 0o755, 0o700, "bin folder"},
	{"bin/backup.sh", 0o755, 0o700, "bin keeps exec, loses group/other"},
	{"bin/get-token", 0o600, 0o700, "bin wins over a secret name"},
	{"logs/", 0o700, 0o750, "logs folder widened (logs are walked)"},
	{"logs/claw.log", 0o600, 0o640, "log widened"},
	{"logs/dumps/", 0o755, 0o750, "logs subtree"},
	{"logs/token.log", 0o644, 0o640, "a secret name does not override logs"},
	{"media/", 0o755, 0o700, "media folder tightened (media is walked)"},
	{"media/a.png", 0o644, 0o600, "media file"},
	{"media/secret-plan.db", 0o600, 0o600, "a secret or database name does not override media"},
	{"agents/", 0o700, 0o770, "agents folder"},
	{"agents/alice/sessions/archive.db", 0o600, 0o660, "agent database follows agents"},
	{"agents/alice/sessions/archive.db-wal", 0o644, 0o660, "side file follows agents"},
	{"agents/alice/notes.md", 0o700, 0o660, "agent file loses exec"},
	{"agents/alice/API_TOKEN", 0o644, 0o600, "secret name inside agents"},
	{"agents/alice/fusion-tokens.db", 0o660, 0o600, "secret-named database"},
	{"common/", 0o700, 0o775, "common folder"},
	{"common/shared.txt", 0o600, 0o664, "common file"},
	{"backup/", 0o755, 0o700, "backup folder"},
	{"backup/claw-backup.tar.gz", 0o600, 0o640, "backup file"},
	{"skills/", 0o755, 0o710, "default folder"},
	{"skills/demo/run.sh", 0o755, 0o604, "default file loses exec"},
	{"cards/x.db", 0o644, 0o600, "a database outside the agents is a secret"},
}

// TestEnforceExactModes: every folder and file is set to exactly its area's
// mode, widened or tightened, and files outside bin/ lose execute bits.
func TestEnforceExactModes(t *testing.T) {
	noExecutable(t)
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, e := range tree {
		place(t, home, e.rel, e.before)
	}
	// A symlink in a walked tree, to a file outside the data dir: neither it
	// nor its target is changed.
	outside := place(t, t.TempDir(), "target.txt", 0o644)
	link := filepath.Join(home, "logs", "link.log")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	cfg := operatorConfig(t, home)

	var rec recorder
	if err := Enforce(home, PolicyFor(cfg), rec.logger()); err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got := modeOf(t, home); got != 0o750 {
		t.Errorf("data dir = %04o, want root dir_mode 0750", got)
	}
	for _, e := range tree {
		if got := modeOf(t, filepath.Join(home, filepath.FromSlash(strings.TrimSuffix(e.rel, "/")))); got != e.want {
			t.Errorf("%s = %04o, want %04o (%s)", e.rel, got, e.want, e.whyChanged)
		}
	}
	if got := modeOf(t, outside); got != 0o644 {
		t.Errorf("symlink target = %04o, want 0644 untouched", got)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink replaced or removed: %v", err)
	}
	joined := strings.Join(rec.info, "\n")
	for _, want := range []string{"set file permissions " + filepath.Join(home, "logs", "claw.log"), "set file permissions " + home} {
		if !strings.Contains(joined, want) {
			t.Errorf("INFO log lacks %q:\n%s", want, joined)
		}
	}
	if len(rec.warn) != 0 {
		t.Errorf("unexpected WARN: %v", rec.warn)
	}

	// A second run finds nothing to change.
	rec = recorder{}
	if err := Enforce(home, PolicyFor(cfg), rec.logger()); err != nil {
		t.Fatalf("second Enforce: %v", err)
	}
	if len(rec.info) != 0 {
		t.Errorf("second Enforce changed: %v", rec.info)
	}
}

// TestEnforceLeavesRunningExecutable: the running binary keeps its mode
// wherever it lies; outside bin/ a WARN names it, inside bin/ nothing is
// said.
func TestEnforceLeavesRunningExecutable(t *testing.T) {
	for _, c := range []struct {
		rel      string
		wantWarn bool
	}{
		{"claw", true},
		{"bin/claw", false},
	} {
		t.Run(c.rel, func(t *testing.T) {
			home := t.TempDir()
			exe := place(t, home, c.rel, 0o755)
			old := executable
			executable = func() (string, error) { return exe, nil }
			t.Cleanup(func() { executable = old })

			var rec recorder
			if err := Enforce(home, PolicyFor(operatorConfig(t, home)), rec.logger()); err != nil {
				t.Fatalf("Enforce: %v", err)
			}
			if got := modeOf(t, exe); got != 0o755 {
				t.Errorf("executable = %04o, want 0755 untouched", got)
			}
			warned := strings.Contains(strings.Join(rec.warn, "\n"), "running executable")
			if warned != c.wantWarn {
				t.Errorf("warned = %v, want %v (%v)", warned, c.wantWarn, rec.warn)
			}
			findings, err := Check(home, "", PolicyFor(operatorConfig(t, home)))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range findings {
				if f.Path == exe {
					t.Errorf("Check reported the running executable: %+v", f)
				}
			}
		})
	}
}

// TestEnforceCreatesDataDir: a missing data directory is created with exactly
// the root folder mode, whatever the umask.
func TestEnforceCreatesDataDir(t *testing.T) {
	noExecutable(t)
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	home := filepath.Join(t.TempDir(), "claw")
	cfg := operatorConfig(t, home)
	if err := Enforce(home, PolicyFor(cfg), Logger{}); err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got := modeOf(t, home); got != 0o750 {
		t.Errorf("created data dir = %04o, want 0750", got)
	}
}

// TestCheckReportsWithoutChanging: Check lists every path more open than
// its configured mode, including a config file outside the data dir, leaves
// out paths tighter than configured, and changes nothing.
func TestCheckReportsWithoutChanging(t *testing.T) {
	noExecutable(t)
	home := t.TempDir()
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	place(t, home, "logs/claw.log", 0o600)     // tighter than 0640: not reported
	place(t, home, "internal/audit.db", 0o644) // looser than 0600: reported
	place(t, home, "logs/", 0o750)
	place(t, home, "internal/", 0o700)
	elsewhere := place(t, t.TempDir(), "config.json", 0o644) // looser than 0640
	cfg := operatorConfig(t, home)

	findings, err := Check(home, elsewhere, PolicyFor(cfg))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	got := map[string]Finding{}
	for _, f := range findings {
		got[f.Path] = f
	}
	for path, want := range map[string]Finding{
		home:      {Mode: 0o755, Want: 0o750},
		elsewhere: {Mode: 0o644, Want: 0o640},
		filepath.Join(home, "internal", "audit.db"): {Mode: 0o644, Want: 0o600},
	} {
		f, ok := got[path]
		if !ok || f.Mode != want.Mode || f.Want != want.Want {
			t.Errorf("finding for %s = %+v (present %v), want %04o→%04o", path, f, ok, want.Mode, want.Want)
		}
	}
	if len(findings) != 3 {
		t.Errorf("got %d findings, want 3: %+v", len(findings), findings)
	}
	if modeOf(t, home) != 0o755 || modeOf(t, filepath.Join(home, "internal", "audit.db")) != 0o644 {
		t.Error("Check changed a mode")
	}
}

// TestChmodSafeRefusesSwappedEntry: an entry replaced between the walk and
// the change, by a symlink or by another file, is not changed, and neither is
// what replaced it.
func TestChmodSafeRefusesSwappedEntry(t *testing.T) {
	dir := t.TempDir()
	outside := place(t, t.TempDir(), "victim", 0o600)

	p := place(t, dir, "a.txt", 0o600)
	walked, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, p); err != nil {
		t.Fatal(err)
	}
	if err = chmodSafe(p, 0o666, walked); err == nil {
		t.Error("chmod through a swapped-in symlink was accepted")
	}
	if got := modeOf(t, outside); got != 0o600 {
		t.Errorf("file outside the data dir changed to %04o", got)
	}

	q := place(t, dir, "b.txt", 0o600)
	walked, err = os.Lstat(q)
	if err != nil {
		t.Fatal(err)
	}
	other := place(t, dir, "other.txt", 0o600)
	if err = os.Rename(other, q); err != nil {
		t.Fatal(err)
	}
	if err = chmodSafe(q, 0o666, walked); !errors.Is(err, errReplaced) {
		t.Errorf("chmod of a replaced file = %v, want errReplaced", err)
	}
	if got := modeOf(t, q); got != 0o600 {
		t.Errorf("replacement changed to %04o", got)
	}

	d := place(t, dir, "sub/", 0o700)
	walked, err = os.Lstat(d)
	if err != nil {
		t.Fatal(err)
	}
	if err = chmodSafe(d, 0o750, walked); err != nil {
		t.Fatalf("chmod of an unchanged directory: %v", err)
	}
	if got := modeOf(t, d); got != 0o750 {
		t.Errorf("directory = %04o, want 0750", got)
	}
}

// TestEnforceSkipsHardLinks: a file with a second hard link (which may lie
// outside the data dir) keeps its mode, and a WARN names it.
func TestEnforceSkipsHardLinks(t *testing.T) {
	noExecutable(t)
	home := t.TempDir()
	if err := os.Chmod(home, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := place(t, t.TempDir(), "shadow", 0o644)
	linked := filepath.Join(home, "logs", "linked.log")
	place(t, home, "logs/", 0o750)
	if err := os.Link(outside, linked); err != nil {
		t.Skipf("hard links unavailable here: %v", err)
	}
	var rec recorder
	if err := Enforce(home, PolicyFor(operatorConfig(t, home)), rec.logger()); err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got := modeOf(t, outside); got != 0o644 {
		t.Errorf("hard-linked file = %04o, want 0644 untouched", got)
	}
	if !strings.Contains(strings.Join(rec.warn, "\n"), "hard link; its mode is left alone "+linked) {
		t.Errorf("no WARN naming %s: %v", linked, rec.warn)
	}
}

// TestEnforceSecretsBeforeCap: with the entry cap used up by a large agents
// tree, the secrets folders and the files directly in the data dir are still
// set.
func TestEnforceSecretsBeforeCap(t *testing.T) {
	noExecutable(t)
	home := t.TempDir()
	for i := range 20 {
		place(t, home, filepath.Join("agents", "alice", strings.Repeat("x", i+1)), 0o644)
	}
	secret := place(t, home, "tokens/webui", 0o644)
	key := place(t, home, "tls/server.key", 0o644)
	creds := place(t, home, "credentials.json", 0o644)
	old := walkLimit
	walkLimit = 5
	t.Cleanup(func() { walkLimit = old })
	if err := Enforce(home, PolicyFor(operatorConfig(t, home)), Logger{}); err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	for _, p := range []string{secret, key, creds} {
		if got := modeOf(t, p); got != 0o600 {
			t.Errorf("%s = %04o, want 0600 despite the cap", p, got)
		}
	}
}

// TestEnforceSymlinkedDataDir: a data directory that is a symbolic link is
// walked at its target, with the areas matched by the configured spelling.
func TestEnforceSymlinkedDataDir(t *testing.T) {
	noExecutable(t)
	target := t.TempDir()
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "claw")
	if err := os.Symlink(target, home); err != nil {
		t.Fatal(err)
	}
	logFile := place(t, target, "logs/claw.log", 0o600)
	secret := place(t, target, "internal/gateway.db", 0o644)
	if err := Enforce(home, PolicyFor(operatorConfig(t, home)), Logger{}); err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	for p, want := range map[string]fs.FileMode{target: 0o750, logFile: 0o640, secret: 0o600} {
		if got := modeOf(t, p); got != want {
			t.Errorf("%s = %04o, want %04o", p, got, want)
		}
	}
	if fi, err := os.Lstat(home); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the data dir link itself was replaced")
	}
}

func TestWalkCap(t *testing.T) {
	noExecutable(t)
	home := t.TempDir()
	for _, e := range tree {
		place(t, home, e.rel, e.before)
	}
	old := walkLimit
	walkLimit = 3
	t.Cleanup(func() { walkLimit = old })
	cfg := operatorConfig(t, home)

	findings, err := Check(home, "", PolicyFor(cfg))
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("Check err = %v, want ErrTruncated", err)
	}
	walkLimit = old
	all, err := Check(home, "", PolicyFor(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) >= len(all) {
		t.Errorf("got %d findings under a cap of 3, the same as without it (%d)", len(findings), len(all))
	}
	walkLimit = 3
	var rec recorder
	if err := Enforce(home, PolicyFor(cfg), rec.logger()); err != nil {
		t.Fatalf("Enforce under cap: %v", err)
	}
	if !strings.Contains(strings.Join(rec.warn, "\n"), "entry cap") {
		t.Errorf("Enforce did not log the truncation: %v", rec.warn)
	}
}

// TestCheckConfig: a config granting a bit the root file mode does not is
// refused with the chmod to that mode; a tighter one is set to it; a symlink
// is never changed; a missing one is fine.
func TestCheckConfig(t *testing.T) {
	dir := t.TempDir()

	loose := place(t, dir, "loose.json", 0o644)
	err := CheckConfig(loose, 0o640, Logger{})
	if err == nil || !strings.Contains(err.Error(), "chmod 640 "+loose) {
		t.Errorf("loose: err = %v, want a refusal naming chmod 640", err)
	}
	if got := modeOf(t, loose); got != 0o644 {
		t.Errorf("loose config changed to %04o", got)
	}

	ownerExec := place(t, dir, "exec.json", 0o700)
	if err := CheckConfig(ownerExec, 0o600, Logger{}); err == nil {
		t.Error("a config with a bit beyond the mode (owner exec) was accepted")
	}

	tight := place(t, dir, "tight.json", 0o600)
	var rec recorder
	if err := CheckConfig(tight, 0o640, rec.logger()); err != nil {
		t.Fatalf("tight: %v", err)
	}
	if got := modeOf(t, tight); got != 0o640 {
		t.Errorf("tight config = %04o, want 0640", got)
	}
	if len(rec.info) != 1 {
		t.Errorf("tight config change not logged once: %v", rec.info)
	}

	link := filepath.Join(dir, "link.json")
	target := place(t, dir, "target.json", 0o600)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := CheckConfig(link, 0o640, Logger{}); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if got := modeOf(t, target); got != 0o600 {
		t.Errorf("config changed through a symlink to %04o", got)
	}

	if err := CheckConfig(filepath.Join(dir, "missing.json"), 0o600, Logger{}); err != nil {
		t.Errorf("missing config: %v", err)
	}
}

// TestEnsureFileMode: a new file is created with exactly the mode whatever
// the umask, an existing one is widened or narrowed to it, and a symlink is
// refused.
func TestEnsureFileMode(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	dir := t.TempDir()

	fresh := filepath.Join(dir, "fresh.db")
	if err := EnsureFileMode(fresh, 0o640); err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if got := modeOf(t, fresh); got != 0o640 {
		t.Errorf("fresh = %04o, want 0640", got)
	}
	for _, before := range []fs.FileMode{0o600, 0o666} {
		p := place(t, dir, "existing.db", before)
		if err := EnsureFileMode(p, 0o640); err != nil {
			t.Fatal(err)
		}
		if got := modeOf(t, p); got != 0o640 {
			t.Errorf("existing %04o = %04o, want 0640", before, got)
		}
	}
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(fresh, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsureFileMode(link, 0o600); err == nil {
		t.Error("a symlink was accepted")
	}
	if got := modeOf(t, fresh); got != 0o640 {
		t.Errorf("symlink target changed to %04o", got)
	}
}

// TestSecretModes: the process default starts at 0700/0600, follows
// SetSecretModes, and EnsureSecretFile uses it.
func TestSecretModes(t *testing.T) {
	t.Cleanup(func() { SetSecretModes(config.Modes{Dir: 0o700, File: 0o600}) })
	if got := SecretModes(); got != (config.Modes{Dir: 0o700, File: 0o600}) {
		t.Fatalf("default = %v, want 0700/0600", got)
	}
	SetSecretModes(config.Modes{Dir: 0o750, File: 0o640})
	p := filepath.Join(t.TempDir(), "store.db")
	if err := EnsureSecretFile(p); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, p); got != 0o640 {
		t.Errorf("secret file = %04o, want 0640", got)
	}
	SetSecretModes(config.Modes{})
	if got := SecretModes(); got != (config.Modes{Dir: 0o750, File: 0o640}) {
		t.Errorf("zero modes changed the default to %v", got)
	}
}

// TestPolicyFor: the policy carries the root, secrets and default modes and
// one area per subtree, the agents area with its databases, bin flagged.
func TestPolicyFor(t *testing.T) {
	home := t.TempDir()
	cfg := operatorConfig(t, home)
	ws := filepath.Join(t.TempDir(), "bob")
	cfg.Agents.List = []config.AgentConfig{{ID: "bob", Workspace: ws}}
	p := PolicyFor(cfg)
	if p.Root != (config.Modes{Dir: 0o750, File: 0o640}) || p.Secrets != (config.Modes{Dir: 0o700, File: 0o600}) ||
		p.Default != (config.Modes{Dir: 0o710, File: 0o604}) {
		t.Errorf("root/secrets/default = %v %v %v", p.Root, p.Secrets, p.Default)
	}
	byRoot := map[string]Area{}
	for _, a := range p.Areas {
		byRoot[a.Root] = a
	}
	for root, want := range map[string]Area{
		filepath.Join(home, "logs"):     {Modes: config.Modes{Dir: 0o750, File: 0o640}, Plain: true},
		filepath.Join(home, "media"):    {Modes: config.Modes{Dir: 0o700, File: 0o600}, Plain: true},
		filepath.Join(home, "common"):   {Modes: config.Modes{Dir: 0o775, File: 0o664}},
		filepath.Join(home, "backup"):   {Modes: config.Modes{Dir: 0o700, File: 0o640}},
		filepath.Join(home, "bin"):      {Modes: config.Modes{Dir: 0o700, File: 0o700}, Bin: true},
		filepath.Join(home, "agents"):   {Modes: config.Modes{Dir: 0o770, File: 0o660}, Databases: true},
		ws:                              {Modes: config.Modes{Dir: 0o770, File: 0o660}, Databases: true},
		filepath.Join(home, "internal"): {Modes: config.Modes{Dir: 0o700, File: 0o600}, Secret: true},
		filepath.Join(home, "tls"):      {Modes: config.Modes{Dir: 0o700, File: 0o600}, Secret: true},
		filepath.Join(home, "tokens"):   {Modes: config.Modes{Dir: 0o700, File: 0o600}, Secret: true},
	} {
		want.Root = root
		if got, ok := byRoot[root]; !ok || got != want {
			t.Errorf("area %s = %+v (present %v), want %+v", root, got, ok, want)
		}
	}
}
