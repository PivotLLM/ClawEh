// ClawEh
// License: MIT

package files

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/utils"
)

// A link in an allow-read host folder is followed only to a place the agent
// may read: another allow-listed path or the workspace's read scope. A link
// from there to the workspace's sessions/ or AGENTS.md, or to any other host
// path, is refused in plain words.
func TestAllowReadPath_LinkTargetChecked(t *testing.T) {
	ws := t.TempDir()
	shared := t.TempDir()
	other := t.TempDir()
	mustWrite(t, filepath.Join(ws, "files", "doc.txt"), "L1\nL2\n")
	mustWrite(t, filepath.Join(ws, "sessions", "s.txt"), "L1\nL2\n")
	mustWrite(t, filepath.Join(ws, "AGENTS.md"), "L1\nL2\n")
	mustWrite(t, filepath.Join(shared, "own.txt"), "L1\nL2\n")
	mustWrite(t, filepath.Join(other, "secret.txt"), "L1\nL2\n")
	for link, target := range map[string]string{
		"sess.txt":   filepath.Join(ws, "sessions", "s.txt"),
		"agents.md":  filepath.Join(ws, "AGENTS.md"),
		"secret.txt": filepath.Join(other, "secret.txt"),
		"files.txt":  filepath.Join(ws, "files", "doc.txt"),
		"own.txt":    filepath.Join(shared, "own.txt"),
	} {
		if err := os.Symlink(target, filepath.Join(shared, "link-"+link)); err != nil {
			t.Fatal(err)
		}
	}
	defaultScope(t)
	patterns := []*regexp.Regexp{regexp.MustCompile("^" + regexp.QuoteMeta(shared) + "/")}
	read := NewReadLinesTool(ws, true, MaxReadFileSize, patterns)
	ctx := context.Background()
	for _, name := range []string{"link-sess.txt", "link-agents.md", "link-secret.txt"} {
		p := filepath.Join(shared, name)
		res := read.Execute(ctx, map[string]any{"path": p})
		if !res.IsError || !tools.IsExpectedRefusal(res.Err) || strings.Contains(res.ForLLM, "L2") {
			t.Errorf("%s: want a refusal, got %v %q", name, res.IsError, res.ForLLM)
		}
	}
	for _, name := range []string{"own.txt", "link-own.txt", "link-files.txt"} {
		p := filepath.Join(shared, name)
		if res := read.Execute(ctx, map[string]any{"path": p}); res.IsError || !strings.Contains(res.ForLLM, "L2") {
			t.Errorf("%s: want it read, got %v %q", name, res.IsError, res.ForLLM)
		}
	}
}

// A file checked as inside the read scope and replaced by a link to
// sessions/ just before the read is not read: the read is made inside the
// read root's own os.Root, which the link cannot leave.
func TestReadScope_LinkSwappedBeforeRead(t *testing.T) {
	for _, op := range []string{"ReadFile", "Open", "Stat"} {
		t.Run(op, func(t *testing.T) {
			ws := t.TempDir()
			doc := filepath.Join(ws, "files", "doc.txt")
			mustWrite(t, doc, "L1\nL2\n")
			mustWrite(t, filepath.Join(ws, "sessions", "s.txt"), "secret")
			r := newReadScopedFs(&sandboxFs{workspace: ws}, ws, []string{"files", "skills"}, nil)
			r.beforeRead = func(string) {
				if err := os.Remove(doc); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../sessions/s.txt", doc); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch op {
			case "ReadFile":
				var data []byte
				data, err = r.ReadFile("files/doc.txt")
				if strings.Contains(string(data), "secret") {
					t.Fatal("read sessions/ through a swapped link")
				}
			case "Open":
				var f fs.File
				if f, err = r.Open("files/doc.txt"); err == nil {
					utils.CloseQuietly(f)
				}
			case "Stat":
				_, err = r.Stat("files/doc.txt")
			}
			if err == nil || !tools.IsExpectedRefusal(err) || !strings.Contains(err.Error(), "resolves outside the folders the agent can read") {
				t.Fatalf("%s after the swap = %v, want the refusal", op, err)
			}
		})
	}
}
