//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	cogmemstore "github.com/PivotLLM/cogmem/store"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
)

// With security.permissions.agents set, the agent's workspace and seeded
// files, its session archive and its cognitive memory are created with the
// area's modes, so a group can read them.
func TestAgentsAreaModes(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	cfg := newTestConfig(t)
	cfg.Security.Permissions.Agents = config.AreaPermissions{DirMode: "0750", FileMode: "0640"}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, nil)
	agent := al.registry.Default()
	if agent == nil {
		t.Fatal("no default agent")
	}
	const sessionKey = "agent:main:main"
	cm, mem, release := al.getSessionContext(agent, sessionKey)
	defer release()
	if _, err := cm.AddUserMessage(context.Background(), providers.Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	if mem == nil || mem.Store() == nil {
		t.Fatal("cognitive-memory store did not open")
	}

	archive := archiveDBPath(agent.StateDir, sessionKey)
	memDir := cogmemhost.Dir(agent.StateDir)
	for path, want := range map[string]os.FileMode{
		agent.Workspace:                             0o750,
		filepath.Join(agent.Workspace, "files"):     0o750,
		filepath.Join(agent.Workspace, "AGENTS.md"): 0o640,
		filepath.Dir(archive):                       0o750,
		archive:                                     0o640,
		memDir:                                      0o750,
		cogmemstore.DBPath(memDir):                  0o640,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", path, got, want)
		}
	}
}

// A temporary agent's state lies outside the agents area, so it keeps the
// owner-only defaults whatever the area allows.
func TestAgentModes_TemporaryStatePrivate(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Security.Permissions.Agents = config.AreaPermissions{DirMode: "0750", FileMode: "0640"}
	got := agentModes(cfg, filepath.Join(t.TempDir(), "claw-temp-agents-1", "uuid"))
	if want := (config.Modes{Dir: 0o700, File: 0o600}); got != want {
		t.Errorf("temporary agent modes = %v, want %v", got, want)
	}
	if got := agentModes(nil, "/anywhere"); got != (config.Modes{Dir: 0o700, File: 0o600}) {
		t.Errorf("modes without a config = %v, want 0700/0600", got)
	}
}
