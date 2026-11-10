// ClawEh
// License: MIT

package files

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
)

// harnessDoc is the content of every file the matrix edits.
const harnessDoc = "L1\nL2\nL3\n"

// harnessFixture is one agent's view for the coding-tools matrix: a workspace
// with files/ (writable), skills/ (read-only) and a file at its root (outside
// the read scope); a writable allowed folder; a read-only allowed folder (the
// account cannot write it); and a folder outside everything, reached only
// through links from files/ and from the writable allowed folder.
type harnessFixture struct {
	ws, src, ro, outside string
}

func newHarnessFixture(t *testing.T) harnessFixture {
	t.Helper()
	f := harnessFixture{ws: t.TempDir(), src: t.TempDir(), ro: t.TempDir(), outside: t.TempDir()}
	for _, p := range []string{
		filepath.Join(f.ws, "files", "doc.txt"),
		filepath.Join(f.ws, "skills", "s", "SKILL.md"),
		filepath.Join(f.ws, "AGENTS.md"),
		filepath.Join(f.src, "doc.txt"),
		filepath.Join(f.ro, "doc.txt"),
		filepath.Join(f.outside, "doc.txt"),
	} {
		mustWrite(t, p, harnessDoc)
	}
	for _, link := range []string{filepath.Join(f.ws, "files", "out"), filepath.Join(f.src, "out")} {
		if err := os.Symlink(f.outside, link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(f.outside, "doc.txt"), link+".txt"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(f.ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(f.ro, 0o700); err != nil {
			t.Error(err)
		}
	})
	allowFolders(t, f.ws, f.src, f.ro)
	defaultScope(t)
	return f
}

// harnessTool is one coding tool as the provider builds it, with the
// arguments of one call on path and, for a write, the file content the call
// leaves.
type harnessTool struct {
	name  string
	write bool
	tool  func(ws string) tools.Tool
	args  func(path string) map[string]any
	want  string // write: content after the call; read: a substring of the result
}

var harnessTools = []harnessTool{
	{
		name: "file_read_lines",
		tool: func(ws string) tools.Tool { return NewReadLinesTool(ws, true, MaxReadFileSize) },
		args: func(p string) map[string]any { return map[string]any{"path": p, "start_line": 2, "line_count": 1} },
		want: "2: L2",
	},
	{
		name: "file_edit_lines", write: true,
		tool: func(ws string) tools.Tool { return newRangeEditTool("edit", "lines", ws, true, "files") },
		args: func(p string) map[string]any { return map[string]any{"path": p, "start": 2, "replace": "X"} },
		want: "L1\nX\nL3\n",
	},
	{
		name: "file_insert_lines", write: true,
		tool: func(ws string) tools.Tool { return newRangeEditTool("insert", "lines", ws, true, "files") },
		args: func(p string) map[string]any { return map[string]any{"path": p, "after_line": 1, "text": "X"} },
		want: "L1\nX\nL2\nL3\n",
	},
	{
		name: "file_delete_lines", write: true,
		tool: func(ws string) tools.Tool { return newRangeEditTool("delete", "lines", ws, true, "files") },
		args: func(p string) map[string]any { return map[string]any{"path": p, "start": 2} },
		want: "L1\nL3\n",
	},
	{
		name: "file_edit", write: true,
		tool: func(ws string) tools.Tool { return NewEditFileToolScoped(ws, true, "files") },
		args: func(p string) map[string]any { return map[string]any{"path": p, "old_text": "L2", "new_text": "X"} },
		want: "L1\nX\nL3\n",
	},
	{
		name: "file_write", write: true,
		tool: func(ws string) tools.Tool { return NewWriteFileToolScoped(ws, true, "files") },
		args: func(p string) map[string]any { return map[string]any{"path": p, "content": "X\n", "overwrite": true} },
		want: "X\n",
	},
}

// Each coding tool, called the way a model calls it, in every place it may
// be pointed at: the workspace's files/ by relative and absolute path, an
// allowed folder by absolute path, a read-only allowed folder, a link out of
// files/ and out of an allowed folder, skills/ and the workspace root. Reads
// work wherever the agent may read; writes land in files/ and the writable
// allowed folder only, a write to the read-only folder is refused with the
// permission sentence, and nothing outside is touched.
func TestCodingTools_PermissionMatrix(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs file permissions that bind the test account")
	}
	type place struct {
		name    string
		path    func(f harnessFixture) string
		file    func(f harnessFixture) string // the file on disk the call reaches
		read    bool                          // reads succeed
		write   bool                          // writes succeed
		refusal func(f harnessFixture) string // the write refusal's text, nil = any refusal
	}
	places := []place{
		{
			name: "files/ relative",
			path: func(harnessFixture) string { return "files/doc.txt" },
			file: func(f harnessFixture) string { return filepath.Join(f.ws, "files", "doc.txt") },
			read: true, write: true,
		},
		{
			name: "files/ absolute",
			path: func(f harnessFixture) string { return filepath.Join(f.ws, "files", "doc.txt") },
			file: func(f harnessFixture) string { return filepath.Join(f.ws, "files", "doc.txt") },
			read: true, write: true,
		},
		{
			name: "allowed folder absolute",
			path: func(f harnessFixture) string { return filepath.Join(f.src, "doc.txt") },
			file: func(f harnessFixture) string { return filepath.Join(f.src, "doc.txt") },
			read: true, write: true,
		},
		{
			name:    "read-only allowed folder",
			path:    func(f harnessFixture) string { return filepath.Join(f.ro, "doc.txt") },
			file:    func(f harnessFixture) string { return filepath.Join(f.ro, "doc.txt") },
			read:    true,
			refusal: func(f harnessFixture) string { return "is read-only for this app" },
		},
		{
			name: "link out of an allowed folder",
			path: func(f harnessFixture) string { return filepath.Join(f.src, "out.txt") },
			file: func(f harnessFixture) string { return filepath.Join(f.outside, "doc.txt") },
		},
		{
			name: "folder link out of an allowed folder",
			path: func(f harnessFixture) string { return filepath.Join(f.src, "out", "doc.txt") },
			file: func(f harnessFixture) string { return filepath.Join(f.outside, "doc.txt") },
		},
		{
			name: "link out of files/",
			path: func(harnessFixture) string { return "files/out.txt" },
			file: func(f harnessFixture) string { return filepath.Join(f.outside, "doc.txt") },
		},
		{
			name: "folder link out of files/",
			path: func(harnessFixture) string { return "files/out/doc.txt" },
			file: func(f harnessFixture) string { return filepath.Join(f.outside, "doc.txt") },
		},
		{
			name: "outside by absolute path",
			path: func(f harnessFixture) string { return filepath.Join(f.outside, "doc.txt") },
			file: func(f harnessFixture) string { return filepath.Join(f.outside, "doc.txt") },
		},
		{
			name:    "skills/ is read-only",
			path:    func(harnessFixture) string { return "skills/s/SKILL.md" },
			file:    func(f harnessFixture) string { return filepath.Join(f.ws, "skills", "s", "SKILL.md") },
			read:    true,
			refusal: func(harnessFixture) string { return "write denied" },
		},
		{
			name: "workspace root",
			path: func(harnessFixture) string { return "AGENTS.md" },
			file: func(f harnessFixture) string { return filepath.Join(f.ws, "AGENTS.md") },
		},
		{
			name: "parent of the workspace",
			path: func(harnessFixture) string { return "../doc.txt" },
			file: func(f harnessFixture) string { return filepath.Join(filepath.Dir(f.ws), "doc.txt") },
		},
	}
	ctx := context.Background()
	for _, ht := range harnessTools {
		for _, pl := range places {
			t.Run(ht.name+"/"+pl.name, func(t *testing.T) {
				f := newHarnessFixture(t)
				path := pl.path(f)
				res := ht.tool(f.ws).Execute(ctx, ht.args(path))
				ok := pl.read
				if ht.write {
					ok = pl.write
				}
				if ok {
					if res.IsError {
						t.Fatalf("%s %s: %s", ht.name, path, res.ForLLM)
					}
					if !ht.write {
						if !strings.Contains(res.ForLLM, ht.want) {
							t.Errorf("%s %s: result lacks %q:\n%s", ht.name, path, ht.want, res.ForLLM)
						}
						return
					}
					if got := readBack(t, pl.file(f)); got != ht.want {
						t.Errorf("%s %s: file holds %q, want %q", ht.name, path, got, ht.want)
					}
					return
				}
				if !res.IsError {
					t.Fatalf("%s %s: not refused: %s", ht.name, path, res.ForLLM)
				}
				if ht.write && pl.refusal != nil {
					if want := pl.refusal(f); !strings.Contains(res.ForLLM, want) {
						t.Errorf("%s %s: refusal %q lacks %q", ht.name, path, res.ForLLM, want)
					}
				}
				if strings.Contains(res.ForLLM, "L2") {
					t.Errorf("%s %s: a refusal shows the file's content: %s", ht.name, path, res.ForLLM)
				}
				if data, err := os.ReadFile(pl.file(f)); err == nil && string(data) != harnessDoc {
					t.Errorf("%s %s: a refused call changed the file to %q", ht.name, path, data)
				}
				// Nothing new beside the file: no backup, no temporary file.
				for _, dir := range []string{f.ro, f.outside, filepath.Join(f.ws, "skills", "s")} {
					entries, err := os.ReadDir(dir)
					if err != nil {
						t.Fatal(err)
					}
					for _, e := range entries {
						if n := e.Name(); n != "doc.txt" && n != "SKILL.md" {
							t.Errorf("%s %s: a refused call left %s in %s", ht.name, path, n, dir)
						}
					}
				}
			})
		}
	}
}

// file_list lists files/ (by relative and absolute path), an allowed folder
// and a read-only one by absolute path, and skills/; it refuses the
// workspace root, a folder outside and a link out of files/ or of an allowed
// folder.
func TestCodingTools_ListMatrix(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs file permissions that bind the test account")
	}
	f := newHarnessFixture(t)
	list := NewListDirTool(f.ws, true)
	ctx := context.Background()
	for _, p := range []string{"files", filepath.Join(f.ws, "files"), f.src, f.ro, "skills/s"} {
		res := list.Execute(ctx, map[string]any{"path": p})
		if res.IsError || (!strings.Contains(res.ForLLM, "doc.txt") && !strings.Contains(res.ForLLM, "SKILL.md")) {
			t.Errorf("list %s: %v %s", p, res.IsError, res.ForLLM)
		}
	}
	for _, p := range []string{".", f.outside, "files/out", filepath.Join(f.src, "out"), ".."} {
		res := list.Execute(ctx, map[string]any{"path": p})
		if !res.IsError || strings.Contains(res.ForLLM, "doc.txt") {
			t.Errorf("list %s: want a refusal, got %v %s", p, res.IsError, res.ForLLM)
		}
	}
}
