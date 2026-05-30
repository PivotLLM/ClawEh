package files

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// --- nextBackupSuffix unit tests ---------------------------------------------

type dirEntry struct {
	name  string
	isDir bool
}

func (d dirEntry) Name() string               { return d.name }
func (d dirEntry) IsDir() bool                { return d.isDir }
func (d dirEntry) Type() os.FileMode          { return 0 }
func (d dirEntry) Info() (os.FileInfo, error) { return nil, nil }

func entries(names ...string) []os.DirEntry {
	out := make([]os.DirEntry, 0, len(names))
	for _, n := range names {
		out = append(out, dirEntry{name: n})
	}
	return out
}

func TestNextBackupSuffix_NoSiblings(t *testing.T) {
	n, err := nextBackupSuffix("file.txt", entries("other.txt", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("got %d want 1", n)
	}
}

func TestNextBackupSuffix_GapsPreserved(t *testing.T) {
	n, err := nextBackupSuffix("file.txt", entries("file.txt.0001", "file.txt.0003"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("got %d want 4 (max+1, gaps preserved)", n)
	}
}

func TestNextBackupSuffix_IgnoresThreeDigitSuffix(t *testing.T) {
	// Only exactly 4 digits count.
	n, err := nextBackupSuffix("file.txt", entries("file.txt.001", "file.txt.99999"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("got %d want 1 (non-4-digit siblings ignored)", n)
	}
}

func TestNextBackupSuffix_IgnoresDirs(t *testing.T) {
	siblings := []os.DirEntry{
		dirEntry{name: "file.txt.0005", isDir: true},
		dirEntry{name: "file.txt.0002"},
	}
	n, err := nextBackupSuffix("file.txt", siblings)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("got %d want 3", n)
	}
}

func TestNextBackupSuffix_Exhausted(t *testing.T) {
	if _, err := nextBackupSuffix("file.txt", entries("file.txt.9999")); err == nil {
		t.Error("expected exhaustion error")
	}
}

func TestNextBackupSuffix_BaseWithRegexMeta(t *testing.T) {
	// QuoteMeta keeps "weird.name[1].txt" literal.
	n, err := nextBackupSuffix("weird.name[1].txt", entries(
		"weird.name[1].txt.0001",
		"weird.name[1].txt.0002",
	))
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("got %d want 3", n)
	}
}

func TestNextBackupSuffix_BaseAlreadySuffixedDoesNotCount(t *testing.T) {
	// File named "foo.0001" should NOT match its own pattern (would need "foo.0001.NNNN").
	n, err := nextBackupSuffix("foo.0001", entries("foo.0001"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("got %d want 1", n)
	}
}

func TestNextBackupSuffix_BaseWithNoExtension(t *testing.T) {
	n, err := nextBackupSuffix("notes", entries("notes.0001", "notes.0007"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Errorf("got %d want 8", n)
	}
}

// --- write_file backup integration tests -------------------------------------

func TestWriteFile_Backup_NonExistentTarget_NoSibling(t *testing.T) {
	ws := t.TempDir()
	tool := NewWriteFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":    "new.txt",
		"content": "hello",
		"backup":  true,
	})
	if res.IsError {
		t.Fatalf("write failed: %s", res.ForLLM)
	}
	got, err := os.ReadFile(filepath.Join(ws, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q", got)
	}
	matches, _ := filepath.Glob(filepath.Join(ws, "new.txt.*"))
	if len(matches) != 0 {
		t.Errorf("expected no backup sibling, got: %v", matches)
	}
}

func TestWriteFile_Backup_ExistingTarget_Creates0001PreservingMode(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "config.json")
	if err := os.WriteFile(target, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}

	tool := NewWriteFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":      "config.json",
		"content":   "replaced",
		"backup":    true,
		"overwrite": true,
	})
	if res.IsError {
		t.Fatalf("write failed: %s", res.ForLLM)
	}

	backup := filepath.Join(ws, "config.json.0001")
	got, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("backup not created: %v", err)
	}
	if string(got) != "original" {
		t.Errorf("backup content = %q, want %q", got, "original")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(backup)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Errorf("backup mode = %o, want 0640", info.Mode().Perm())
		}
	}

	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "replaced" {
		t.Errorf("target = %q, want %q", got, "replaced")
	}
}

func TestWriteFile_Backup_TwiceCreates0001Then0002(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "x.txt")
	if err := os.WriteFile(target, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewWriteFileTool(ws, true)

	res := tool.Execute(context.Background(), map[string]any{
		"path": "x.txt", "content": "v1", "backup": true, "overwrite": true,
	})
	if res.IsError {
		t.Fatalf("first write failed: %s", res.ForLLM)
	}
	res = tool.Execute(context.Background(), map[string]any{
		"path": "x.txt", "content": "v2", "backup": true, "overwrite": true,
	})
	if res.IsError {
		t.Fatalf("second write failed: %s", res.ForLLM)
	}

	b1, err := os.ReadFile(filepath.Join(ws, "x.txt.0001"))
	if err != nil {
		t.Fatalf(".0001 missing: %v", err)
	}
	if string(b1) != "v0" {
		t.Errorf(".0001 = %q want v0", b1)
	}
	b2, err := os.ReadFile(filepath.Join(ws, "x.txt.0002"))
	if err != nil {
		t.Fatalf(".0002 missing: %v", err)
	}
	if string(b2) != "v1" {
		t.Errorf(".0002 = %q want v1", b2)
	}
}

func TestWriteFile_Backup_GapPreserved(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "g.txt")
	if err := os.WriteFile(target, []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "g.txt.0001"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "g.txt.0003"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewWriteFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path": "g.txt", "content": "new", "backup": true, "overwrite": true,
	})
	if res.IsError {
		t.Fatalf("write failed: %s", res.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(ws, "g.txt.0004")); err != nil {
		t.Errorf("expected .0004 created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "g.txt.0002")); err == nil {
		t.Errorf("did not expect .0002 (gap should be preserved)")
	}
}

func TestWriteFile_Backup_False_Default_NoSibling(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "x.txt")
	if err := os.WriteFile(target, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewWriteFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":      "x.txt",
		"content":   "v1",
		"overwrite": true,
	})
	if res.IsError {
		t.Fatalf("write failed: %s", res.ForLLM)
	}
	matches, _ := filepath.Glob(filepath.Join(ws, "x.txt.*"))
	if len(matches) != 0 {
		t.Errorf("expected no backup sibling when backup=false; got: %v", matches)
	}
}

// --- edit_file backup integration tests --------------------------------------

func TestEditFile_Backup_NonExistentTarget(t *testing.T) {
	ws := t.TempDir()
	tool := NewEditFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":     "missing.txt",
		"old_text": "x",
		"new_text": "y",
		"backup":   true,
	})
	// edit_file requires the file to exist; backup is silently skipped, then edit fails.
	if !res.IsError {
		t.Fatal("expected edit to fail when target missing")
	}
	matches, _ := filepath.Glob(filepath.Join(ws, "missing.txt.*"))
	if len(matches) != 0 {
		t.Errorf("no backup expected: %v", matches)
	}
}

func TestEditFile_Backup_ExistingTarget(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "e.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":     "e.txt",
		"old_text": "world",
		"new_text": "there",
		"backup":   true,
	})
	if res.IsError {
		t.Fatalf("edit failed: %s", res.ForLLM)
	}
	b, err := os.ReadFile(filepath.Join(ws, "e.txt.0001"))
	if err != nil {
		t.Fatalf(".0001 missing: %v", err)
	}
	if string(b) != "hello world" {
		t.Errorf(".0001 = %q want %q", b, "hello world")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello there" {
		t.Errorf("target = %q", got)
	}
}

func TestEditFile_Backup_TwiceCreates0001Then0002(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "e.txt")
	if err := os.WriteFile(target, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path": "e.txt", "old_text": "a", "new_text": "b", "backup": true,
	})
	if res.IsError {
		t.Fatalf("first edit failed: %s", res.ForLLM)
	}
	res = tool.Execute(context.Background(), map[string]any{
		"path": "e.txt", "old_text": "b", "new_text": "c", "backup": true,
	})
	if res.IsError {
		t.Fatalf("second edit failed: %s", res.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(ws, "e.txt.0001")); err != nil {
		t.Errorf(".0001 missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "e.txt.0002")); err != nil {
		t.Errorf(".0002 missing: %v", err)
	}
}

// --- append_file backup integration tests ------------------------------------

func TestAppendFile_Backup_NonExistentTarget(t *testing.T) {
	ws := t.TempDir()
	tool := NewAppendFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":    "new.txt",
		"content": "hi",
		"backup":  true,
	})
	if res.IsError {
		t.Fatalf("append failed: %s", res.ForLLM)
	}
	matches, _ := filepath.Glob(filepath.Join(ws, "new.txt.*"))
	if len(matches) != 0 {
		t.Errorf("expected no backup for non-existent target; got: %v", matches)
	}
	got, _ := os.ReadFile(filepath.Join(ws, "new.txt"))
	if string(got) != "hi" {
		t.Errorf("target = %q", got)
	}
}

func TestAppendFile_Backup_ExistingTarget(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "a.txt")
	if err := os.WriteFile(target, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewAppendFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path": "a.txt", "content": "b", "backup": true,
	})
	if res.IsError {
		t.Fatalf("append failed: %s", res.ForLLM)
	}
	b1, err := os.ReadFile(filepath.Join(ws, "a.txt.0001"))
	if err != nil {
		t.Fatalf(".0001 missing: %v", err)
	}
	if string(b1) != "a" {
		t.Errorf(".0001 = %q", b1)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ab" {
		t.Errorf("target = %q want %q", got, "ab")
	}
}

func TestAppendFile_Backup_GapPreserved(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "g.txt")
	if err := os.WriteFile(target, []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "g.txt.0001"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "g.txt.0003"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewAppendFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path": "g.txt", "content": "y", "backup": true,
	})
	if res.IsError {
		t.Fatalf("append failed: %s", res.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(ws, "g.txt.0004")); err != nil {
		t.Errorf(".0004 missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "g.txt.0002")); err == nil {
		t.Errorf(".0002 should not be created (gap preserved)")
	}
}

// --- backup failure aborts modification --------------------------------------

func TestWriteFile_Backup_FailureAbortsModification(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write to read-only dirs")
	}
	ws := t.TempDir()
	subdir := filepath.Join(ws, "ro")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(subdir, "f.txt")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make subdir read-only so backup write fails.
	if err := os.Chmod(subdir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(subdir, 0o755)

	tool := NewWriteFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":      "ro/f.txt",
		"content":   "replaced",
		"backup":    true,
		"overwrite": true,
	})
	if !res.IsError {
		t.Fatal("expected error when backup write fails")
	}
	// Re-enable read so we can verify the target is unchanged.
	if err := os.Chmod(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("target should be unchanged after backup failure; got %q", got)
	}
}

// --- F3: backup must not be created when edit validation fails --------------

func TestEditFile_Backup_OldTextMissing_NoBackup(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "e.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":     "e.txt",
		"old_text": "absent",
		"new_text": "x",
		"backup":   true,
	})
	if !res.IsError {
		t.Fatal("expected edit to fail when old_text is missing")
	}
	matches, _ := filepath.Glob(filepath.Join(ws, "e.txt.*"))
	if len(matches) != 0 {
		t.Errorf("expected no orphan backup when validation fails; got: %v", matches)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Errorf("target should be unchanged; got %q", got)
	}
}

func TestEditFile_Backup_OldTextDuplicate_NoBackup(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "e.txt")
	if err := os.WriteFile(target, []byte("ab ab"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":     "e.txt",
		"old_text": "ab",
		"new_text": "z",
		"backup":   true,
	})
	if !res.IsError {
		t.Fatal("expected edit to fail when old_text is ambiguous")
	}
	matches, _ := filepath.Glob(filepath.Join(ws, "e.txt.*"))
	if len(matches) != 0 {
		t.Errorf("expected no orphan backup when validation fails; got: %v", matches)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ab ab" {
		t.Errorf("target should be unchanged; got %q", got)
	}
}

func TestEditFile_Backup_SuccessStillCreatesBackup(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "e.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":     "e.txt",
		"old_text": "world",
		"new_text": "there",
		"backup":   true,
	})
	if res.IsError {
		t.Fatalf("edit failed: %s", res.ForLLM)
	}
	b, err := os.ReadFile(filepath.Join(ws, "e.txt.0001"))
	if err != nil {
		t.Fatalf(".0001 missing: %v", err)
	}
	if string(b) != "hello world" {
		t.Errorf(".0001 = %q want %q", b, "hello world")
	}
}

// --- F4: concurrent backup under race detector -------------------------------

func TestWriteFile_Backup_Concurrent_DistinctSuffixes(t *testing.T) {
	ws := t.TempDir()
	target := filepath.Join(ws, "shared.txt")
	if err := os.WriteFile(target, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}

	const N = 8
	tool := NewWriteFileTool(ws, true)
	var wg sync.WaitGroup
	errs := make([]string, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := tool.Execute(context.Background(), map[string]any{
				"path":      "shared.txt",
				"content":   fmt.Sprintf("v%d", i+1),
				"backup":    true,
				"overwrite": true,
			})
			if res.IsError {
				errs[i] = res.ForLLM
			}
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != "" {
			t.Errorf("worker %d: %s", i, e)
		}
	}

	matches, err := filepath.Glob(filepath.Join(ws, "shared.txt.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != N {
		t.Fatalf("expected %d distinct backups, got %d: %v", N, len(matches), matches)
	}
	// All backups must hold a valid pre-image — one of the values the file
	// held at some point ("v0".."v8") — and never be empty.
	valid := map[string]bool{"v0": true}
	for i := 1; i <= N; i++ {
		valid[fmt.Sprintf("v%d", i)] = true
	}
	seenSuffixes := map[string]bool{}
	for _, m := range matches {
		body, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("read backup %s: %v", m, err)
		}
		if !valid[string(body)] {
			t.Errorf("backup %s holds unexpected pre-image %q", m, body)
		}
		if seenSuffixes[m] {
			t.Errorf("duplicate suffix observed: %s", m)
		}
		seenSuffixes[m] = true
	}
}

// --- N1: backup + memory redirect lands inside the memory sandbox ------------

func TestWriteFile_Backup_MemoryRedirect_BackupLandsInMemoryRoot(t *testing.T) {
	ws := t.TempDir()
	memRoot := t.TempDir()
	// Pre-create the target inside memRoot so write_file with memory/foo.md
	// is overwriting an existing file (and therefore triggers a backup).
	if err := os.WriteFile(filepath.Join(memRoot, "foo.md"), []byte("pre-image"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := NewWriteFileToolWithMemoryRedirect(ws, true, nil, memRoot)
	res := tool.Execute(context.Background(), map[string]any{
		"path":      "memory/foo.md",
		"content":   "post-image",
		"backup":    true,
		"overwrite": true,
	})
	if res.IsError {
		t.Fatalf("write failed: %s", res.ForLLM)
	}

	// Modification lands in memRoot, not workspace.
	got, err := os.ReadFile(filepath.Join(memRoot, "foo.md"))
	if err != nil {
		t.Fatalf("memRoot file missing: %v", err)
	}
	if string(got) != "post-image" {
		t.Errorf("memRoot file = %q want %q", got, "post-image")
	}

	// Backup lands in memRoot, not workspace.
	backup, err := os.ReadFile(filepath.Join(memRoot, "foo.md.0001"))
	if err != nil {
		t.Fatalf("memRoot backup missing: %v", err)
	}
	if string(backup) != "pre-image" {
		t.Errorf("backup = %q want %q", backup, "pre-image")
	}

	// Nothing should have been written under the workspace memory/ subtree.
	if _, err := os.Stat(filepath.Join(ws, "memory")); !os.IsNotExist(err) {
		t.Errorf("workspace memory/ should not exist; got err=%v", err)
	}
}

// --- scope confinement: target outside workspace still errors ---------------

func TestWriteFile_Backup_OutsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "x.txt")
	if err := os.WriteFile(target, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewWriteFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"path":      target,
		"content":   "new",
		"backup":    true,
		"overwrite": true,
	})
	if !res.IsError {
		t.Fatal("expected error for target outside workspace")
	}
	if _, err := os.Stat(target + ".0001"); !os.IsNotExist(err) {
		t.Errorf("backup must not escape scope; got: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "orig" {
		t.Errorf("target should be unchanged; got %q", got)
	}
}
