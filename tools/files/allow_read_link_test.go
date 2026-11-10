// ClawEh
// License: MIT

package files

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
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
