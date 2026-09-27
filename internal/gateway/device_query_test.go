package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
)

func countFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// openFDs counts this process's open descriptors where /proc exposes them;
// -1 elsewhere.
func openFDs() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// History for a session key whose agent is not registered reads nothing: no
// session database is created and no descriptor is left open.
func TestDeviceQuerierHistoryUnknownAgent(t *testing.T) {
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
	q := deviceAgentQuerier{al: al}

	filesBefore, fdsBefore := countFiles(t, base), openFDs()
	for range 50 {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		if got := q.History("agent:" + hex.EncodeToString(b) + ":main"); got != nil {
			t.Fatalf("history for an unknown agent: %+v", got)
		}
	}
	if after := countFiles(t, base); after != filesBefore {
		t.Fatalf("files under the agents dir: %d before, %d after", filesBefore, after)
	}
	if fdsBefore >= 0 {
		if after := openFDs(); after > fdsBefore {
			t.Fatalf("open descriptors: %d before, %d after", fdsBefore, after)
		}
	}
}
