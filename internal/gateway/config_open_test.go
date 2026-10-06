package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/logger"
)

type lockedBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

// The config is read once at startup, so an unknown key is warned about once.
func TestOpenConfigStore_WarnsUnknownKeyOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	home := filepath.Join(dir, ".claw")
	t.Setenv("CLAW_HOME", home)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"agents": {"list": [{"id": "alice", "name": "Alice", "default": true}]}, "no_such_key": 1}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	buf := &lockedBuf{}
	restore := logger.RedirectForTest(buf)
	defer restore()

	store, err := openConfigStore(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("openConfigStore: %v", err)
	}
	if store.Path() != filepath.Join(home, "config.json") {
		t.Fatalf("store path = %q", store.Path())
	}
	if n := strings.Count(buf.String(), "unknown config key: no_such_key"); n != 1 {
		t.Fatalf("unknown key warned %d times, want once:\n%s", n, buf.String())
	}
}

// On first run a default config is seeded and opened.
func TestOpenConfigStore_SeedsFirstRun(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	home := filepath.Join(dir, ".claw")
	t.Setenv("CLAW_HOME", home)

	store, err := openConfigStore(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("openConfigStore: %v", err)
	}
	if _, err := os.Stat(store.Path()); err != nil {
		t.Fatalf("default config not seeded: %v", err)
	}
}
