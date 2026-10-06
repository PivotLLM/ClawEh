//go:build !windows

// ClawEh - Cognitive Memory
// License: MIT

package cogmem

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PivotLLM/cogmem/store"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/tools"
)

// A memory store first created by a cogmem tool is owner-only: the directory
// 0700 and the database with its side files 0600, whatever the umask.
func TestToolCreatesPrivateStore(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	ws := t.TempDir()
	alice := handlersFor(tools.ToolDeps{Workspace: ws, AgentID: "alice"})
	if res := run(t, alice["memory_create"], newCall(mainSession, map[string]any{
		"type": "fact", "text": "Alice prefers coffee.",
	})); res.IsError {
		t.Fatalf("memory_create failed: %s", res.ForLLM)
	}

	dir := cogmemhost.Dir(ws)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("cogmem directory mode = %04o, want 0700", got)
	}
	path := store.DBPath(dir)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if os.IsNotExist(err) && p != path {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", filepath.Base(p), got)
		}
	}
}
