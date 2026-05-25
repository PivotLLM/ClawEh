package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCopyFileTool_Metadata(t *testing.T) {
	tool := NewCopyFileTool("", false)
	if tool.Name() != "copy_file" {
		t.Errorf("Name() = %q, want copy_file", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("Parameters().properties should be a map")
	}
	for _, key := range []string{"source_path", "destination_path", "overwrite", "display"} {
		if _, ok := props[key]; !ok {
			t.Errorf("Parameters() missing %q", key)
		}
	}
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("Parameters().required should be a []string")
	}
	if len(required) != 2 || required[0] != "source_path" || required[1] != "destination_path" {
		t.Errorf("required = %v, want [source_path destination_path]", required)
	}
}

func TestCopyFileTool_HappyPath_PreservesMode(t *testing.T) {
	ws := t.TempDir()
	srcPath := filepath.Join(ws, "src.txt")
	if err := os.WriteFile(srcPath, []byte("contents"), 0o640); err != nil {
		t.Fatal(err)
	}
	// Force the mode independently of umask.
	if err := os.Chmod(srcPath, 0o640); err != nil {
		t.Fatal(err)
	}

	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "src.txt",
		"destination_path": "dst.txt",
	})
	if res.IsError {
		t.Fatalf("copy failed: %s", res.ForLLM)
	}

	dstPath := filepath.Join(ws, "dst.txt")
	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "contents" {
		t.Errorf("dst contents = %q, want %q", got, "contents")
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(dstPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Errorf("dst mode = %o, want 0640", info.Mode().Perm())
		}
	}
}

func TestCopyFileTool_OverwriteFalse_ErrorsWhenDstExists(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "src.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "dst.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "src.txt",
		"destination_path": "dst.txt",
	})
	if !res.IsError {
		t.Fatal("expected error when destination exists and overwrite=false")
	}
	if !strings.Contains(res.ForLLM, "exists") {
		t.Errorf("error should mention dst exists; got: %s", res.ForLLM)
	}
	got, err := os.ReadFile(filepath.Join(ws, "dst.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Errorf("dst unexpectedly modified: %q", got)
	}
}

func TestCopyFileTool_OverwriteTrue_ReplacesDst(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "src.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "dst.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "src.txt",
		"destination_path": "dst.txt",
		"overwrite":        true,
	})
	if res.IsError {
		t.Fatalf("copy failed: %s", res.ForLLM)
	}
	got, err := os.ReadFile(filepath.Join(ws, "dst.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("dst = %q, want %q", got, "new")
	}
}

func TestCopyFileTool_SourceMissing(t *testing.T) {
	ws := t.TempDir()
	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "ghost.txt",
		"destination_path": "dst.txt",
	})
	if !res.IsError {
		t.Fatal("expected error for missing source")
	}
	if !strings.Contains(res.ForLLM, "not found") {
		t.Errorf("error should mention not found; got: %s", res.ForLLM)
	}
}

func TestCopyFileTool_DestinationIsDirectory(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "src.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, "dst-dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "src.txt",
		"destination_path": "dst-dir",
		"overwrite":        true,
	})
	if !res.IsError {
		t.Fatal("expected error when dst resolves to a directory")
	}
	if !strings.Contains(res.ForLLM, "directory") {
		t.Errorf("error should mention directory; got: %s", res.ForLLM)
	}
}

func TestCopyFileTool_SourceIsDirectory(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "subdir",
		"destination_path": "dst.txt",
	})
	if !res.IsError {
		t.Fatal("expected error when source is a directory")
	}
	if !strings.Contains(res.ForLLM, "directory") {
		t.Errorf("error should mention directory; got: %s", res.ForLLM)
	}
}

func TestCopyFileTool_ScopeConfinement_SourceOutsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "src.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      filepath.Join(outside, "src.txt"),
		"destination_path": "dst.txt",
	})
	if !res.IsError {
		t.Fatal("expected error for source outside workspace")
	}
}

func TestCopyFileTool_ScopeConfinement_DestinationOutsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "src.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "src.txt",
		"destination_path": filepath.Join(outside, "dst.txt"),
	})
	if !res.IsError {
		t.Fatal("expected error for destination outside workspace")
	}
	if _, err := os.Stat(filepath.Join(outside, "dst.txt")); !os.IsNotExist(err) {
		t.Errorf("dst should not exist outside workspace: %v", err)
	}
}

func TestCopyFileTool_Display_True(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "src.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "src.txt",
		"destination_path": "dst.txt",
		"display":          true,
	})
	if res.IsError {
		t.Fatalf("copy failed: %s", res.ForLLM)
	}
	want := "---\nhello\n---"
	if res.ForUser != want {
		t.Errorf("ForUser = %q, want %q", res.ForUser, want)
	}
}

func TestCopyFileTool_Display_False_NoBlock(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "src.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "src.txt",
		"destination_path": "dst.txt",
	})
	if res.IsError {
		t.Fatalf("copy failed: %s", res.ForLLM)
	}
	if !res.Silent {
		t.Error("expected Silent result when display=false")
	}
	if res.ForUser != "" {
		t.Errorf("ForUser should be empty when display=false; got %q", res.ForUser)
	}
}

func TestCopyFileTool_MemoryRedirect_CrossSandbox(t *testing.T) {
	ws := t.TempDir()
	memRoot := t.TempDir()
	// Place a file in mem root so memory/src.txt resolves there.
	if err := os.WriteFile(filepath.Join(memRoot, "src.txt"), []byte("from memory"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewCopyFileToolWithMemoryRedirect(ws, true, nil, memRoot)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "memory/src.txt",
		"destination_path": "dst.txt",
	})
	if res.IsError {
		t.Fatalf("cross-sandbox copy failed: %s", res.ForLLM)
	}
	got, err := os.ReadFile(filepath.Join(ws, "dst.txt"))
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "from memory" {
		t.Errorf("dst = %q, want %q", got, "from memory")
	}
	// Original memory file unchanged.
	got, err = os.ReadFile(filepath.Join(memRoot, "src.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from memory" {
		t.Errorf("source memory file changed: %q", got)
	}
}

func TestCopyFileTool_SameSourceDestination_OverwriteFalse(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "f.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "f.txt",
		"destination_path": "f.txt",
	})
	if !res.IsError {
		t.Fatal("expected error when source and destination resolve to the same file")
	}
	if !strings.Contains(res.ForLLM, "same file") {
		t.Errorf("error should mention same file; got: %s", res.ForLLM)
	}
	got, err := os.ReadFile(filepath.Join(ws, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("file should be unchanged; got %q", got)
	}
}

func TestCopyFileTool_SameSourceDestination_OverwriteTrue(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "f.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "f.txt",
		"destination_path": "f.txt",
		"overwrite":        true,
	})
	if !res.IsError {
		t.Fatal("expected error when source and destination resolve to the same file even with overwrite=true")
	}
	if !strings.Contains(res.ForLLM, "same file") {
		t.Errorf("error should mention same file; got: %s", res.ForLLM)
	}
	got, err := os.ReadFile(filepath.Join(ws, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("file should be unchanged; got %q", got)
	}
}

func TestCopyFileTool_SameSourceDestination_PathClean(t *testing.T) {
	// Differs textually but resolves to the same path via filepath.Clean.
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "sub", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewCopyFileTool(ws, true)
	res := tool.Execute(context.Background(), map[string]any{
		"source_path":      "sub/f.txt",
		"destination_path": "sub/./f.txt",
		"overwrite":        true,
	})
	if !res.IsError {
		t.Fatal("expected error when source and destination resolve to the same file via clean")
	}
}

func TestCopyFileTool_MissingArgs(t *testing.T) {
	tool := NewCopyFileTool(t.TempDir(), false)
	cases := []map[string]any{
		{"destination_path": "x"},
		{"source_path": "x"},
		{"source_path": "", "destination_path": "x"},
		{"source_path": "x", "destination_path": ""},
	}
	for i, args := range cases {
		res := tool.Execute(context.Background(), args)
		if !res.IsError {
			t.Errorf("case %d: expected error for args %v", i, args)
		}
	}
}
