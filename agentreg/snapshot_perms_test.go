//go:build !windows

package agentreg

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	cogmemstore "github.com/PivotLLM/cogmem/store"

	"github.com/PivotLLM/ClawEh/cogmemhost"
)

// A clone's snapshot of its source's memory is owner-only (directory 0700,
// database 0600) whatever the process umask.
func TestCloneMemorySnapshot_Private(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	cfg := testConfig(t)
	r := mustNew(t, cfg, newFakeHost())

	srcPath := cogmemstore.DBPath(cogmemhost.Dir(mustGet(t, r, "alice").spec.StateDir))
	st, err := cogmemstore.Open(srcPath)
	if err != nil {
		t.Fatalf("open source memory: %v", err)
	}
	if err = st.Close(); err != nil {
		t.Fatalf("close source memory: %v", err)
	}

	clone := mustClone(t, r, "alice")
	dir := cogmemhost.Dir(mustGet(t, r, clone).spec.StateDir)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("clone memory directory mode = %04o, want 0700", got)
	}
	path := cogmemstore.DBPath(dir)
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("%s mode = %04o, want 0600", filepath.Base(path), got)
	}
}
