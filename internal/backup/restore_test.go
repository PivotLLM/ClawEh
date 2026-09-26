package backup

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
)

// holdLock takes the gateway's lock in home the way the gateway does and
// releases it when the test ends (or when the returned func is called).
func holdLock(t *testing.T, home string) func() {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(home, LockFileName), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	var released bool
	release := func() {
		if released {
			return
		}
		released = true
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			t.Errorf("unlock: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Errorf("close lock file: %v", err)
		}
	}
	t.Cleanup(release)
	return release
}

func TestGatewayRunning(t *testing.T) {
	home := t.TempDir()
	if running, err := GatewayRunning(home); err != nil || running {
		t.Fatalf("no lock file: running=%v err=%v", running, err)
	}
	release := holdLock(t, home)
	if running, err := GatewayRunning(home); err != nil || !running {
		t.Fatalf("lock held: running=%v err=%v", running, err)
	}
	release()
	// A stale lock file (process gone, file left) is not "running".
	if running, err := GatewayRunning(home); err != nil || running {
		t.Fatalf("stale lock: running=%v err=%v", running, err)
	}
}

func TestRestoreRefusesWhileGatewayRuns(t *testing.T) {
	src := fixture(t)
	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	writeFileT(t, filepath.Join(target, "config.json"), `{"old":true}`, 0o600)
	holdLock(t, target)
	plan, err := PlanRestore(res.Archive, target)
	if err != nil {
		t.Fatalf("PlanRestore must work read-only even while locked: %v", err)
	}
	if _, err := plan.Apply(time.Now()); !errors.Is(err, ErrGatewayRunning) {
		t.Fatalf("Apply err = %v, want ErrGatewayRunning", err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "config.json")); err != nil || string(got) != `{"old":true}` {
		t.Fatalf("config.json changed while locked: %q (err %v)", got, err)
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	src := fixture(t)
	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// A second home with its own (different) files that the restore replaces,
	// including a live-looking WAL database with stale sidecars.
	target := t.TempDir()
	writeFileT(t, filepath.Join(target, "config.json"), `{"old":true}`, 0o644)
	writeFileT(t, filepath.Join(target, "state", "gateway.db"), "old db bytes", 0o600)
	writeFileT(t, filepath.Join(target, "state", "gateway.db-wal"), "stale wal", 0o600)
	writeFileT(t, filepath.Join(target, "state", "gateway.db-shm"), "stale shm", 0o600)
	writeFileT(t, filepath.Join(target, "state", "untouched.json"), `keep`, 0o600)

	plan, err := PlanRestore(res.Archive, target)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Home != target || plan.Manifest.Home != src.Home {
		t.Fatalf("plan = %+v", plan)
	}
	replaces := map[string]bool{}
	for _, e := range plan.Entries {
		replaces[e.Name] = e.Replaces
		if want := filepath.Join(target, filepath.FromSlash(e.Name)); e.Target != want {
			t.Errorf("target for %s = %s, want %s", e.Name, e.Target, want)
		}
	}
	if !replaces["config.json"] || !replaces["state/gateway.db"] || replaces["credentials.json"] {
		t.Fatalf("replaces = %v", replaces)
	}

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	parked, err := plan.Apply(now)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := filepath.Join(target, "restore-backup-20260701-120000"); parked != want {
		t.Fatalf("parked = %s, want %s", parked, want)
	}

	// Restored content and modes.
	if got, err := os.ReadFile(filepath.Join(target, "config.json")); err != nil || string(got) != `{"agents":{}}` {
		t.Errorf("config.json = %q (err %v)", got, err)
	}
	if fi, err := os.Stat(filepath.Join(target, "tls", "server.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("tls/server.key: %v mode=%v", err, fi)
	}
	if fi, err := os.Stat(filepath.Join(target, "cron", "jobs.json")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("cron/jobs.json: %v mode=%v", err, fi)
	}
	restoredDB := filepath.Join(target, "agents", "main", "sessions", "s1.archive.db")
	if err := QuickCheck(restoredDB); err != nil {
		t.Errorf("restored session DB: %v", err)
	}
	if n := rowCount(t, restoredDB); n != 50 {
		t.Errorf("restored rows = %d, want 50", n)
	}
	if n := rowCount(t, filepath.Join(target, "state", "gateway.db")); n != 5 {
		t.Errorf("restored gateway.db rows = %d, want 5", n)
	}
	// Stale sidecars are gone from the live location...
	for _, s := range []string{"gateway.db-wal", "gateway.db-shm"} {
		if _, err := os.Stat(filepath.Join(target, "state", s)); !os.IsNotExist(err) {
			t.Errorf("stale %s must be moved away (err=%v)", s, err)
		}
	}
	// ...and parked with the replaced files.
	for name, want := range map[string]string{
		"config.json":          `{"old":true}`,
		"state/gateway.db":     "old db bytes",
		"state/gateway.db-wal": "stale wal",
		"state/gateway.db-shm": "stale shm",
	} {
		got, err := os.ReadFile(filepath.Join(parked, filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Errorf("parked %s = %q err=%v, want %q", name, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(parked, "credentials.json")); !os.IsNotExist(err) {
		t.Errorf("a file that did not exist before must not be parked (err=%v)", err)
	}
	// Files the archive does not mention are left alone.
	if got, err := os.ReadFile(filepath.Join(target, "state", "untouched.json")); err != nil || string(got) != "keep" {
		t.Errorf("untouched.json = %q (err %v)", got, err)
	}
	if matches, err := filepath.Glob(filepath.Join(target, ".restore-staging-*")); err != nil || len(matches) != 0 {
		t.Errorf("staging left behind: %v (err %v)", matches, err)
	}
}

// corruptArchive writes a valid-looking backup whose only database is garbage.
func corruptArchive(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "bad.tar.gz")
	m := Manifest{Version: ManifestVersion, CreatedAt: time.Now(), Home: "/nowhere", Files: []string{"config.json", "state/gateway.db"}}
	err := writeArchive(path, m, func(tw *tar.Writer) error {
		for name, body := range map[string]string{
			"config.json":      `{"restored":true}`,
			"state/gateway.db": "SQLite format 3\x00 but the rest is garbage ...............................",
		} {
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(len(body)), Format: tar.FormatPAX}); err != nil {
				return err
			}
			if _, err := tw.Write([]byte(body)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRestoreAbortsOnCorruptDatabase(t *testing.T) {
	archive := corruptArchive(t, t.TempDir())
	target := t.TempDir()
	writeFileT(t, filepath.Join(target, "config.json"), `{"old":true}`, 0o600)
	writeFileT(t, filepath.Join(target, "state", "gateway.db"), "old", 0o600)

	plan, err := PlanRestore(archive, target)
	if err != nil {
		t.Fatal(err)
	}
	_, err = plan.Apply(time.Now())
	if err == nil {
		t.Fatal("Apply must fail on a corrupt restored database")
	}
	for name, want := range map[string]string{"config.json": `{"old":true}`, "state/gateway.db": "old"} {
		if got, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(name))); err != nil || string(got) != want {
			t.Errorf("%s = %q (err %v), want unchanged %q", name, got, err, want)
		}
	}
	for _, glob := range []string{"restore-backup-*", ".restore-staging-*"} {
		if matches, err := filepath.Glob(filepath.Join(target, glob)); err != nil || len(matches) != 0 {
			t.Errorf("%s left behind: %v (err %v)", glob, matches, err)
		}
	}
}

func TestRestoreRejectsForeignArchives(t *testing.T) {
	dir := t.TempDir()
	notGzip := filepath.Join(dir, "x.tar.gz")
	writeFileT(t, notGzip, "plain text", 0o600)
	if _, err := PlanRestore(notGzip, dir); err == nil {
		t.Error("plain file accepted")
	}
	// A manifest from a layout this build does not read.
	wrongVersion := filepath.Join(dir, "y.tar.gz")
	if err := writeArchive(wrongVersion, Manifest{Version: ManifestVersion + 1}, func(*tar.Writer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanRestore(wrongVersion, dir); err == nil || !strings.Contains(err.Error(), "manifest version") {
		t.Errorf("wrong manifest version accepted: %v", err)
	}
	// An archive with a manifest and no files is valid, if pointless.
	empty := filepath.Join(dir, "z.tar.gz")
	if err := writeArchive(empty, Manifest{Version: ManifestVersion}, func(*tar.Writer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanRestore(empty, dir); err != nil {
		t.Errorf("empty archive with manifest should plan fine: %v", err)
	}
}

func TestTargetForRejectsUnsafePaths(t *testing.T) {
	home := "/home/x"
	m := &Manifest{}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "state/../../x", ".", "external/agents/x.db"} {
		if _, err := targetFor(bad, home, m); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got, err := targetFor("state/gateway.db", home, m); err != nil || got != filepath.Join(home, "state", "gateway.db") {
		t.Errorf("safe path: %s %v", got, err)
	}
	m.AgentsDir = "/srv/agents"
	if got, err := targetFor("external/agents/bot/s.db", home, m); err != nil || got != "/srv/agents/bot/s.db" {
		t.Errorf("external path: %s %v", got, err)
	}
}

func TestDestFor(t *testing.T) {
	cfg := config.DefaultConfig()
	if got := DestFor(cfg, "/mnt/override"); got != "/mnt/override" {
		t.Errorf("override: %s", got)
	}
	cfg.Backup.Dest = "/mnt/nas/claw"
	if got := DestFor(cfg, ""); got != "/mnt/nas/claw" {
		t.Errorf("config dest: %s", got)
	}
	cfg.Backup.Dest = ""
	if got, want := DestFor(cfg, ""), filepath.Join(cfg.DataDir(), "backup"); got != want {
		t.Errorf("default: %s, want %s", got, want)
	}
}
