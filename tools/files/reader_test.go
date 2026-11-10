// ClawEh
// License: MIT

package files

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/utils"
)

// readerConfig builds a config with the workspace restrictions the reader is
// meant to inherit: reads confined to the workspace, and (via SetReadScopeSubdirs)
// to files/ within it.
func readerConfig() *config.Config {
	c := &config.Config{}
	c.Agents.Defaults.RestrictToWorkspace = true
	c.Agents.Defaults.AllowReadOutsideWorkspace = false
	return c
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReaderReadsInsideReadScope(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "files", "voice.md"), "my voice")
	SetReadScopeSubdirs([]string{"files"})
	defer SetReadScopeSubdirs(nil)

	got, err := NewReader(readerConfig(), ws).ReadFile("files/voice.md")
	if err != nil {
		t.Fatalf("read in scope: %v", err)
	}
	if string(got) != "my voice" {
		t.Fatalf("unexpected content: %q", got)
	}
}

// A path inside the workspace but outside the read scope is denied, exactly as
// it would be for file_read.
func TestReaderDeniesOutsideReadScope(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "secrets", "keys.md"), "nope")
	SetReadScopeSubdirs([]string{"files"})
	defer SetReadScopeSubdirs(nil)

	if _, err := NewReader(readerConfig(), ws).ReadFile("secrets/keys.md"); err == nil {
		t.Fatal("expected read outside the read scope to be denied")
	}
}

func TestReaderDeniesTraversalAndAbsolutePaths(t *testing.T) {
	ws := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeFile(t, outside, "host file")
	SetReadScopeSubdirs([]string{"files"})
	defer SetReadScopeSubdirs(nil)

	r := NewReader(readerConfig(), ws)
	for _, p := range []string{"../outside.md", "files/../../outside.md", outside} {
		if _, err := r.ReadFile(p); err == nil {
			t.Fatalf("expected %q to be denied", p)
		}
	}
}

// Allowed folders and maestro/ are readable through the Reader, so a memory
// may attach a document that lives outside the workspace but inside a folder
// the agent already has.
func TestReaderReadsAllowedFolders(t *testing.T) {
	ws := t.TempDir()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "style.md"), "folder doc")
	writeFile(t, filepath.Join(ws, "maestro", "plan.md"), "maestro doc")
	SetReadScopeSubdirs([]string{"files"})
	defer SetReadScopeSubdirs(nil)
	allowFolders(t, ws, src, filepath.Join(ws, "maestro"))

	r := NewReader(readerConfig(), ws)
	if got, err := r.ReadFile(filepath.Join(src, "style.md")); err != nil || string(got) != "folder doc" {
		t.Fatalf("read in allowed folder: %q %v", got, err)
	}
	if got, err := r.ReadFile("maestro/plan.md"); err != nil || string(got) != "maestro doc" {
		t.Fatalf("read maestro/: %q %v", got, err)
	}
}

// A folder registered after the Reader was constructed must still be
// visible: the file tools install folders at registration time, which can
// happen later.
func TestReaderPicksUpLateRegisteredFolder(t *testing.T) {
	ws := t.TempDir()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "late.md"), "late doc")
	SetReadScopeSubdirs([]string{"files"})
	defer SetReadScopeSubdirs(nil)

	r := NewReader(readerConfig(), ws)
	if _, err := r.ReadFile(filepath.Join(src, "late.md")); err == nil {
		t.Fatal("expected failure before the folder is allowed")
	}

	allowFolders(t, ws, src)

	if _, err := r.ReadFile(filepath.Join(src, "late.md")); err != nil {
		t.Fatalf("late folder not honored: %v", err)
	}
}

func TestReadFileLimitTruncatesAndReportsMore(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "files", "big.md"), strings.Repeat("x", 100))
	SetReadScopeSubdirs([]string{"files"})
	defer SetReadScopeSubdirs(nil)
	r := NewReader(readerConfig(), ws)

	data, more, err := r.ReadFileLimit("files/big.md", 10)
	if err != nil {
		t.Fatalf("limited read: %v", err)
	}
	if len(data) != 10 || !more {
		t.Fatalf("expected a 10-byte prefix with more=true, got %d/%v", len(data), more)
	}

	data, more, err = r.ReadFileLimit("files/big.md", 100)
	if err != nil {
		t.Fatalf("exact-size read: %v", err)
	}
	if len(data) != 100 || more {
		t.Fatalf("exact-size read should not report more: %d/%v", len(data), more)
	}

	data, more, err = r.ReadFileLimit("files/big.md", 0)
	if err != nil || len(data) != 100 || more {
		t.Fatalf("unlimited read: %d/%v err=%v", len(data), more, err)
	}
}

func TestReadFileLimitRespectsPermissions(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "secrets", "keys.md"), "nope")
	SetReadScopeSubdirs([]string{"files"})
	defer SetReadScopeSubdirs(nil)

	if _, _, err := NewReader(readerConfig(), ws).ReadFileLimit("secrets/keys.md", 10); err == nil {
		t.Fatal("limited read must enforce the same scope as ReadFile")
	}
}

// Resolve gives the host path of what the agent may read (a workspace path or
// one in an allowed folder) and refuses the rest; Allowed checks a host path
// the same way.
func TestReaderResolveAndAllowed(t *testing.T) {
	ws := t.TempDir()
	mountDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	writeFile(t, filepath.Join(ws, "files", "forum.json"), "{}")
	writeFile(t, filepath.Join(ws, "secrets", "keys.md"), "nope")
	writeFile(t, filepath.Join(mountDir, "refs", "brief.md"), "brief")
	writeFile(t, outside, "{}")
	SetReadScopeSubdirs([]string{"files"})
	defer SetReadScopeSubdirs(nil)
	allowFolders(t, ws, mountDir)
	r := NewReader(readerConfig(), ws)

	brief := filepath.Join(mountDir, "refs", "brief.md")
	for ref, want := range map[string]string{
		"files/forum.json": filepath.Join(ws, "files", "forum.json"),
		brief:              brief,
	} {
		got, err := r.Resolve(ref)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", ref, got, err, want)
		}
		if err := r.Allowed(want); err != nil {
			t.Errorf("Allowed(%q) = %v, want nil", want, err)
		}
	}
	for _, ref := range []string{"secrets/keys.md", "../outside.json", outside, "files/missing.json"} {
		if got, err := r.Resolve(ref); err == nil {
			t.Errorf("Resolve(%q) = %q, want a refusal", ref, got)
		}
	}
	for _, abs := range []string{filepath.Join(ws, "secrets", "keys.md"), outside} {
		if err := r.Allowed(abs); err == nil {
			t.Errorf("Allowed(%q) = nil, want a refusal", abs)
		}
	}
}

// A workspace reached through a symbolic link: a path given by its resolved
// target is still the workspace's, and is checked as such.
func TestReaderAllowedThroughSymlinkedWorkspace(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "ws")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(target, "files", "brief.md"), "brief")
	writeFile(t, filepath.Join(target, "secrets", "keys.md"), "nope")
	SetReadScopeSubdirs([]string{"files"})
	defer SetReadScopeSubdirs(nil)
	r := NewReader(readerConfig(), link)
	for _, p := range []string{filepath.Join(target, "files", "brief.md"), filepath.Join(link, "files", "brief.md")} {
		if err := r.Allowed(p); err != nil {
			t.Errorf("Allowed(%q) = %v, want nil", p, err)
		}
	}
	if err := r.Allowed(filepath.Join(target, "secrets", "keys.md")); err == nil {
		t.Error("Allowed accepted a file outside the read scope through the resolved workspace")
	}
	if got, err := r.Resolve("files/brief.md"); err != nil || got != filepath.Join(link, "files", "brief.md") {
		t.Errorf("Resolve = %q, %v", got, err)
	}
}

// Open reads what the agent may read (a workspace path, by its relative or
// absolute name, also through a linked workspace, or a file in an allowed
// folder) and refuses the rest.
func TestReaderOpen(t *testing.T) {
	target := t.TempDir()
	ws := filepath.Join(t.TempDir(), "ws")
	if err := os.Symlink(target, ws); err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeFile(t, filepath.Join(target, "files", "brief.md"), "brief")
	writeFile(t, filepath.Join(target, "secrets", "keys.md"), "nope")
	writeFile(t, filepath.Join(folder, "doc.md"), "doc")
	writeFile(t, outside, "host")
	SetReadScopeSubdirs([]string{"files"})
	defer SetReadScopeSubdirs(nil)
	allowFolders(t, ws, folder)
	r := NewReader(readerConfig(), ws)

	for p, want := range map[string]string{
		"files/brief.md":                           "brief",
		filepath.Join(ws, "files", "brief.md"):     "brief",
		filepath.Join(target, "files", "brief.md"): "brief",
		filepath.Join(folder, "doc.md"):            "doc",
	} {
		f, err := r.Open(p)
		if err != nil {
			t.Errorf("Open(%q) = %v", p, err)
			continue
		}
		data, err := io.ReadAll(f)
		utils.CloseQuietly(f)
		if err != nil || string(data) != want {
			t.Errorf("Open(%q) read %q, %v; want %q", p, data, err, want)
		}
	}
	for _, p := range []string{"secrets/keys.md", "../outside.md", outside, filepath.Join(target, "secrets", "keys.md")} {
		if f, err := r.Open(p); err == nil {
			utils.CloseQuietly(f)
			t.Errorf("Open(%q) succeeded, want a refusal", p)
		}
	}
}

// A file checked as readable and then replaced by a link leading out of the
// workspace or out of an allowed folder cannot be read: the open goes
// through the same sandbox as the check, not through the host path.
func TestReaderOpen_LinkSwappedAfterCheck(t *testing.T) {
	ws := t.TempDir()
	folder := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.md")
	writeFile(t, secret, "secret")
	inWs := filepath.Join(ws, "files", "brief.md")
	inFolder := filepath.Join(folder, "doc.md")
	writeFile(t, inWs, "brief")
	writeFile(t, inFolder, "doc")
	allowFolders(t, ws, folder)
	r := NewReader(readerConfig(), ws)

	for _, abs := range []string{inWs, inFolder} {
		if err := r.Allowed(abs); err != nil {
			t.Fatalf("Allowed(%q) = %v before the swap", abs, err)
		}
		if err := os.Remove(abs); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, abs); err != nil {
			t.Fatal(err)
		}
		if f, err := r.Open(abs); err == nil {
			data, _ := io.ReadAll(f)
			utils.CloseQuietly(f)
			t.Errorf("Open(%q) after the swap read %q, want a refusal", abs, data)
		}
	}
}
