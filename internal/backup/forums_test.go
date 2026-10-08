package backup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const forumID = "0b6f1c1e-3a51-4c43-9d55-7d1f2e6f0a10"

// forumTree writes a forum of the agent at agentDir: what the store keeps
// (config, meta, run 1 with an output and the transcript, a cleanup marker, a
// source with a database extension) and what it does not (its lock file,
// temporary files of a write in progress). It returns the files that must be
// archived, relative to agentDir, with their content.
func forumTree(t *testing.T, agentDir string) map[string]string {
	t.Helper()
	forums := filepath.Join(agentDir, "forums")
	keep := map[string]string{
		"forums/" + forumID + "/forum-meta.json":                              `{"owner":"alice"}`,
		"forums/" + forumID + "/forum.json":                                   `{"layers":[]}`,
		"forums/" + forumID + "/runs/1/snapshot.json":                         `{"run":1}`,
		"forums/" + forumID + "/runs/1/transcript.md":                         "# Forum\n",
		"forums/" + forumID + "/runs/1/layers/draft/calls/t1/1/output.md":     "Alice's draft",
		"forums/" + forumID + "/runs/1/commits/00000001.json":                 `{"seq":1}`,
		"forums/" + forumID + "/runs/1/sources/notes.db":                      "not a sqlite file",
		"forums/.cleanup/" + forumID + ".1.agents.json":                       `["x"]`,
		"forums/" + forumID + "/runs/1/layers/tmp/calls/t1/1/output.md":       "a layer named tmp",
		"forums/" + forumID + "/runs/1/layers/logs/calls/t1/1/published.json": `{}`,
	}
	for rel, content := range keep {
		writeFileT(t, filepath.Join(agentDir, filepath.FromSlash(rel)), content, 0o600)
	}
	writeFileT(t, filepath.Join(forums, ".locks", forumID+".run"), "123\n", 0o600)
	writeFileT(t, filepath.Join(forums, forumID, ".tmp-1234"), "half a config", 0o600)
	writeFileT(t, filepath.Join(forums, forumID, "runs", "1", "commits", ".tmp-5678"), "half a commit", 0o600)
	writeFileT(t, filepath.Join(forums, forumID, "runs", ".tmp-run-abcd", "snapshot.json"), "a run being removed", 0o600)
	// A link inside the forums folder pointing at a secret elsewhere, and a
	// linked directory.
	secret := filepath.Join(t.TempDir(), "secret")
	writeFileT(t, filepath.Join(secret, "key.txt"), "SECRET", 0o600)
	if err := os.Symlink(filepath.Join(secret, "key.txt"), filepath.Join(forums, forumID, "runs", "1", "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(forums, forumID, "runs", "1", "linkdir")); err != nil {
		t.Fatal(err)
	}
	return keep
}

func TestRunArchivesForums(t *testing.T) {
	src := fixture(t)
	keep := forumTree(t, filepath.Join(src.Home, "agents", "main"))
	// An agent whose forums folder is itself a link is not followed.
	linked := t.TempDir()
	writeFileT(t, filepath.Join(linked, forumID, "forum.json"), "elsewhere", 0o600)
	if err := os.MkdirAll(filepath.Join(src.Home, "agents", "bob"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linked, filepath.Join(src.Home, "agents", "bob", "forums")); err != nil {
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
		if e.hdr.Mode != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, e.hdr.Mode)
		}
	}
	for name := range entries {
		for _, bad := range []string{"/.locks/", "/.tmp-", "link", "agents/bob/"} {
			if strings.Contains(name, bad) {
				t.Errorf("%s must not be archived", name)
			}
		}
	}
	if want := []string{"agents/main/forums/" + forumID + "/runs/1/sources/notes.db"}; !slices.Equal(m.Plain, want) {
		t.Errorf("manifest plain = %v, want %v", m.Plain, want)
	}
}

func TestRunArchivesExternalAgentForums(t *testing.T) {
	src := fixture(t)
	ext := t.TempDir()
	keep := forumTree(t, filepath.Join(ext, "alice"))
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
	if _, ok := entries["external/agents/alice/forums/.locks/"+forumID+".run"]; ok {
		t.Error("the external forum lock file must not be archived")
	}

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
	for rel, content := range keep {
		got, err := os.ReadFile(filepath.Join(ext, "alice", filepath.FromSlash(rel)))
		if err != nil || string(got) != content {
			t.Errorf("restored %s = %q err=%v, want %q", rel, got, err, content)
		}
	}
}

func TestRestoreForums(t *testing.T) {
	src := fixture(t)
	keep := forumTree(t, filepath.Join(src.Home, "agents", "main"))
	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	plan, err := PlanRestore(res.Archive, target)
	if err != nil {
		t.Fatal(err)
	}
	// The forum source named *.db is not SQLite; the restore must not check it.
	if _, err := plan.Apply(time.Now()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for rel, content := range keep {
		path := filepath.Join(target, "agents", "main", filepath.FromSlash(rel))
		got, err := os.ReadFile(path)
		if err != nil || string(got) != content {
			t.Errorf("restored %s = %q err=%v, want %q", rel, got, err, content)
			continue
		}
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("restored %s mode = %v err=%v, want 0600", rel, fi.Mode().Perm(), err)
		}
	}
	for _, dir := range []string{"forums", "forums/" + forumID, "forums/" + forumID + "/runs/1", "forums/.cleanup"} {
		fi, err := os.Stat(filepath.Join(target, "agents", "main", filepath.FromSlash(dir)))
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("restored dir %s: err=%v mode=%v, want 0700", dir, err, fi)
		}
	}
	for _, absent := range []string{"forums/.locks", "forums/" + forumID + "/.tmp-1234", "forums/" + forumID + "/runs/1/link.txt"} {
		if _, err := os.Lstat(filepath.Join(target, "agents", "main", filepath.FromSlash(absent))); !os.IsNotExist(err) {
			t.Errorf("%s must not be restored (err=%v)", absent, err)
		}
	}
}
