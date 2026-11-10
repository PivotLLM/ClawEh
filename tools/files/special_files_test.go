//go:build unix

// ClawEh
// License: MIT

package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/utils"
)

// mkfifo makes a FIFO at path, skipping the test where that is not possible.
func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
}

// notBlocking runs fn and fails the test when it does not return in time: a read
// that waits on a FIFO for a writer never would.
func notBlocking[T any](t *testing.T, what string, fn func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- fn() }()
	select {
	case v := <-done:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked", what)
		var zero T
		return zero
	}
}

// Every file tool that reads a file refuses a FIFO at once, whether the
// agent is confined to its workspace (os.Root) or not (host paths).
func TestFileTools_RefuseFIFO(t *testing.T) {
	ctx := context.Background()
	for _, restrict := range []bool{true, false} {
		ws := t.TempDir()
		mkfifo(t, filepath.Join(ws, "files", "pipe"))
		path := "files/pipe"
		if !restrict {
			path = filepath.Join(ws, "files", "pipe")
		}
		for _, tc := range []struct {
			name string
			tool tools.Tool
			args map[string]any
		}{
			{"file_read_lines", NewReadLinesTool(ws, restrict, MaxReadFileSize), map[string]any{"path": path}},
			{"file_read_bytes", NewReadFileTool(ws, restrict, MaxReadFileSize), map[string]any{"path": path}},
			{"file_edit", NewEditFileToolScoped(ws, restrict, "files"), map[string]any{"path": path, "old_text": "a", "new_text": "b"}},
			{"file_edit_lines", newRangeEditTool("edit", "lines", ws, restrict, "files"), map[string]any{"path": path, "start": 1, "replace": "x"}},
			{"file_view_image", NewViewImageTool(ws, restrict), map[string]any{"path": path}},
			{"file_search_lines", NewSearchLinesTool(ws, restrict), map[string]any{"query": "x", "path": path}},
			{"file_count", NewCountFileTool(ws, restrict), map[string]any{"path": path}},
		} {
			res := notBlocking(t, tc.name, func() *tools.ToolResult { return tc.tool.Execute(ctx, tc.args) })
			// A search skips what is not a regular file; every other tool refuses.
			if !res.IsError && (tc.name != "file_search_lines" || !strings.HasPrefix(res.ForLLM, "No matches")) {
				t.Errorf("%s (restrict=%v): a FIFO was read: %q", tc.name, restrict, res.ForLLM)
			}
		}
		r := NewReader(readerConfigRestrict(restrict), ws)
		if err := notBlocking(t, "Reader.ReadFile", func() error { _, err := r.ReadFile(path); return err }); err == nil {
			t.Errorf("Reader.ReadFile (restrict=%v) read a FIFO", restrict)
		}
		if err := notBlocking(t, "Reader.ReadFileLimit", func() error { _, _, err := r.ReadFileLimit(path, 0); return err }); err == nil {
			t.Errorf("Reader.ReadFileLimit (restrict=%v) read a FIFO", restrict)
		}
		if err := notBlocking(t, "Reader.Open", func() error {
			f, err := r.Open(path)
			if err == nil {
				utils.CloseQuietly(f)
			}
			return err
		}); err == nil || !tools.IsExpectedRefusal(err) {
			t.Errorf("Reader.Open (restrict=%v) = %v, want a refusal", restrict, err)
		}
	}
}

func readerConfigRestrict(restrict bool) *config.Config {
	c := readerConfig()
	c.Agents.Defaults.RestrictToWorkspace = restrict
	return c
}

// A host path (an unrestricted agent, an allow-listed path) is opened by its
// resolved target, never following a link at the open: a link swapped in
// after the checks is refused, and a target that is not a regular file is
// refused before anything is opened.
func TestHostFs_OpenChecksTheTarget(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "doc.txt")
	secret := filepath.Join(dir, "secret.txt")
	mustWrite(t, doc, "doc")
	mustWrite(t, secret, "secret")

	h := &hostFs{beforeOpen: func(target string) {
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, target); err != nil {
			t.Fatal(err)
		}
	}}
	if f, err := h.Open(doc); err == nil {
		utils.CloseQuietly(f)
		t.Fatal("a link swapped in before the open was followed")
	}
	if target, err := os.Readlink(doc); err != nil || target != secret {
		t.Fatalf("the hook did not swap the file: %q %v", target, err)
	}

	fifo := filepath.Join(dir, "pipe")
	mkfifo(t, fifo)
	link := filepath.Join(dir, "pipe-link")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	opened := false
	h = &hostFs{beforeOpen: func(string) { opened = true }}
	for _, p := range []string{fifo, link} {
		err := notBlocking(t, "hostFs.Open", func() error {
			f, err := h.Open(p)
			if err == nil {
				utils.CloseQuietly(f)
			}
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("Open(%s) = %v, want not a regular file", p, err)
		}
	}
	if opened {
		t.Error("a FIFO target reached the open")
	}
	// Nothing holds the FIFO open for reading: a writer finds no reader.
	if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
		utils.CloseQuietly(w)
		t.Error("the FIFO has a reader")
	}
}
