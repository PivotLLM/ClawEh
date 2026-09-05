// ClawEh - Cognitive Memory
// License: MIT

package attachfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/tools/files"
)

// testConfig mirrors the default agent posture: reads confined to the workspace,
// and (via the read scope) to files/ inside it.
func testConfig() *config.Config {
	c := &config.Config{}
	c.Agents.Defaults.RestrictToWorkspace = true
	c.Agents.Defaults.AllowReadOutsideWorkspace = false
	return c
}

func setupWorkspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	files.SetReadScopeSubdirs([]string{"files"})
	t.Cleanup(func() { files.SetReadScopeSubdirs(nil) })
	return ws
}

func write(t *testing.T, ws, rel, body string) {
	t.Helper()
	p := filepath.Join(ws, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAcceptsReadableMarkdown(t *testing.T) {
	ws := setupWorkspace(t)
	write(t, ws, "files/voice.md", "hello")

	size, err := Check(testConfig(), ws, "files/voice.md")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if size != 5 {
		t.Fatalf("size = %d, want 5", size)
	}
}

func TestCheckRejectsNonMarkdown(t *testing.T) {
	ws := setupWorkspace(t)
	write(t, ws, "files/secrets.env", "TOKEN=1")

	if _, err := Check(testConfig(), ws, "files/secrets.env"); err == nil {
		t.Fatal("expected a non-markdown path to be rejected")
	} else if !strings.Contains(err.Error(), "not a markdown file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckRejectsUnreadablePath(t *testing.T) {
	ws := setupWorkspace(t)
	write(t, ws, "secrets/keys.md", "nope")
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, ref := range []string{"secrets/keys.md", "../outside.md", outside} {
		if _, err := Check(testConfig(), ws, ref); err == nil {
			t.Fatalf("expected %q to be rejected", ref)
		}
	}
}

func TestCheckRejectsEmptyAndDirectory(t *testing.T) {
	ws := setupWorkspace(t)
	if err := os.MkdirAll(filepath.Join(ws, "files", "docs.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Check(testConfig(), ws, "   "); err == nil {
		t.Fatal("expected empty reference to be rejected")
	}
	if _, err := Check(testConfig(), ws, "files/docs.md"); err == nil {
		t.Fatal("expected a directory to be rejected")
	}
}

func TestLoaderReturnsFullFile(t *testing.T) {
	ws := setupWorkspace(t)
	body := "# Voice\n\nShort sentences.\n"
	write(t, ws, "files/voice.md", body)

	att, err := NewLoader(testConfig(), "agent", ws)("files/voice.md", 1024)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if att.Content != body || att.Truncated || att.Size != int64(len(body)) {
		t.Fatalf("unexpected attachment: %+v", att)
	}
}

func TestLoaderTruncatesAtLineBoundary(t *testing.T) {
	ws := setupWorkspace(t)
	write(t, ws, "files/big.md", "line one\nline two\nline three\n")

	// A cap that lands mid-way through "line two" must cut back to the end of
	// "line one" rather than hand the model half a sentence.
	att, err := NewLoader(testConfig(), "agent", ws)("files/big.md", 13)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !att.Truncated {
		t.Fatal("expected Truncated")
	}
	if att.Content != "line one" {
		t.Fatalf("content = %q, want %q", att.Content, "line one")
	}
	if att.Size != 29 {
		t.Fatalf("Size should be the full file size, got %d", att.Size)
	}
}

func TestLoaderReportsUnreadableFile(t *testing.T) {
	ws := setupWorkspace(t)

	if _, err := NewLoader(testConfig(), "agent", ws)("files/missing.md", 1024); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// A memory may point into a mount (the auto maestro/ tree), because the file
// tools can read it.
func TestLoaderReadsThroughMount(t *testing.T) {
	ws := setupWorkspace(t)
	mountDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(mountDir, "style.md"), []byte("mounted"), 0o644); err != nil {
		t.Fatal(err)
	}
	files.SetMountsForWorkspace(ws, []files.MountSpec{{Name: "maestro", Path: mountDir}})
	defer files.SetMountsForWorkspace(ws, nil)

	att, err := NewLoader(testConfig(), "agent", ws)("maestro/style.md", 1024)
	if err != nil {
		t.Fatalf("load through mount: %v", err)
	}
	if att.Content != "mounted" {
		t.Fatalf("unexpected content: %q", att.Content)
	}
}
