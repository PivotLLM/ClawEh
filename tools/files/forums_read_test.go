package files

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestEffectiveReadSubdirs_AddsAlwaysReadable checks that a configured read
// scope always gains tasks/, tmp/ and forums/, without duplicates, and that an
// empty scope (workspace-wide reads) is left empty.
func TestEffectiveReadSubdirs_AddsAlwaysReadable(t *testing.T) {
	tests := []struct {
		name       string
		configured []string
		want       []string
	}{
		{name: "default scope", configured: []string{"files", "skills"}, want: []string{"files", "skills", "tasks", "tmp", "forums"}},
		{name: "already listed", configured: []string{"files", "forums"}, want: []string{"files", "forums", "tasks", "tmp"}},
		{name: "workspace-wide", configured: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveReadSubdirs(tt.configured)
			if !slices.Equal(got, tt.want) {
				t.Errorf("effectiveReadSubdirs(%v) = %v, want %v", tt.configured, got, tt.want)
			}
		})
	}
}

// TestForumsFolder_ReadOnlyForOwnAgent covers the launching agent's access to
// its own forums/ folder: its file tools may read the results there, may not
// write there, and cannot reach another agent's forums by a relative path, an
// absolute path or a mount.
func TestForumsFolder_ReadOnlyForOwnAgent(t *testing.T) {
	base := t.TempDir()
	alice := filepath.Join(base, "alice")
	bob := filepath.Join(base, "bob")
	notes := filepath.Join(base, "notes")
	mustWrite(t, filepath.Join(alice, "forums", "f1", "transcript.md"), "alice transcript")
	mustWrite(t, filepath.Join(bob, "forums", "f2", "transcript.md"), "bob transcript")
	mustWrite(t, filepath.Join(notes, "readme.md"), "notes")

	SetReadScopeSubdirs(effectiveReadSubdirs([]string{"files", "skills"}))
	defer SetReadScopeSubdirs(nil)
	SetMountsForWorkspace(alice, []MountSpec{{Name: "notes", Path: notes}})
	defer SetMountsForWorkspace(alice, nil)

	ctx := context.Background()
	read := NewReadFileTool(alice, true, MaxReadFileSize)
	list := NewListDirTool(alice, true)
	write := NewWriteFileToolScoped(alice, true, "files")

	if r := read.Execute(ctx, map[string]any{"path": "forums/f1/transcript.md"}); r.IsError {
		t.Fatalf("own forum transcript should be readable: %s", r.ForLLM)
	}
	if r := list.Execute(ctx, map[string]any{"path": "forums"}); r.IsError {
		t.Fatalf("own forums folder should be listable: %s", r.ForLLM)
	}

	if r := write.Execute(ctx, map[string]any{"path": "forums/f1/transcript.md", "content": "tampered"}); !r.IsError {
		t.Fatal("writing into forums/ should be refused")
	}
	if r := write.Execute(ctx, map[string]any{"path": "forums/f1/new.md", "content": "x"}); !r.IsError {
		t.Fatal("creating a file in forums/ should be refused")
	}
	if b, err := os.ReadFile(filepath.Join(alice, "forums", "f1", "transcript.md")); err != nil || string(b) != "alice transcript" {
		t.Fatalf("transcript changed: %q err=%v", b, err)
	}

	for _, p := range []string{
		"../bob/forums/f2/transcript.md",
		filepath.Join(bob, "forums", "f2", "transcript.md"),
		"forums/../../bob/forums/f2/transcript.md",
		"notes/../bob/forums/f2/transcript.md",
	} {
		if r := read.Execute(ctx, map[string]any{"path": p}); !r.IsError {
			t.Errorf("another agent's forum must not be readable via %q", p)
		}
	}
}
