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

// linkedWorkspace makes a workspace with files/ and skills/s/skill.md, and the
// link files/link -> ../skills, so the link's target is outside files/.
func linkedWorkspace(t *testing.T) (ws, skill string) {
	t.Helper()
	ws = t.TempDir()
	mkTree(t, ws, map[string]string{"skills/s/skill.md": "skill", "files/keep.txt": "k"})
	if err := os.Symlink(filepath.Join("..", "skills"), filepath.Join(ws, "files", "link")); err != nil {
		t.Fatal(err)
	}
	return ws, filepath.Join(ws, "skills", "s", "skill.md")
}

func TestWriteScoped_LinkOutOfWriteFolderRefused(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func(ws string) *tools.ToolResult
	}{
		{"file_write through the link", func(ws string) *tools.ToolResult {
			return NewWriteFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link/x", "content": "x"})
		}},
		{"file_write over a file through the link", func(ws string) *tools.ToolResult {
			return NewWriteFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link/s/skill.md", "content": "x", "overwrite": true})
		}},
		{"file_delete through the link", func(ws string) *tools.ToolResult {
			return NewDeleteFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link/s/skill.md", "sure": true})
		}},
		{"file_rmdir recursive below the link", func(ws string) *tools.ToolResult {
			return NewRmdirToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link/s", "sure": true, "recursive": true})
		}},
		{"file_mkdir through the link", func(ws string) *tools.ToolResult {
			return NewMkdirToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link/new"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, skill := linkedWorkspace(t)
			res := tc.run(ws)
			if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "resolves outside") {
				t.Fatalf("want a refusal naming the link, got IsError=%v: %s", res.IsError, res.ForLLM)
			}
			mustRead(t, skill, "skill")
			mustNotExist(t, filepath.Join(ws, "skills", "x"))
			mustNotExist(t, filepath.Join(ws, "skills", "new"))
			mustNotExist(t, filepath.Join(ws, "files", "moved"))
		})
	}
}

func TestWriteScoped_LinkInsideWriteFolderAllowed(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	mkTree(t, ws, map[string]string{"files/real/a.txt": "a"})
	link := filepath.Join(ws, "files", "alias")
	if err := os.Symlink("real", link); err != nil {
		t.Fatal(err)
	}

	res := NewWriteFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/alias/b.txt", "content": "b"})
	if res.IsError {
		t.Fatalf("write through a link inside files/: %s", res.ForLLM)
	}
	mustRead(t, filepath.Join(ws, "files", "real", "b.txt"), "b")

	res = NewRmdirToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/alias", "sure": true, "recursive": true})
	if res.IsError || !contains(res.ForLLM, "Link removed") {
		t.Fatalf("rmdir on a link: IsError=%v: %s", res.IsError, res.ForLLM)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("link should be gone (err=%v)", err)
	}
	mustRead(t, filepath.Join(ws, "files", "real", "a.txt"), "a")
	mustRead(t, filepath.Join(ws, "files", "real", "b.txt"), "b")
}

func TestWriteScoped_LinkLoopRefused(t *testing.T) {
	ws := t.TempDir()
	mkTree(t, ws, map[string]string{"files/keep.txt": "k"})
	for _, l := range [][2]string{{"b", "a"}, {"a", "b"}} {
		if err := os.Symlink(l[0], filepath.Join(ws, "files", l[1])); err != nil {
			t.Fatal(err)
		}
	}
	res := NewWriteFileToolScoped(ws, true, "files").Execute(context.Background(), map[string]any{"path": "files/a/x", "content": "x"})
	if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "resolves outside") {
		t.Fatalf("want a refusal for a link loop, got IsError=%v: %s", res.IsError, res.ForLLM)
	}
}

// A ".." in a link target applies where the link before it leads, and a ".."
// after a missing folder applies once MkdirAll has created that folder; neither
// may be cleaned away lexically.
func TestWriteScoped_LinkTargetDotDotRefused(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		links [][2]string // target, link name under files/
	}{
		{"dot-dot after a link", [][2]string{{"../skills/s", "l3"}, {"l3/..", "l"}}},
		{"dot-dot after a missing folder", [][2]string{{"../skills", "l2"}, {"missing/../l2", "l"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			mkTree(t, ws, map[string]string{"skills/s/skill.md": "skill", "files/keep.txt": "k"})
			for _, l := range tc.links {
				if err := os.Symlink(l[0], filepath.Join(ws, "files", l[1])); err != nil {
					t.Fatal(err)
				}
			}
			for _, res := range []*tools.ToolResult{
				NewMkdirToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/l/new"}),
				NewWriteFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/l/x", "content": "x"}),
			} {
				if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "resolves outside") {
					t.Fatalf("want a refusal, got IsError=%v: %s", res.IsError, res.ForLLM)
				}
			}
			mustNotExist(t, filepath.Join(ws, "skills", "x"))
			mustNotExist(t, filepath.Join(ws, "skills", "new"))
		})
	}
}

// Removing a link removes the link only, so file_delete and file_rmdir remove
// a link in files/ whatever it points to, and leave its target alone.
func TestWriteScoped_LinkRemovedAsLink(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		target string // relative to files/
		run    func(ws string) *tools.ToolResult
	}{
		{"file_delete of a link to a folder outside", "../skills", func(ws string) *tools.ToolResult {
			return NewDeleteFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link", "sure": true})
		}},
		{"file_delete of a link to a file outside", "../skills/s/skill.md", func(ws string) *tools.ToolResult {
			return NewDeleteFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link", "sure": true})
		}},
		{"file_rmdir of a link to a folder outside", "../skills", func(ws string) *tools.ToolResult {
			return NewRmdirToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link", "sure": true})
		}},
		{"file_rmdir recursive of a link to a folder outside", "../skills", func(ws string) *tools.ToolResult {
			return NewRmdirToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link", "sure": true, "recursive": true})
		}},
		{"file_delete of a dangling link", "missing", func(ws string) *tools.ToolResult {
			return NewDeleteFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link", "sure": true})
		}},
		{"file_rmdir of a dangling link", "missing", func(ws string) *tools.ToolResult {
			return NewRmdirToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link", "sure": true})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			mkTree(t, ws, map[string]string{"skills/s/skill.md": "skill", "files/keep.txt": "k"})
			link := filepath.Join(ws, "files", "link")
			if err := os.Symlink(tc.target, link); err != nil {
				t.Fatal(err)
			}
			res := tc.run(ws)
			if res.IsError || !contains(res.ForLLM, "Link removed: files/link (its target was not touched)") {
				t.Fatalf("want the link removed, got IsError=%v: %s", res.IsError, res.ForLLM)
			}
			if _, err := os.Lstat(link); !os.IsNotExist(err) {
				t.Fatalf("link should be gone (err=%v)", err)
			}
			mustRead(t, filepath.Join(ws, "skills", "s", "skill.md"), "skill")
			mustRead(t, filepath.Join(ws, "files", "keep.txt"), "k")
		})
	}
}

func TestWriteScoped_LinkDeleteNeedsSure(t *testing.T) {
	ws, skill := linkedWorkspace(t)
	ctx := context.Background()
	for _, res := range []*tools.ToolResult{
		NewDeleteFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link", "sure": false}),
		NewRmdirToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link", "sure": false}),
	} {
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) {
			t.Fatalf("want a refusal without sure=true, got IsError=%v: %s", res.IsError, res.ForLLM)
		}
	}
	if _, err := os.Lstat(filepath.Join(ws, "files", "link")); err != nil {
		t.Fatalf("link should still be there: %v", err)
	}
	mustRead(t, skill, "skill")
}

func TestWriteScoped_LinkWithBackupNameRefused(t *testing.T) {
	ws, skill := linkedWorkspace(t)
	link := filepath.Join(ws, "files", "notes.0001")
	if err := os.Symlink("keep.txt", link); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, res := range []*tools.ToolResult{
		NewDeleteFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/notes.0001", "sure": true}),
		NewRmdirToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/notes.0001", "sure": true}),
	} {
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "backup file") {
			t.Fatalf("want a backup-name refusal, got IsError=%v: %s", res.IsError, res.ForLLM)
		}
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("link should still be there: %v", err)
	}
	mustRead(t, skill, "skill")
}

func TestWriteScoped_RmdirTreeWithOutwardLink(t *testing.T) {
	ws := t.TempDir()
	mkTree(t, ws, map[string]string{"skills/s/skill.md": "skill", "files/dir/a.txt": "a", "files/dir/sub/b.txt": "b"})
	if err := os.Symlink(filepath.Join("..", "..", "..", "skills"), filepath.Join(ws, "files", "dir", "sub", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gone", filepath.Join(ws, "files", "dir", "dangling")); err != nil {
		t.Fatal(err)
	}

	res := NewRmdirToolScoped(ws, true, "files").Execute(context.Background(), map[string]any{"path": "files/dir", "sure": true, "recursive": true})
	if res.IsError {
		t.Fatalf("rmdir of a folder holding links: %s", res.ForLLM)
	}
	mustNotExist(t, filepath.Join(ws, "files", "dir"))
	mustRead(t, filepath.Join(ws, "skills", "s", "skill.md"), "skill")
}

func TestWriteScoped_MoveFolderWithLinkRefused(t *testing.T) {
	ws := t.TempDir()
	mkTree(t, ws, map[string]string{"skills/s/skill.md": "skill", "files/dir/a.txt": "a"})
	if err := os.Symlink(filepath.Join("..", "..", "skills"), filepath.Join(ws, "files", "dir", "link")); err != nil {
		t.Fatal(err)
	}

	res := NewMoveFileToolScoped(ws, true, "files").Execute(context.Background(), map[string]any{"source_path": "files/dir", "destination_path": "files/moved"})
	if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "files/dir/link") {
		t.Fatalf("want a refusal naming the link, got IsError=%v: %s", res.IsError, res.ForLLM)
	}
	mustNotExist(t, filepath.Join(ws, "files", "moved"))
	mustRead(t, filepath.Join(ws, "files", "dir", "a.txt"), "a")
	if _, err := os.Lstat(filepath.Join(ws, "files", "dir", "link")); err != nil {
		t.Fatalf("link should still be there: %v", err)
	}
	mustRead(t, filepath.Join(ws, "skills", "s", "skill.md"), "skill")
}

// file_move refuses a link as its source, before any copy, wherever it points.
func TestWriteScoped_MoveLinkSourceRefused(t *testing.T) {
	cases := []struct {
		name   string
		target string // relative to files/
	}{
		{"link to a file inside files", "real.txt"},
		{"link to a folder inside files", "realdir"},
		{"link to a folder outside files", "../skills"},
		{"dangling link", "missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			mkTree(t, ws, map[string]string{"skills/s/skill.md": "skill", "files/real.txt": "r", "files/realdir/a.txt": "a"})
			link := filepath.Join(ws, "files", "link")
			if err := os.Symlink(tc.target, link); err != nil {
				t.Fatal(err)
			}
			res := NewMoveFileToolScoped(ws, true, "files").Execute(context.Background(), map[string]any{"source_path": "files/link", "destination_path": "files/moved"})
			if !res.IsError || !tools.IsExpectedRefusal(res.Err) || !contains(res.ForLLM, "file_move cannot move a link: files/link") {
				t.Fatalf("want a refusal naming the link, got IsError=%v: %s", res.IsError, res.ForLLM)
			}
			mustNotExist(t, filepath.Join(ws, "files", "moved"))
			if _, err := os.Lstat(link); err != nil {
				t.Fatalf("link should still be there: %v", err)
			}
			mustRead(t, filepath.Join(ws, "files", "real.txt"), "r")
			mustRead(t, filepath.Join(ws, "files", "realdir", "a.txt"), "a")
			mustRead(t, filepath.Join(ws, "skills", "s", "skill.md"), "skill")
		})
	}
}
