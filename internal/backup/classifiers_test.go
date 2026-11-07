package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// classifierTree writes an agent's classifiers folder at agentDir: what it
// keeps (two definitions, one hand-edited and invalid) and what it does not
// (the temporary file of a definition write in progress, a symbolic link).
// It returns the plain files that must be archived, relative to agentDir,
// with their content.
func classifierTree(t *testing.T, agentDir string) map[string]string {
	t.Helper()
	root := filepath.Join(agentDir, "classifiers")
	keep := map[string]string{
		"classifiers/inbox-priority.json": `{"id":"inbox-priority","owner":"alice"}`,
		"classifiers/broken.json":         `{"id": "broken",`,
	}
	for rel, content := range keep {
		writeFileT(t, filepath.Join(agentDir, filepath.FromSlash(rel)), content, 0o600)
	}
	writeFileT(t, filepath.Join(root, ".tmp-1234"), "half a definition", 0o600)
	secret := filepath.Join(t.TempDir(), "secret")
	writeFileT(t, filepath.Join(secret, "key.txt"), "SECRET", 0o600)
	if err := os.Symlink(filepath.Join(secret, "key.txt"), filepath.Join(root, "link.json")); err != nil {
		t.Fatal(err)
	}
	return keep
}

func TestRunArchivesClassifiers(t *testing.T) {
	src := fixture(t)
	agentDir := filepath.Join(src.Home, "agents", "main")
	keep := classifierTree(t, agentDir)
	dbPath := filepath.Join(agentDir, "classifiers", "state.db")
	// Rows stay in the WAL (a sidecar, not archived) while the connection is
	// open.
	openWAL(t, dbPath, 5)
	if _, err := os.Stat(dbPath + "-wal"); err != nil {
		t.Fatalf("fixture: no WAL beside state.db: %v", err)
	}

	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("skipped = %+v, want none", res.Skipped)
	}
	m, entries := readTar(t, res.Archive)
	for rel, content := range keep {
		name := "agents/main/" + rel
		e, ok := entries[name]
		if !ok {
			t.Errorf("%s missing from the archive", name)
			continue
		}
		if string(e.data) != content {
			t.Errorf("%s = %q, want %q", name, e.data, content)
		}
	}
	for name := range entries {
		if !strings.Contains(name, "/classifiers/") {
			continue
		}
		for _, bad := range []string{".tmp-", "-wal", "-shm", "link"} {
			if strings.Contains(name, bad) {
				t.Errorf("%s must not be archived", name)
			}
		}
	}

	name := "agents/main/classifiers/state.db"
	e, ok := entries[name]
	if !ok {
		t.Fatalf("%s missing from the archive", name)
	}
	for _, p := range m.Plain {
		if p == name {
			t.Errorf("%s recorded as plain; want a VACUUM copy", name)
		}
	}
	out := filepath.Join(t.TempDir(), "state.db")
	writeFileT(t, out, string(e.data), 0o600)
	if n := rowCount(t, out); n != 5 {
		t.Errorf("archived database rows = %d, want 5", n)
	}
}

func TestRestoreClassifiers(t *testing.T) {
	src := fixture(t)
	keep := classifierTree(t, filepath.Join(src.Home, "agents", "main"))
	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	plan, err := PlanRestore(res.Archive, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(time.Now()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for rel, content := range keep {
		got, err := os.ReadFile(filepath.Join(target, "agents", "main", filepath.FromSlash(rel)))
		if err != nil || string(got) != content {
			t.Errorf("restored %s = %q err=%v, want %q", rel, got, err, content)
		}
	}
	for _, absent := range []string{"classifiers/.tmp-1234", "classifiers/link.json"} {
		if _, err := os.Lstat(filepath.Join(target, "agents", "main", filepath.FromSlash(absent))); !os.IsNotExist(err) {
			t.Errorf("%s must not be restored (err=%v)", absent, err)
		}
	}
}

// A classifiers folder that is a symbolic link is not followed.
func TestRunSkipsLinkedClassifiersFolder(t *testing.T) {
	src := fixture(t)
	linked := t.TempDir()
	writeFileT(t, filepath.Join(linked, "elsewhere.json"), "elsewhere", 0o600)
	if err := os.MkdirAll(filepath.Join(src.Home, "agents", "bob"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linked, filepath.Join(src.Home, "agents", "bob", "classifiers")); err != nil {
		t.Fatal(err)
	}
	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, entries := readTar(t, res.Archive)
	for name := range entries {
		if strings.HasPrefix(name, "agents/bob/") {
			t.Errorf("%s must not be archived (linked classifiers folder)", name)
		}
	}
}
