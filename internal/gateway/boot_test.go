package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/internal/perms"
)

// TestEnforceDataDirPerms_SetsDataDirMode: the data directory itself is set
// to exactly the root folder mode, here narrowing a 0755 one to 0700.
func TestEnforceDataDirPerms_SetsDataDirMode(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	if err := enforceDataDirPerms(home, perms.PolicyFor(cfg)); err != nil {
		t.Fatalf("enforceDataDirPerms: %v", err)
	}
	fi, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Fatalf("CLAW_HOME mode = %04o, want 0700", got)
	}
}

// writeBootConfig writes config.json with mode (umask-proof) and returns its path.
func writeBootConfig(t *testing.T, home, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestOpenBootConfig_LooseConfigRefused: a config granting bits the root file
// mode does not is refused with the chmod to the configured mode, and the
// file is not rewritten (the missing permission keys are not added).
func TestOpenBootConfig_LooseConfigRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	body := `{"security":{"permissions":{"root":{"dir_mode":"0700","file_mode":"0640"}}}}`
	configPath := writeBootConfig(t, home, body, 0o644)
	store, _, _, err := openBootConfig(home, configPath)
	if err == nil {
		t.Fatalf("startup proceeded with a world-readable config.json (%s)", store.Path())
	}
	for _, want := range []string{"startup aborted", "chmod 640 " + configPath} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if got, rerr := os.ReadFile(configPath); rerr != nil || string(got) != body {
		t.Errorf("config.json = %q (%v), want it untouched", got, rerr)
	}
}

// TestOpenBootConfig_TighterConfigWidened: a config tighter than the root
// file mode is set to exactly that mode.
func TestOpenBootConfig_TighterConfigWidened(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	configPath := writeBootConfig(t, home,
		`{"security":{"permissions":{"root":{"dir_mode":"0750","file_mode":"0640"}}}}`, 0o600)
	if _, _, _, err := openBootConfig(home, configPath); err != nil {
		t.Fatalf("openBootConfig: %v", err)
	}
	for path, want := range map[string]os.FileMode{configPath: 0o640, home: 0o750} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", path, got, want)
		}
	}
}

// TestOpenBootConfig_WritesPermissionDefaults: a config without the full
// security.permissions block gets every key written at start, with the old
// top-level pair ignored and left out of the file, and a second start writes
// nothing.
func TestOpenBootConfig_WritesPermissionDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	configPath := writeBootConfig(t, home,
		`{"default_config":true,"security":{"permissions":{"dir_mode":"0750","logs":{"file_mode":"0640"}}}}`, 0o600)
	if _, _, _, err := openBootConfig(home, configPath); err != nil {
		t.Fatalf("openBootConfig: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		DefaultConfig bool `json:"default_config"`
		Security      struct {
			Permissions map[string]any `json:"permissions"`
		} `json:"security"`
	}
	if err = json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.DefaultConfig {
		t.Error("default_config cleared by the defaults write; the setup wizard would not open")
	}
	p := doc.Security.Permissions
	for _, gone := range []string{"dir_mode", "file_mode"} {
		if _, ok := p[gone]; ok {
			t.Errorf("old top-level %s still in the file", gone)
		}
	}
	want := map[string][2]string{
		"root": {"0700", "0600"}, "secrets": {"0700", "0600"}, "bin": {"0700", "0700"},
		"default": {"0700", "0600"}, "logs": {"0700", "0640"}, "agents": {"0700", "0600"},
		"media": {"0700", "0600"}, "common": {"0700", "0600"}, "backup": {"0700", "0600"},
	}
	for area, modes := range want {
		got, ok := p[area].(map[string]any)
		if !ok || got["dir_mode"] != modes[0] || got["file_mode"] != modes[1] {
			t.Errorf("%s = %v, want dir_mode %s file_mode %s", area, got, modes[0], modes[1])
		}
	}

	before, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	store, _, _, err := openBootConfig(home, configPath)
	if err != nil {
		t.Fatalf("second openBootConfig: %v", err)
	}
	if added, dropped := store.Current().PermissionsAdded(); len(added)+len(dropped) != 0 {
		t.Errorf("second start found keys to add or drop: %v %v", added, dropped)
	}
	after, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("second start rewrote config.json")
	}
}

// TestOpenBootConfig_EnforcesConfiguredModes: the data directory is held to
// the modes the config sets, read before the walk: a 0755 agent folder is
// tightened to the agents area's 0750, not to 0700.
func TestOpenBootConfig_EnforcesConfiguredModes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	alice := filepath.Join(home, "agents", "alice")
	if err := os.MkdirAll(alice, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(home, "agents"), alice} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configPath := writeBootConfig(t, home,
		`{"security":{"permissions":{"agents":{"dir_mode":"0750","file_mode":"0640"}}}}`, 0o600)
	if _, _, _, err := openBootConfig(home, configPath); err != nil {
		t.Fatalf("openBootConfig: %v", err)
	}
	for path, want := range map[string]os.FileMode{home: 0o700, filepath.Join(home, "agents"): 0o750, alice: 0o750} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", path, got, want)
		}
	}
}

// TestOpenBootConfig_InvalidModeStops: an invalid mode stops the start with
// the key named, before anything is changed.
func TestOpenBootConfig_InvalidModeStops(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := writeBootConfig(t, home, `{"security":{"permissions":{"logs":{"file_mode":"4640"}}}}`, 0o600)
	store, _, _, err := openBootConfig(home, configPath)
	if err == nil {
		t.Fatalf("startup proceeded with an invalid mode (%s)", store.Path())
	}
	if !strings.Contains(err.Error(), "security.permissions.logs.file_mode") {
		t.Fatalf("openBootConfig = %v, want a refusal naming security.permissions.logs.file_mode", err)
	}
	fi, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Errorf("CLAW_HOME mode = %04o after a refused start, want 0755 untouched", got)
	}
}

// TestOpenAuditLog_CreatesDatabase: boot opens <CLAW_HOME>/internal/audit.db, private,
// and shutdown closes it.
func TestOpenAuditLog_CreatesDatabase(t *testing.T) {
	home := t.TempDir()
	openAuditLog(home)
	t.Cleanup(func() {
		if err := audit.Close(); err != nil { // idempotent: nil after the Close below
			t.Errorf("audit.Close in cleanup: %v", err)
		}
	})

	fi, err := os.Stat(filepath.Join(home, "internal", audit.FileName))
	if err != nil {
		t.Fatalf("audit.db missing after boot: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("audit.db mode = %04o, want 0600", got)
	}
	if audit.Default() == nil {
		t.Fatal("no process-wide audit store after openAuditLog")
	}
	if err := audit.Close(); err != nil {
		t.Fatalf("audit.Close: %v", err)
	}
	if audit.Default() != nil {
		t.Fatal("audit store still installed after Close")
	}
}

// TestAcquireLock_PrivateModes: the lock file and a freshly created CLAW_HOME
// are owner-only.
func TestAcquireLock_PrivateModes(t *testing.T) {
	home := filepath.Join(t.TempDir(), "claw-home")
	f, err := acquireLock(home)
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	t.Cleanup(func() { releaseLock(f) })
	if want := filepath.Join(home, "internal", lockFileName); f.Name() != want {
		t.Errorf("lock file = %s, want %s", f.Name(), want)
	}
	for path, want := range map[string]os.FileMode{home: 0o700, filepath.Join(home, "internal"): 0o700, f.Name(): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", path, got, want)
		}
	}
}
