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
		{"file_rmdir recursive on the link", func(ws string) *tools.ToolResult {
			return NewRmdirToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link", "sure": true, "recursive": true})
		}},
		{"file_rmdir recursive below the link", func(ws string) *tools.ToolResult {
			return NewRmdirToolScoped(ws, true, "files").Execute(ctx, map[string]any{"path": "files/link/s", "sure": true, "recursive": true})
		}},
		{"file_move with the link as source", func(ws string) *tools.ToolResult {
			return NewMoveFileToolScoped(ws, true, "files").Execute(ctx, map[string]any{"source_path": "files/link", "destination_path": "files/moved"})
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
