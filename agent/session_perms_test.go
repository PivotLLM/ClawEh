//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/providers"
)

// A session's archive database and its -wal/-shm side files are 0600 from
// the first write, whatever the process umask, not only after the next start.
func TestSessionArchive_PrivateFromCreation(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	al := newTestAgentLoop(t).al
	agent := al.registry.Default()
	if agent == nil {
		t.Fatal("no default agent")
	}
	const sessionKey = "agent:main:main"
	cm, release := al.getContextManager(agent, sessionKey)
	defer release()
	if _, err := cm.AddUserMessage(context.Background(), providers.Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	path := archiveDBPath(agent.StateDir, sessionKey)
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("sessions directory mode = %v (err %v), want 0700", modeOf(info), err)
	}
	checked := 0
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if os.IsNotExist(err) && p != path {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		checked++
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(p), got)
		}
	}
	if checked < 2 {
		t.Fatalf("expected the archive and its WAL to exist, checked %d files", checked)
	}
}

func modeOf(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}
