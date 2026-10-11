package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// maestroFile is one file Maestro keeps, with the mode it was written with.
type maestroFile struct {
	content string
	mode    os.FileMode
}

// maestroTree writes an agent's Maestro data at agentDir: what Maestro keeps
// (projects with a taskset, a per-project log and files, playbooks, the
// library, lists) and what it does not (the flock beside a taskset, the
// temporary file of an atomic write, symbolic links). It returns the files
// that must be archived, relative to agentDir.
func maestroTree(t *testing.T, agentDir string) map[string]maestroFile {
	t.Helper()
	root := filepath.Join(agentDir, "maestro")
	keep := map[string]maestroFile{
		"maestro/projects/audit/project.json":           {`{"name":"audit"}`, 0o600},
		"maestro/projects/audit/tasksets/main.json":     {`{"tasks":[]}`, 0o600},
		"maestro/projects/audit/logs/project.log":       {"Alice started\n", 0o644},
		"maestro/projects/audit/files/tmp/notes.md":     {"a folder named tmp", 0o640},
		"maestro/projects/audit/files/report.pdf.json":  {`{"meta":1}`, 0o600},
		"maestro/playbooks/review/guide.md":             {"# Review\n", 0o644},
		"maestro/library/reference.md":                  {"Bob's reference", 0o600},
		"maestro/lists/findings.json":                   {`{"items":[]}`, 0o600},
		"maestro/projects/audit/files/media/chart.json": {`{}`, 0o600},
	}
	for rel, f := range keep {
		writeFileT(t, filepath.Join(agentDir, filepath.FromSlash(rel)), f.content, f.mode)
	}
	writeFileT(t, filepath.Join(root, "projects", "audit", "tasksets", "main.json.lock"), "", 0o600)
	writeFileT(t, filepath.Join(root, "projects", "audit", "project.json.tmp"), "half a project", 0o600)
	writeFileT(t, filepath.Join(root, "library", "reference.md.tmp"), "half a file", 0o600)
	secret := filepath.Join(t.TempDir(), "secret")
	writeFileT(t, filepath.Join(secret, "key.txt"), "SECRET", 0o600)
	if err := os.Symlink(filepath.Join(secret, "key.txt"), filepath.Join(root, "projects", "audit", "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "playbooks", "linkdir")); err != nil {
		t.Fatal(err)
	}
	return keep
}

// checkNoMaestroScratch fails for any archived Maestro lock, temporary file
// or link target.
func checkNoMaestroScratch(t *testing.T, entries map[string]tarEntry) {
	t.Helper()
	for name := range entries {
		if !strings.Contains(name, "/maestro/") {
			continue
		}
		for _, bad := range []string{".lock", ".tmp", "link", "-wal", "-shm"} {
			if strings.Contains(name, bad) {
				t.Errorf("%s must not be archived", name)
			}
		}
	}
}

func TestRunArchivesMaestro(t *testing.T) {
	src := fixture(t)
	keep := maestroTree(t, filepath.Join(src.Home, "agents", "main"))
	// An agent whose maestro folder is itself a link is not followed.
	linked := t.TempDir()
	writeFileT(t, filepath.Join(linked, "projects", "x", "project.json"), "elsewhere", 0o600)
	if err := os.MkdirAll(filepath.Join(src.Home, "agents", "bob"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linked, filepath.Join(src.Home, "agents", "bob", "maestro")); err != nil {
		t.Fatal(err)
	}

	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("skipped = %+v, want none", res.Skipped)
	}
	m, entries := readTar(t, res.Archive)
	for rel, f := range keep {
		name := "agents/main/" + rel
		e, ok := entries[name]
		if !ok {
			t.Errorf("%s missing from the archive", name)
			continue
		}
		if string(e.data) != f.content {
			t.Errorf("%s = %q, want %q", name, e.data, f.content)
		}
		if os.FileMode(e.hdr.Mode) != f.mode {
			t.Errorf("%s mode = %o, want %o", name, e.hdr.Mode, f.mode)
		}
	}
	checkNoMaestroScratch(t, entries)
	for name := range entries {
		if strings.HasPrefix(name, "agents/bob/") {
			t.Errorf("%s must not be archived (linked maestro folder)", name)
		}
	}
	if len(m.Plain) != 0 {
		t.Errorf("manifest plain = %v, want none", m.Plain)
	}
}

func TestRunArchivesMaestroDatabaseWithVacuum(t *testing.T) {
	src := fixture(t)
	dbPath := filepath.Join(src.Home, "agents", "main", "maestro", "projects", "audit", "state.db")
	openWAL(t, dbPath, 7) // rows stay in the WAL while the connection is open

	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m, entries := readTar(t, res.Archive)
	name := "agents/main/maestro/projects/audit/state.db"
	e, ok := entries[name]
	if !ok {
		t.Fatalf("%s missing from the archive", name)
	}
	for _, p := range m.Plain {
		if p == name {
			t.Errorf("%s recorded as plain; want a VACUUM copy", name)
		}
	}
	checkNoMaestroScratch(t, entries)
	out := filepath.Join(t.TempDir(), "state.db")
	writeFileT(t, out, string(e.data), 0o600)
	if n := rowCount(t, out); n != 7 {
		t.Errorf("archived database rows = %d, want 7", n)
	}
}

func TestRestoreMaestro(t *testing.T) {
	src := fixture(t)
	keep := maestroTree(t, filepath.Join(src.Home, "agents", "main"))
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
	for rel, f := range keep {
		path := filepath.Join(target, "agents", "main", filepath.FromSlash(rel))
		got, err := os.ReadFile(path)
		if err != nil || string(got) != f.content {
			t.Errorf("restored %s = %q err=%v, want %q", rel, got, err, f.content)
			continue
		}
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != f.mode {
			t.Errorf("restored %s err=%v, want mode %o", rel, err, f.mode)
		}
	}
	for _, absent := range []string{
		"maestro/projects/audit/tasksets/main.json.lock",
		"maestro/projects/audit/project.json.tmp",
		"maestro/projects/audit/link.txt",
		"maestro/playbooks/linkdir",
	} {
		if _, err := os.Lstat(filepath.Join(target, "agents", "main", filepath.FromSlash(absent))); !os.IsNotExist(err) {
			t.Errorf("%s must not be restored (err=%v)", absent, err)
		}
	}
}

func TestRunArchivesExternalAgentMaestro(t *testing.T) {
	src := fixture(t)
	ext := t.TempDir()
	keep := maestroTree(t, filepath.Join(ext, "alice"))
	src.AgentsDir = ext
	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m, entries := readTar(t, res.Archive)
	if m.AgentsDir != ext {
		t.Fatalf("manifest agents_dir = %q, want %q", m.AgentsDir, ext)
	}
	for rel := range keep {
		if _, ok := entries["external/agents/alice/"+rel]; !ok {
			t.Errorf("external/agents/alice/%s missing", rel)
		}
	}
	checkNoMaestroScratch(t, entries)

	// Restore puts them back under the recorded agents directory.
	if rmErr := os.RemoveAll(filepath.Join(ext, "alice")); rmErr != nil {
		t.Fatal(rmErr)
	}
	plan, err := PlanRestore(res.Archive, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(time.Now()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for rel, f := range keep {
		got, err := os.ReadFile(filepath.Join(ext, "alice", filepath.FromSlash(rel)))
		if err != nil || string(got) != f.content {
			t.Errorf("restored %s = %q err=%v, want %q", rel, got, err, f.content)
		}
	}
}
