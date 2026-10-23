//go:build !windows

package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
)

// A device's chat.history on a session nobody has used yet creates the
// archive privately (0600, with its side files), whatever the umask.
func TestDeviceQuerierHistory_CreatesArchivePrivately(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	base := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: base,
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{{ID: "alice", Name: "Alice", Default: true}},
		},
	}
	al, err := agent.NewAgentLoop(cfg, bus.NewMessageBus(), providers.NewUnconfiguredProvider(), nil)
	if err != nil {
		t.Fatalf("NewAgentLoop: %v", err)
	}
	if got := (deviceAgentQuerier{al: al}).History("agent:alice:main"); len(got) != 0 {
		t.Fatalf("history of a new session = %+v", got)
	}

	found := 0
	err = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(filepath.Base(p), ".archive.db") {
			found++
			if got := info.Mode().Perm(); got != 0o600 {
				t.Errorf("%s mode = %o, want 600", filepath.Base(p), got)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("chat.history created no archive to check")
	}
}
