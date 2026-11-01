// ClawEh
// License: MIT

package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
)

// mkTree creates files (relative path -> content) under root.
func mkTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("%s should exist: %v", p, err)
	}
}

func mustNotExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist (err=%v)", p, err)
	}
}

func mustRead(t *testing.T, p, want string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil || string(b) != want {
		t.Fatalf("%s: got %q (err %v), want %q", p, string(b), err, want)
	}
}

func TestFileMkdir(t *testing.T) {
	dir := t.TempDir()
	mkTree(t, dir, map[string]string{"plain.txt": "x"})
	tool := NewMkdirToolScoped(dir, false, "")
	ctx := context.Background()

	cases := []struct {
		name      string
		path      string
		wantErr   bool
		wantInLLM string
	}{
		{"creates nested folders", filepath.Join(dir, "a", "b", "c"), false, "Folder created"},
		{"existing folder succeeds", filepath.Join(dir, "a", "b"), false, "already exists"},
		{"existing file is an error", filepath.Join(dir, "plain.txt"), true, "is a file"},
		{"missing path", "", true, "path is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tool.Execute(ctx, map[string]any{"path": tc.path})
			if res.IsError != tc.wantErr || !contains(res.ForLLM, tc.wantInLLM) {
				t.Fatalf("IsError=%v ForLLM=%q; want IsError=%v containing %q", res.IsError, res.ForLLM, tc.wantErr, tc.wantInLLM)
			}
			if !tc.wantErr && !res.Silent {
				t.Fatalf("success should be silent")
			}
		})
	}
	if info, err := os.Stat(filepath.Join(dir, "a", "b", "c")); err != nil || !info.IsDir() {
		t.Fatalf("nested folder not created: %v", err)
	}
}

func TestFileMkdir_SandboxConfinedToWriteSubdir(t *testing.T) {
	ws := t.TempDir()
	tool := NewMkdirToolScoped(ws, true, "files")
	ctx := context.Background()

	if res := tool.Execute(ctx, map[string]any{"path": "files/alice/notes"}); res.IsError {
		t.Fatalf("mkdir inside files/ failed: %s", res.ForLLM)
	}
	mustExist(t, filepath.Join(ws, "files", "alice", "notes"))

	for _, p := range []string{"skills/x", "../escape", filepath.Join(filepath.Dir(ws), "outside")} {
		res := tool.Execute(ctx, map[string]any{"path": p})
		if !res.IsError {
			t.Fatalf("mkdir %s outside files/ should be refused", p)
		}
		if !tools.IsExpectedRefusal(res.Err) {
			t.Fatalf("mkdir %s should be an expected refusal, got: %s", p, res.ForLLM)
		}
	}
	mustNotExist(t, filepath.Join(ws, "skills"))
	mustNotExist(t, filepath.Join(filepath.Dir(ws), "escape"))
	mustNotExist(t, filepath.Join(filepath.Dir(ws), "outside"))
}

func TestFileMkdir_Mounts(t *testing.T) {
	ws := t.TempDir()
	rw, ro := t.TempDir(), t.TempDir()
	SetMountsForWorkspace(ws, []MountSpec{{Name: "notes", Path: rw, Writable: true}, {Name: "ref", Path: ro}})
	defer SetMountsForWorkspace(ws, nil)
	tool := NewMkdirToolScoped(ws, true, "files")
	ctx := context.Background()

	if res := tool.Execute(ctx, map[string]any{"path": "notes/a/b"}); res.IsError {
		t.Fatalf("mkdir in a writable mount failed: %s", res.ForLLM)
	}
	mustExist(t, filepath.Join(rw, "a", "b"))

	res := tool.Execute(ctx, map[string]any{"path": "ref/a"})
	if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "read-only") {
		t.Fatalf("mkdir in a read-only mount should be an expected refusal, got: %s", res.ForLLM)
	}
	mustNotExist(t, filepath.Join(ro, "a"))
}

func TestFileRmdir(t *testing.T) {
	ctx := context.Background()

	t.Run("requires sure", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "empty")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		tool := NewRmdirToolScoped(dir, false, "")
		res := tool.Execute(ctx, map[string]any{"path": sub})
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "sure=true") {
			t.Fatalf("rmdir without sure should be refused, got: %s", res.ForLLM)
		}
		mustExist(t, sub)
	})

	t.Run("removes an empty folder", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "empty")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		tool := NewRmdirToolScoped(dir, false, "")
		if res := tool.Execute(ctx, map[string]any{"path": sub, "sure": true}); res.IsError || !res.Silent {
			t.Fatalf("rmdir empty folder failed: %s", res.ForLLM)
		}
		mustNotExist(t, sub)
	})

	t.Run("non-empty folder needs recursive", func(t *testing.T) {
		dir := t.TempDir()
		mkTree(t, dir, map[string]string{"full/a.txt": "a"})
		tool := NewRmdirToolScoped(dir, false, "")
		res := tool.Execute(ctx, map[string]any{"path": filepath.Join(dir, "full"), "sure": true})
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "recursive=true") {
			t.Fatalf("non-empty rmdir should be refused pointing at recursive, got: %s", res.ForLLM)
		}
		mustRead(t, filepath.Join(dir, "full", "a.txt"), "a")
	})

	t.Run("recursive removes the tree", func(t *testing.T) {
		dir := t.TempDir()
		mkTree(t, dir, map[string]string{"full/a.txt": "a", "full/sub/b.txt": "b", "full/sub/deeper/c.txt": "c"})
		if err := os.MkdirAll(filepath.Join(dir, "full", "emptysub"), 0o755); err != nil {
			t.Fatal(err)
		}
		tool := NewRmdirToolScoped(dir, false, "")
		res := tool.Execute(ctx, map[string]any{"path": filepath.Join(dir, "full"), "sure": true, "recursive": true})
		if res.IsError {
			t.Fatalf("recursive rmdir failed: %s", res.ForLLM)
		}
		mustNotExist(t, filepath.Join(dir, "full"))
	})

	t.Run("recursive refuses a tree holding a backup file", func(t *testing.T) {
		dir := t.TempDir()
		mkTree(t, dir, map[string]string{"full/a.txt": "a", "full/sub/notes.md.0002": "backup"})
		tool := NewRmdirToolScoped(dir, false, "")
		res := tool.Execute(ctx, map[string]any{"path": filepath.Join(dir, "full"), "sure": true, "recursive": true})
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "notes.md.0002") {
			t.Fatalf("should refuse naming the backup file, got: %s", res.ForLLM)
		}
		mustRead(t, filepath.Join(dir, "full", "a.txt"), "a")
		mustRead(t, filepath.Join(dir, "full", "sub", "notes.md.0002"), "backup")
	})

	t.Run("a file points at file_delete", func(t *testing.T) {
		dir, p := writeTemp(t, "data")
		tool := NewRmdirToolScoped(dir, false, "")
		res := tool.Execute(ctx, map[string]any{"path": p, "sure": true, "recursive": true})
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "file_delete") {
			t.Fatalf("rmdir of a file should point at file_delete, got: %s", res.ForLLM)
		}
		mustExist(t, p)
	})
}

func TestFileRmdir_SandboxRoots(t *testing.T) {
	ws := t.TempDir()
	mount := t.TempDir()
	mkTree(t, ws, map[string]string{"files/keep.txt": "k", "files/alice/a.txt": "a", "skills/s.md": "s"})
	mkTree(t, mount, map[string]string{"m.txt": "m"})
	SetMountsForWorkspace(ws, []MountSpec{{Name: "notes", Path: mount, Writable: true}})
	defer SetMountsForWorkspace(ws, nil)
	tool := NewRmdirToolScoped(ws, true, "files")
	ctx := context.Background()

	for _, p := range []string{"files", "files/", "./files", filepath.Join(ws, "files"), "notes", "notes/", "skills", ".", "..", filepath.Dir(ws)} {
		res := tool.Execute(ctx, map[string]any{"path": p, "sure": true, "recursive": true})
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) {
			t.Fatalf("rmdir %q should be an expected refusal, got: %s", p, res.ForLLM)
		}
	}
	mustExist(t, filepath.Join(ws, "files", "keep.txt"))
	mustExist(t, filepath.Join(ws, "skills", "s.md"))
	mustExist(t, filepath.Join(mount, "m.txt"))

	if res := tool.Execute(ctx, map[string]any{"path": "files/alice", "sure": true, "recursive": true}); res.IsError {
		t.Fatalf("rmdir inside files/ failed: %s", res.ForLLM)
	}
	mustNotExist(t, filepath.Join(ws, "files", "alice"))
}

func TestFileRmdir_WorkspaceRootWithoutWriteSubdir(t *testing.T) {
	ws := t.TempDir()
	mkTree(t, ws, map[string]string{"a.txt": "a"})
	tool := NewRmdirToolScoped(ws, true, "")
	for _, p := range []string{".", ws} {
		res := tool.Execute(context.Background(), map[string]any{"path": p, "sure": true, "recursive": true})
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) {
			t.Fatalf("rmdir of the workspace %q should be refused, got: %s", p, res.ForLLM)
		}
	}
	mustExist(t, filepath.Join(ws, "a.txt"))
}

func TestFileDelete_DirectoryPointsAtRmdir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	res := NewDeleteFileToolScoped(dir, false, "").Execute(context.Background(), map[string]any{"path": sub, "sure": true})
	if !res.IsError || !contains(res.ForLLM, "file_rmdir") {
		t.Fatalf("file_delete on a folder should point at file_rmdir, got: %s", res.ForLLM)
	}
}

func TestFileCopy_DirectoryMentionsMove(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	res := NewCopyFileToolScoped(dir, false, "").Execute(context.Background(),
		map[string]any{"source_path": sub, "destination_path": filepath.Join(dir, "other")})
	if !res.IsError || !contains(res.ForLLM, "file_move") {
		t.Fatalf("file_copy on a folder should mention file_move, got: %s", res.ForLLM)
	}
}

func TestFileMove_Folder(t *testing.T) {
	ctx := context.Background()

	t.Run("moves the tree", func(t *testing.T) {
		dir := t.TempDir()
		mkTree(t, dir, map[string]string{"src/a.txt": "a", "src/sub/b.txt": "b", "src/sub/notes.md.0001": "backup"})
		if err := os.MkdirAll(filepath.Join(dir, "src", "empty"), 0o755); err != nil {
			t.Fatal(err)
		}
		tool := NewMoveFileToolScoped(dir, false, "")
		dst := filepath.Join(dir, "new", "dst")
		res := tool.Execute(ctx, map[string]any{"source_path": filepath.Join(dir, "src"), "destination_path": dst})
		if res.IsError || !res.Silent {
			t.Fatalf("folder move failed: %s", res.ForLLM)
		}
		mustNotExist(t, filepath.Join(dir, "src"))
		mustRead(t, filepath.Join(dst, "a.txt"), "a")
		mustRead(t, filepath.Join(dst, "sub", "b.txt"), "b")
		mustRead(t, filepath.Join(dst, "sub", "notes.md.0001"), "backup")
		mustExist(t, filepath.Join(dst, "empty"))
	})

	t.Run("existing destination needs overwrite", func(t *testing.T) {
		dir := t.TempDir()
		mkTree(t, dir, map[string]string{"src/a.txt": "a", "dst/other.txt": "o"})
		tool := NewMoveFileToolScoped(dir, false, "")
		res := tool.Execute(ctx, map[string]any{"source_path": filepath.Join(dir, "src"), "destination_path": filepath.Join(dir, "dst")})
		if !res.IsError || !contains(res.ForLLM, "overwrite=true") {
			t.Fatalf("move onto an existing folder should need overwrite, got: %s", res.ForLLM)
		}
		mustRead(t, filepath.Join(dir, "src", "a.txt"), "a")
		mustNotExist(t, filepath.Join(dir, "dst", "a.txt"))
	})

	t.Run("overwrite merges into the destination", func(t *testing.T) {
		dir := t.TempDir()
		mkTree(t, dir, map[string]string{
			"src/a.txt": "new a", "src/sub/b.txt": "new b",
			"dst/a.txt": "old a", "dst/extra.txt": "extra",
		})
		tool := NewMoveFileToolScoped(dir, false, "")
		res := tool.Execute(ctx, map[string]any{"source_path": filepath.Join(dir, "src"), "destination_path": filepath.Join(dir, "dst"), "overwrite": true})
		if res.IsError {
			t.Fatalf("overwrite merge failed: %s", res.ForLLM)
		}
		mustNotExist(t, filepath.Join(dir, "src"))
		mustRead(t, filepath.Join(dir, "dst", "a.txt"), "new a")
		mustRead(t, filepath.Join(dir, "dst", "sub", "b.txt"), "new b")
		mustRead(t, filepath.Join(dir, "dst", "extra.txt"), "extra")
	})

	t.Run("overwrite refuses to replace a backup file", func(t *testing.T) {
		dir := t.TempDir()
		mkTree(t, dir, map[string]string{
			"src/a.txt": "a", "src/n.md.0001": "new",
			"dst/n.md.0001": "old backup",
		})
		tool := NewMoveFileToolScoped(dir, false, "")
		res := tool.Execute(ctx, map[string]any{"source_path": filepath.Join(dir, "src"), "destination_path": filepath.Join(dir, "dst"), "overwrite": true})
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "n.md.0001") {
			t.Fatalf("should refuse to overwrite a backup, got: %s", res.ForLLM)
		}
		mustRead(t, filepath.Join(dir, "dst", "n.md.0001"), "old backup")
		mustRead(t, filepath.Join(dir, "src", "a.txt"), "a")
		mustNotExist(t, filepath.Join(dir, "dst", "a.txt"))
	})

	t.Run("refuses moving into itself", func(t *testing.T) {
		dir := t.TempDir()
		mkTree(t, dir, map[string]string{"src/a.txt": "a"})
		tool := NewMoveFileToolScoped(dir, false, "")
		for _, d := range []string{filepath.Join(dir, "src"), filepath.Join(dir, "src", "inner"), filepath.Join(dir, "src", "x", "y")} {
			res := tool.Execute(ctx, map[string]any{"source_path": filepath.Join(dir, "src"), "destination_path": d, "overwrite": true})
			if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "into itself") {
				t.Fatalf("move into %s should be refused, got: %s", d, res.ForLLM)
			}
		}
		mustRead(t, filepath.Join(dir, "src", "a.txt"), "a")
		mustNotExist(t, filepath.Join(dir, "src", "inner"))
	})

	t.Run("refuses moving into itself with a relative source and an absolute destination", func(t *testing.T) {
		ws := t.TempDir()
		mkTree(t, ws, map[string]string{"files/src/a.txt": "a"})
		tool := NewMoveFileToolScoped(ws, true, "files")
		res := tool.Execute(ctx, map[string]any{"source_path": "files/src", "destination_path": filepath.Join(ws, "files", "src", "inner")})
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "into itself") {
			t.Fatalf("move into itself should be refused, got: %s", res.ForLLM)
		}
		mustRead(t, filepath.Join(ws, "files", "src", "a.txt"), "a")
		mustNotExist(t, filepath.Join(ws, "files", "src", "inner"))
	})

	t.Run("a sibling with a shared prefix is not a subtree", func(t *testing.T) {
		dir := t.TempDir()
		mkTree(t, dir, map[string]string{"src/a.txt": "a"})
		tool := NewMoveFileToolScoped(dir, false, "")
		res := tool.Execute(ctx, map[string]any{"source_path": filepath.Join(dir, "src"), "destination_path": filepath.Join(dir, "src2")})
		if res.IsError {
			t.Fatalf("move to a sibling failed: %s", res.ForLLM)
		}
		mustRead(t, filepath.Join(dir, "src2", "a.txt"), "a")
	})
}

func TestFileMove_FolderStopsMidwayAndReports(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files")
	}
	dir := t.TempDir()
	mkTree(t, dir, map[string]string{"src/a.txt": "a", "src/b.txt": "b", "src/c.txt": "c"})
	if err := os.Chmod(filepath.Join(dir, "src", "b.txt"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(filepath.Join(dir, "src", "b.txt"), 0o644); err != nil {
			t.Error(err)
		}
	})

	tool := NewMoveFileToolScoped(dir, false, "")
	dst := filepath.Join(dir, "dst")
	res := tool.Execute(context.Background(), map[string]any{"source_path": filepath.Join(dir, "src"), "destination_path": dst})
	if !res.IsError {
		t.Fatalf("move with an unreadable file should fail")
	}
	for _, want := range []string{"Moved 1 of 3 files", "a.txt", "Not moved", "b.txt, c.txt"} {
		if !contains(res.ForLLM, want) {
			t.Fatalf("error should contain %q, got: %s", want, res.ForLLM)
		}
	}
	mustRead(t, filepath.Join(dst, "a.txt"), "a")
	mustNotExist(t, filepath.Join(dir, "src", "a.txt"))
	mustExist(t, filepath.Join(dir, "src", "b.txt"))
	mustRead(t, filepath.Join(dir, "src", "c.txt"), "c")
}

// TestFileMove_FolderSandbox covers the full write-scoped, sandboxed stack: a
// folder move works when both ends are under files/, and is refused before
// anything is copied when either end is outside it.
func TestFileMove_FolderSandbox(t *testing.T) {
	ws := t.TempDir()
	mkTree(t, ws, map[string]string{
		"files/alice/a.txt": "a", "files/alice/sub/b.txt": "b",
		"skills/s/skill.md": "s",
	})
	tool := NewMoveFileToolScoped(ws, true, "files")
	ctx := context.Background()

	res := tool.Execute(ctx, map[string]any{"source_path": "files/alice", "destination_path": "files/archive/alice"})
	if res.IsError {
		t.Fatalf("folder move inside files/ failed: %s", res.ForLLM)
	}
	mustNotExist(t, filepath.Join(ws, "files", "alice"))
	mustRead(t, filepath.Join(ws, "files", "archive", "alice", "a.txt"), "a")
	mustRead(t, filepath.Join(ws, "files", "archive", "alice", "sub", "b.txt"), "b")

	cases := []struct{ src, dst string }{
		{"skills/s", "files/s"},             // source outside the write scope
		{"files/archive", "skills/archive"}, // destination outside the write scope
		{"files/archive", "../escape"},      // destination outside the workspace
		{"files", "files2"},                 // the write folder itself
	}
	for _, tc := range cases {
		res := tool.Execute(ctx, map[string]any{"source_path": tc.src, "destination_path": tc.dst})
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) {
			t.Fatalf("move %s -> %s should be an expected refusal, got: %s", tc.src, tc.dst, res.ForLLM)
		}
	}
	mustRead(t, filepath.Join(ws, "skills", "s", "skill.md"), "s")
	mustNotExist(t, filepath.Join(ws, "files", "s"))
	mustNotExist(t, filepath.Join(ws, "skills", "archive"))
	mustNotExist(t, filepath.Join(filepath.Dir(ws), "escape"))
	mustNotExist(t, filepath.Join(ws, "files2"))
	mustRead(t, filepath.Join(ws, "files", "archive", "alice", "a.txt"), "a")
}

// TestFileMove_FolderAcrossMount moves a folder from the write folder into a
// writable mount (a different sandbox root), and refuses one into a read-only
// mount before copying anything.
func TestFileMove_FolderAcrossMount(t *testing.T) {
	ws := t.TempDir()
	rw, ro := t.TempDir(), t.TempDir()
	mkTree(t, ws, map[string]string{"files/bob/a.txt": "a", "files/bob/sub/b.txt": "b"})
	SetMountsForWorkspace(ws, []MountSpec{{Name: "notes", Path: rw, Writable: true}, {Name: "ref", Path: ro}})
	defer SetMountsForWorkspace(ws, nil)
	tool := NewMoveFileToolScoped(ws, true, "files")
	ctx := context.Background()

	res := tool.Execute(ctx, map[string]any{"source_path": "files/bob", "destination_path": "ref/bob"})
	if !res.IsError || !tools.IsExpectedRefusal(res.Err) {
		t.Fatalf("move into a read-only mount should be refused, got: %s", res.ForLLM)
	}
	mustRead(t, filepath.Join(ws, "files", "bob", "a.txt"), "a")
	mustNotExist(t, filepath.Join(ro, "bob"))

	res = tool.Execute(ctx, map[string]any{"source_path": "files/bob", "destination_path": "notes/bob"})
	if res.IsError {
		t.Fatalf("move into a writable mount failed: %s", res.ForLLM)
	}
	mustNotExist(t, filepath.Join(ws, "files", "bob"))
	mustRead(t, filepath.Join(rw, "bob", "a.txt"), "a")
	mustRead(t, filepath.Join(rw, "bob", "sub", "b.txt"), "b")

	res = tool.Execute(ctx, map[string]any{"source_path": "notes", "destination_path": "files/notes"})
	if !res.IsError || !tools.IsExpectedRefusal(res.Err) {
		t.Fatalf("moving a mount root should be refused, got: %s", res.ForLLM)
	}
	mustExist(t, filepath.Join(rw, "bob", "a.txt"))
}
