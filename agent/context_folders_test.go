// ClawEh
// License: MIT

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

func TestAccessibleFolders(t *testing.T) {
	cb := &ContextBuilder{}
	const ws = "/srv/agents/alice"
	if got, want := cb.accessibleFolders(ws), "- /srv/agents/alice/files (read/write)\n- /srv/agents/alice/skills (read-only)"; got != want {
		t.Fatalf("base folders = %q, want %q", got, want)
	}

	// Allowed folders are listed by their real path; one the account cannot
	// write is marked read-only; a note follows the folder; a missing one is
	// left out. Maestro is part of the workspace.
	base := t.TempDir()
	src := filepath.Join(base, "src")
	ref := filepath.Join(base, "ref")
	for _, d := range []string{src, ref} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	readOnly := os.Geteuid() != 0
	if readOnly {
		if err := os.Chmod(ref, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(ref, 0o700); err != nil {
				t.Error(err)
			}
		})
	}
	cb.allowedFolders = []config.AllowedFolderConfig{
		{Path: src},
		{Path: ref, Note: "inbound audio notes"},
		{Path: filepath.Join(base, "missing"), Note: "never shown"},
	}
	cb.maestroEnabled = true
	refLine := ref
	if readOnly {
		refLine += " (read-only)"
	}
	refLine += ": inbound audio notes"
	want := strings.Join([]string{
		"- /srv/agents/alice/files (read/write)",
		"- /srv/agents/alice/skills (read-only)",
		"- /srv/agents/alice/maestro (read/write)",
		"- " + src,
		"- " + refLine,
	}, "\n")
	if got := cb.accessibleFolders(ws); got != want {
		t.Fatalf("with folders = %q, want %q", got, want)
	}
}

// The workspace section says where relative paths start and where work is
// written, and lists the folders by absolute path; the old "working area"
// wording is gone.
func TestIdentity_WorkspaceSection(t *testing.T) {
	ws := t.TempDir()
	notes := filepath.Join(ws, "..", filepath.Base(ws)+"-notes")
	if err := os.Mkdir(notes, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(notes); err != nil {
			t.Error(err)
		}
	})
	notes = filepath.Clean(notes)
	got := NewContextBuilder(ws).
		WithAllowedFolders([]config.AllowedFolderConfig{{Path: notes, Note: "meeting notes"}}).
		getIdentity()
	abs, err := filepath.Abs(ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Your workspace is " + abs + ". Relative paths start there; write your work under files/.",
		"Folders your file tools can reach by absolute path:\n- " + abs + "/files (read/write)\n- " + abs + "/skills (read-only)\n- " + notes,
		notes + ": meeting notes\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("identity lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "working area") {
		t.Errorf("identity still uses the working area wording:\n%s", got)
	}
}

// A note is shown on one line: newlines and other control characters become
// spaces, and it is cut to 100 characters, whatever the file holds.
func TestPromptNote(t *testing.T) {
	for in, want := range map[string]string{
		"inbound audio notes":             "inbound audio notes",
		"line one\nline two\r\n- /etc":    "line one line two  - /etc",
		"tab\there\x00nul\x1besc":         "tab here nul esc",
		"  \n trimmed \n ":                "trimmed",
		strings.Repeat("é", 150):          strings.Repeat("é", 100),
		strings.Repeat("a", 99) + "\nbcd": strings.Repeat("a", 99),
	} {
		if got := promptNote(in); got != want {
			t.Errorf("promptNote(%q) = %q, want %q", in, got, want)
		}
	}
	dir := t.TempDir()
	cb := &ContextBuilder{allowedFolders: []config.AllowedFolderConfig{{Path: dir, Note: "a\n- /etc (read/write)"}}}
	got := cb.accessibleFolders("/srv/agents/alice")
	if strings.Count(got, "\n") != 2 || !strings.HasSuffix(got, dir+": a - /etc (read/write)") {
		t.Errorf("a note broke the folder list:\n%s", got)
	}
}
