// ClawEh
// License: MIT

package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
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

// Mounts (e.g. the auto maestro/ tree) are readable through the Reader, so a
// memory may attach a document that lives outside the workspace but inside a
// mount the agent already has.
func TestReaderReadsThroughMount(t *testing.T) {
	ws := t.TempDir()
	mountDir := t.TempDir()
	writeFile(t, filepath.Join(mountDir, "style.md"), "mounted doc")
	SetReadScopeSubdirs([]string{"files"})
	defer SetReadScopeSubdirs(nil)
	SetMountsForWorkspace(ws, []MountSpec{{Name: "maestro", Path: mountDir}})
	defer SetMountsForWorkspace(ws, nil)

	got, err := NewReader(readerConfig(), ws).ReadFile("maestro/style.md")
	if err != nil {
		t.Fatalf("read through mount: %v", err)
	}
	if string(got) != "mounted doc" {
		t.Fatalf("unexpected content: %q", got)
	}
}

// A mount registered after the Reader was constructed must still be visible:
// the file tools install mounts at registration time, which can happen later.
func TestReaderPicksUpLateRegisteredMount(t *testing.T) {
	ws := t.TempDir()
	mountDir := t.TempDir()
	writeFile(t, filepath.Join(mountDir, "late.md"), "late doc")

	r := NewReader(readerConfig(), ws)
	if _, err := r.ReadFile("maestro/late.md"); err == nil {
		t.Fatal("expected failure before the mount exists")
	}

	SetMountsForWorkspace(ws, []MountSpec{{Name: "maestro", Path: mountDir}})
	defer SetMountsForWorkspace(ws, nil)

	if _, err := r.ReadFile("maestro/late.md"); err != nil {
		t.Fatalf("late mount not honored: %v", err)
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
