// ClawEh
// License: MIT

package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileDelete_RequiresSure(t *testing.T) {
	dir, p := writeTemp(t, "data")
	tool := NewDeleteFileToolScoped(dir, false, "")
	// Without sure → refused, file remains.
	if res := tool.Execute(context.Background(), map[string]any{"path": p}); !res.IsError {
		t.Fatalf("delete without sure should be refused")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("file should still exist after refused delete")
	}
	// With sure=true → deleted.
	if res := tool.Execute(context.Background(), map[string]any{"path": p, "sure": true}); res.IsError {
		t.Fatalf("delete with sure failed: %s", res.ForLLM)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("file should be gone after delete")
	}
}

func TestFileDelete_RefusesBackupFiles(t *testing.T) {
	dir := t.TempDir()
	bp := filepath.Join(dir, "notes.md.0001")
	os.WriteFile(bp, []byte("backup"), 0o644)
	tool := NewDeleteFileToolScoped(dir, false, "")
	res := tool.Execute(context.Background(), map[string]any{"path": bp, "sure": true})
	if !res.IsError || !contains(res.ForLLM, "backup") {
		t.Fatalf("should refuse to delete a backup file, got: %s", res.ForLLM)
	}
	if _, err := os.Stat(bp); err != nil {
		t.Fatalf("backup file must survive")
	}
}

func TestFileMove_CopyThenDelete(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	dst := filepath.Join(dir, "sub", "b.txt")
	os.WriteFile(src, []byte("hello"), 0o644)
	tool := NewMoveFileToolScoped(dir, false, "")
	res := tool.Execute(context.Background(), map[string]any{"source_path": src, "destination_path": dst})
	if res.IsError {
		t.Fatalf("move failed: %s", res.ForLLM)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source should be gone after move")
	}
	b, err := os.ReadFile(dst)
	if err != nil || string(b) != "hello" {
		t.Fatalf("destination missing/wrong: %v %q", err, string(b))
	}
}

func TestFileMove_NoOverwriteByDefault(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	dst := filepath.Join(dir, "b.txt")
	os.WriteFile(src, []byte("new"), 0o644)
	os.WriteFile(dst, []byte("existing"), 0o644)
	tool := NewMoveFileToolScoped(dir, false, "")
	res := tool.Execute(context.Background(), map[string]any{"source_path": src, "destination_path": dst})
	if !res.IsError {
		t.Fatalf("move onto existing dest should fail without overwrite")
	}
	// Source must remain (copy failed before delete).
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source must remain when move is refused")
	}
}
