package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
)

// readOnlyFixture is a workspace whose writes are confined to files/, with a
// file in forums/ (readable, not writable) holding a repeated line.
func readOnlyFixture(t *testing.T) (workspace, forumFile string) {
	t.Helper()
	workspace = t.TempDir()
	for _, d := range []string{"files", "forums/f1"} {
		if err := os.MkdirAll(filepath.Join(workspace, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	forumFile = filepath.Join(workspace, "forums", "f1", "transcript.md")
	if err := os.WriteFile(forumFile, []byte("same\nsame\nsame\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return workspace, forumFile
}

func assertWriteRefused(t *testing.T, res *tools.ToolResult) {
	t.Helper()
	if !res.IsError || !strings.HasPrefix(res.ForLLM, "write denied") {
		t.Fatalf("result = %q (error %v), want the write-denied refusal", res.ForLLM, res.IsError)
	}
	if !tools.IsExpectedRefusal(res.Err) {
		t.Errorf("write denial is not marked as a refusal: %v", res.Err)
	}
}

func assertUnchanged(t *testing.T, workspace, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "same\nsame\nsame\n" {
		t.Errorf("file changed: %q", data)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("forum folder holds %d entries, want only the original (no backup)", len(entries))
	}
}

// A move out of a read-only folder is refused before anything is copied.
func TestMoveFromReadOnlyFolderRefusedWithoutCopy(t *testing.T) {
	workspace, forumFile := readOnlyFixture(t)
	tool := NewMoveFileToolScoped(workspace, true, "files")
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "forums/f1/transcript.md",
		"destination_path": "files/t.md",
	})
	assertWriteRefused(t, res)
	if _, err := os.Stat(filepath.Join(workspace, "files", "t.md")); !os.IsNotExist(err) {
		t.Errorf("a refused move left a copy behind (stat err %v)", err)
	}
	assertUnchanged(t, workspace, forumFile)
}

// file_edit on a read-only file is refused as a write denial even when the
// old text would not match uniquely, and writes no backup.
func TestEditReadOnlyFileRefusedBeforeMatch(t *testing.T) {
	workspace, forumFile := readOnlyFixture(t)
	tool := NewEditFileToolScoped(workspace, true, "files")
	res := tool.Execute(context.Background(), map[string]any{
		"path": "forums/f1/transcript.md", "old_text": "same", "new_text": "x", "backup": true,
	})
	assertWriteRefused(t, res)
	assertUnchanged(t, workspace, forumFile)
}

// file_edit_lines on a read-only file is refused as a write denial, not as a
// failed backup.
func TestEditLinesReadOnlyFileRefusedBeforeBackup(t *testing.T) {
	workspace, forumFile := readOnlyFixture(t)
	tool := newRangeEditTool("edit", "lines", workspace, true, "files")
	res := tool.Execute(context.Background(), map[string]any{
		"path": "forums/f1/transcript.md", "start_line": float64(1), "end_line": float64(1), "content": "x",
	})
	assertWriteRefused(t, res)
	assertUnchanged(t, workspace, forumFile)
}

// file_append to a read-only file is refused before any backup.
func TestAppendReadOnlyFileRefusedBeforeBackup(t *testing.T) {
	workspace, forumFile := readOnlyFixture(t)
	tool := NewAppendFileToolScoped(workspace, true, "files")
	res := tool.Execute(context.Background(), map[string]any{
		"path": "forums/f1/transcript.md", "content": "x", "backup": true,
	})
	assertWriteRefused(t, res)
	assertUnchanged(t, workspace, forumFile)
}

// A move inside the writable folder still works.
func TestMoveInsideWritableFolder(t *testing.T) {
	workspace, _ := readOnlyFixture(t)
	if err := os.WriteFile(filepath.Join(workspace, "files", "a.md"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewMoveFileToolScoped(workspace, true, "files")
	res := tool.Execute(context.Background(), map[string]any{
		"source_path": "files/a.md", "destination_path": "files/b.md",
	})
	if res.IsError {
		t.Fatalf("move failed: %s", res.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(workspace, "files", "a.md")); !os.IsNotExist(err) {
		t.Errorf("source still present after move")
	}
}

// A read outside the read scope is a refusal; a missing file is a failure.
func TestReadDenialIsRefusal(t *testing.T) {
	SetReadScopeSubdirs([]string{"files"})
	defer SetReadScopeSubdirs(nil)
	workspace, _ := readOnlyFixture(t)
	tool := NewReadLinesTool(workspace, true, 0)

	denied := tool.Execute(context.Background(), map[string]any{"path": "forums/f1/transcript.md"})
	if !denied.IsError || !strings.Contains(denied.ForLLM, "read denied") || !tools.IsExpectedRefusal(denied.Err) {
		t.Fatalf("denied read = %q (refusal %v), want a read-denied refusal", denied.ForLLM, tools.IsExpectedRefusal(denied.Err))
	}
	missing := tool.Execute(context.Background(), map[string]any{"path": "files/nope.md"})
	if !missing.IsError || tools.IsExpectedRefusal(missing.Err) {
		t.Fatalf("missing file = %q (refusal %v), want a failure that is not a refusal", missing.ForLLM, tools.IsExpectedRefusal(missing.Err))
	}
}

// file_delete without sure=true is a refusal.
func TestDeleteWithoutSureIsRefusal(t *testing.T) {
	workspace, _ := readOnlyFixture(t)
	tool := NewDeleteFileToolScoped(workspace, true, "files")
	res := tool.Execute(context.Background(), map[string]any{"path": "files/x.md"})
	if !res.IsError || !tools.IsExpectedRefusal(res.Err) {
		t.Fatalf("result = %q (refusal %v), want a refusal", res.ForLLM, tools.IsExpectedRefusal(res.Err))
	}
}
