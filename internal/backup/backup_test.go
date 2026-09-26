package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

// openWAL creates (or opens) a WAL-mode SQLite database with a table t(id, v)
// and inserts n rows, keeping the connection open so the rows stay in the WAL.
func openWAL(t *testing.T, path string, n int) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close %s: %v", path, err)
		}
	})
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if _, err := db.ExecContext(ctx, `INSERT INTO t (v) VALUES (?)`, fmt.Sprintf("row-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// rowCount opens path read-only and counts rows in t.
func rowCount(t *testing.T, path string) int {
	t.Helper()
	db, err := openReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close %s: %v", path, err)
		}
	}()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", path, err)
	}
	return n
}

func writeFileT(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// fixture builds a CLAW_HOME with everything the backup should and should not
// pick up. The live databases stay open for the test's lifetime.
func fixture(t *testing.T) Source {
	t.Helper()
	home := t.TempDir()
	writeFileT(t, filepath.Join(home, "config.json"), `{"agents":{}}`, 0o600)
	writeFileT(t, filepath.Join(home, "cron", "jobs.json"), `[]`, 0o644)
	writeFileT(t, filepath.Join(home, "credentials.json"), `{"k":"v"}`, 0o600)
	writeFileT(t, filepath.Join(home, "tls", "server.key"), "KEY", 0o600)
	writeFileT(t, filepath.Join(home, "state", "tokens.json"), `{"t":1}`, 0o600)
	writeFileT(t, filepath.Join(home, "media", "cache.bin"), "media", 0o644)
	writeFileT(t, filepath.Join(home, "logs", "claw.log"), "log", 0o644)
	writeFileT(t, filepath.Join(home, "backup", "claw-backup-20200101-000000.tar.gz"), "old", 0o600)
	writeFileT(t, filepath.Join(home, "agents", "main", "tmp", "scratch.db"), "not a db", 0o644)
	writeFileT(t, filepath.Join(home, "agents", "main", "files", "notes.txt"), "not archived", 0o644)
	openWAL(t, filepath.Join(home, "state", "gateway.db"), 5)
	openWAL(t, filepath.Join(home, "agents", "main", "sessions", "s1.archive.db"), 50)
	openWAL(t, filepath.Join(home, "agents", "main", "cogmem", "memory.sqlite"), 3)
	return Source{
		Home:       home,
		ConfigPath: filepath.Join(home, "config.json"),
		CronDir:    filepath.Join(home, "cron"),
		AgentsDir:  filepath.Join(home, "agents"),
	}
}

// readTar returns the manifest and every entry (name → header, content).
type tarEntry struct {
	hdr  *tar.Header
	data []byte
}

func readTar(t *testing.T, archive string) (Manifest, map[string]tarEntry) {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			t.Errorf("close %s: %v", archive, closeErr)
		}
	}()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	entries := map[string]tarEntry{}
	var order []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries[hdr.Name] = tarEntry{hdr: hdr, data: data}
		order = append(order, hdr.Name)
	}
	if len(order) == 0 || order[0] != ManifestName {
		t.Fatalf("manifest must be the first entry, got %v", order)
	}
	var m Manifest
	if err := json.Unmarshal(entries[ManifestName].data, &m); err != nil {
		t.Fatal(err)
	}
	return m, entries
}

func TestRunArchivesLiveWALDatabases(t *testing.T) {
	src := fixture(t)
	// The session archive must still have rows sitting in its WAL, or the test
	// would not prove anything about live databases.
	if fi, err := os.Stat(filepath.Join(src.Home, "agents", "main", "sessions", "s1.archive.db-wal")); err != nil || fi.Size() == 0 {
		t.Fatalf("fixture WAL missing or empty: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "out")
	now := time.Date(2026, 6, 19, 3, 0, 0, 0, time.UTC)
	res, err := Run(src, dest, now, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("skipped = %+v, want none", res.Skipped)
	}
	if want := filepath.Join(dest, "claw-backup-20260619-030000.tar.gz"); res.Archive != want {
		t.Fatalf("archive = %s, want %s", res.Archive, want)
	}
	if fi, err := os.Stat(res.Archive); err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != res.Bytes || res.Bytes == 0 {
		t.Fatalf("archive stat: %+v err=%v bytes=%d", fi, err, res.Bytes)
	}
	if fi, err := os.Stat(dest); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dest dir mode = %v err=%v, want 0700", fi.Mode(), err)
	}

	m, entries := readTar(t, res.Archive)
	want := []string{
		"config.json", "cron/jobs.json", "credentials.json", "tls/server.key",
		"state/gateway.db", "state/tokens.json",
		"agents/main/cogmem/memory.sqlite", "agents/main/sessions/s1.archive.db",
	}
	if !slices.Equal(m.Files, want) {
		t.Fatalf("manifest files = %v, want %v", m.Files, want)
	}
	if res.Files != len(want) {
		t.Fatalf("Files = %d, want %d", res.Files, len(want))
	}
	if m.Home != src.Home || m.AgentsDir != "" || m.Version != ManifestVersion {
		t.Fatalf("manifest = %+v", m)
	}
	for _, excluded := range []string{
		"media/cache.bin", "logs/claw.log", "backup/claw-backup-20200101-000000.tar.gz",
		"agents/main/tmp/scratch.db", "agents/main/files/notes.txt",
		"state/gateway.db-wal", "state/gateway.db-shm",
	} {
		if _, ok := entries[excluded]; ok {
			t.Errorf("%s must not be archived", excluded)
		}
	}
	if string(entries["config.json"].data) != `{"agents":{}}` {
		t.Errorf("config.json = %q", entries["config.json"].data)
	}
	if string(entries["state/tokens.json"].data) != `{"t":1}` {
		t.Errorf("state/tokens.json = %q", entries["state/tokens.json"].data)
	}
	if got := entries["tls/server.key"].hdr.Mode; got != 0o600 {
		t.Errorf("tls/server.key mode = %o, want 600", got)
	}
	if got := entries["cron/jobs.json"].hdr.Mode; got != 0o644 {
		t.Errorf("cron/jobs.json mode = %o, want 644", got)
	}

	// The archived session DB is a self-contained copy that passes quick_check
	// and holds every row, WAL included.
	extracted := filepath.Join(t.TempDir(), "s1.archive.db")
	if err := os.WriteFile(extracted, entries["agents/main/sessions/s1.archive.db"].data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := QuickCheck(extracted); err != nil {
		t.Fatalf("extracted DB quick_check: %v", err)
	}
	if n := rowCount(t, extracted); n != 50 {
		t.Fatalf("extracted rows = %d, want 50", n)
	}
	// And the live database was not disturbed.
	if n := rowCount(t, filepath.Join(src.Home, "agents", "main", "sessions", "s1.archive.db")); n != 50 {
		t.Fatalf("live rows = %d, want 50", n)
	}
}

func TestRunDefaultDestUnderHomeIsExcluded(t *testing.T) {
	src := fixture(t)
	dest := filepath.Join(src.Home, "backup")
	res, err := Run(src, dest, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, entries := readTar(t, res.Archive)
	for name := range entries {
		if filepath.Dir(name) == "backup" {
			t.Errorf("archive contains an older backup: %s", name)
		}
	}
	// The temp dir used for VACUUM copies is gone.
	matches, err := filepath.Glob(filepath.Join(dest, ".claw-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("temp dirs left behind: %v", matches)
	}
}

func TestRunSkipsCorruptDatabaseAndAlerts(t *testing.T) {
	src := fixture(t)
	bad := filepath.Join(src.Home, "agents", "other", "sessions", "bad.archive.db")
	writeFileT(t, bad, "this is not a sqlite file, just enough bytes to look like one .............", 0o600)
	rec := &testalerts.Recorder{}
	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), rec)
	if err != nil {
		t.Fatalf("Run must not fail on one bad database: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Path != bad {
		t.Fatalf("skipped = %+v, want just %s", res.Skipped, bad)
	}
	m, _ := readTar(t, res.Archive)
	if slices.Contains(m.Files, "agents/other/sessions/bad.archive.db") {
		t.Error("corrupt DB must not be in the archive")
	}
	if !slices.Contains(m.Files, "agents/main/sessions/s1.archive.db") {
		t.Error("healthy DBs must still be archived")
	}
	got := rec.Alerts()
	if len(got) != 1 {
		t.Fatalf("alerts = %d, want 1: %+v", len(got), got)
	}
	want := alerter.Alert{Title: "Database failed integrity check", EventID: "backup:agents/other/sessions/bad.archive.db"}
	if got[0].Title != want.Title || got[0].EventID != want.EventID || got[0].Details == "" {
		t.Errorf("alert = %+v", got[0])
	}
}

func TestRunNilAlerterUsesProcessDefault(t *testing.T) {
	rec := testalerts.Install(t)
	src := fixture(t)
	writeFileT(t, filepath.Join(src.Home, "state", "broken.db"), "garbage garbage garbage garbage garbage", 0o600)
	if _, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if len(rec.Alerts()) != 1 {
		t.Fatalf("alerts = %+v, want 1", rec.Alerts())
	}
}

func TestRunExternalAgentsDir(t *testing.T) {
	src := fixture(t)
	ext := t.TempDir()
	openWAL(t, filepath.Join(ext, "bot", "sessions", "x.archive.db"), 2)
	src.AgentsDir = ext
	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := readTar(t, res.Archive)
	if m.AgentsDir != ext {
		t.Errorf("manifest agents_dir = %q, want %q", m.AgentsDir, ext)
	}
	if !slices.Contains(m.Files, "external/agents/bot/sessions/x.archive.db") {
		t.Errorf("external agent DB missing: %v", m.Files)
	}
}

func TestRunMissingOptionalFiles(t *testing.T) {
	home := t.TempDir()
	writeFileT(t, filepath.Join(home, "config.json"), `{}`, 0o600)
	src := Source{Home: home, ConfigPath: filepath.Join(home, "config.json"), CronDir: filepath.Join(home, "cron"), AgentsDir: filepath.Join(home, "agents")}
	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := readTar(t, res.Archive)
	if !slices.Equal(m.Files, []string{"config.json"}) {
		t.Fatalf("files = %v", m.Files)
	}
}

func TestPruneRemovesOldArchivesAndLegacyFolders(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 19, 3, 0, 0, 0, time.UTC)
	for _, name := range []string{"20260619", "20260614", "20260510", "notes"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{
		"claw-backup-20260619-030000.tar.gz", // today
		"claw-backup-20260520-030000.tar.gz", // exactly 30 days: kept
		"claw-backup-20260519-235959.tar.gz", // 31 days: pruned
		"claw-backup-garbage.tar.gz",         // unparseable: kept
		"unrelated.tar.gz",                   // kept
	} {
		writeFileT(t, filepath.Join(root, name), "x", 0o600)
	}
	removed, err := Prune(root, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	for _, gone := range []string{"20260510", "claw-backup-20260519-235959.tar.gz"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should be pruned", gone)
		}
	}
	for _, keep := range []string{
		"20260619", "20260614", "notes", "claw-backup-20260619-030000.tar.gz",
		"claw-backup-20260520-030000.tar.gz", "claw-backup-garbage.tar.gz", "unrelated.tar.gz",
	} {
		if _, err := os.Stat(filepath.Join(root, keep)); err != nil {
			t.Errorf("%s should be kept: %v", keep, err)
		}
	}
	if n, err := Prune(root, 0, now); err != nil || n != 0 {
		t.Errorf("retainDays=0 should prune nothing: n=%d err=%v", n, err)
	}
	if n, err := Prune(filepath.Join(root, "missing"), 30, now); err != nil || n != 0 {
		t.Errorf("missing dest: n=%d err=%v", n, err)
	}
}

func TestExcludedDir(t *testing.T) {
	cases := []struct {
		name, rel string
		want      bool
	}{
		{"media", "media", true},
		{"logs", "logs", true},
		{"backup", "backup", true},
		{"tmp", "agents/main/tmp", true},
		{"subagents", "agents/main/cogmem/subagents", true},
		{"restore-backup-20260101-000000", "restore-backup-20260101-000000", true},
		{".restore-staging-20260101-000000", ".restore-staging-20260101-000000", true},
		{"restore-backup-x", "agents/restore-backup-x", false}, // prefix rule is top-level only
		{"state", "state", false},
		{"sessions", "agents/main/sessions", false},
	}
	for _, c := range cases {
		if got := excludedDir(c.name, filepath.FromSlash(c.rel)); got != c.want {
			t.Errorf("excludedDir(%q, %q) = %v, want %v", c.name, c.rel, got, c.want)
		}
	}
}
