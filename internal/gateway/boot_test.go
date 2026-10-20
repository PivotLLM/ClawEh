package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/internal/perms"
)

// TestEnforceDataDirPerms_LooseConfigAbortsStartup: a CLAW_HOME whose
// config.json other users can read refuses to start and names the chmod.
func TestEnforceDataDirPerms_LooseConfigAbortsStartup(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config.json")
	if err := os.WriteFile(configPath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configPath, 0o644); err != nil { // umask-proof
		t.Fatal(err)
	}

	err := enforceDataDirPerms(home, configPath, perms.Policy{})
	if err == nil {
		t.Fatal("startup proceeded with a world-readable config.json")
	}
	for _, want := range []string{"startup aborted", "chmod 600 " + configPath} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// TestEnforceDataDirPerms_PrivateConfigStarts: a private config passes, and
// the data directory itself is tightened to 0700 on the way.
func TestEnforceDataDirPerms_PrivateConfigStarts(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "config.json")
	if err := os.WriteFile(configPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := enforceDataDirPerms(home, configPath, perms.Policy{}); err != nil {
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

// TestOpenBootConfig_LooseConfigRefusedBeforeRead: a config other users can
// read is refused before it is parsed, so even an unreadable one gets the
// chmod, not a parse error.
func TestOpenBootConfig_LooseConfigRefusedBeforeRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	configPath := writeBootConfig(t, home, `{not json`, 0o644)
	store, _, _, err := openBootConfig(home, configPath)
	if err == nil {
		t.Fatalf("startup proceeded with a world-readable config.json (%s)", store.Path())
	}
	for _, want := range []string{"startup aborted", "chmod 600 " + configPath} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
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
	configPath := writeBootConfig(t, home, `{"security":{"permissions":{"logs":{"file_mode":"0666"}}}}`, 0o600)
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
