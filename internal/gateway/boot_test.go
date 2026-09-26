package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/internal/audit"
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

	err := enforceDataDirPerms(home, configPath)
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
	if err := enforceDataDirPerms(home, configPath); err != nil {
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

// TestOpenAuditLog_CreatesDatabase: boot opens <CLAW_HOME>/audit.db, private,
// and shutdown closes it.
func TestOpenAuditLog_CreatesDatabase(t *testing.T) {
	home := t.TempDir()
	openAuditLog(home)
	t.Cleanup(func() {
		if err := audit.Close(); err != nil { // idempotent: nil after the Close below
			t.Errorf("audit.Close in cleanup: %v", err)
		}
	})

	fi, err := os.Stat(filepath.Join(home, audit.FileName))
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
	for path, want := range map[string]os.FileMode{home: 0o700, f.Name(): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", path, got, want)
		}
	}
}
