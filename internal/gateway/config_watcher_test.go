package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// validConfigJSON is the minimal config the watcher's LoadConfig+ValidateModels
// accepts. An empty models is valid (validation only rejects malformed lists).
const validConfigJSON = `{"models":[]}`

func writeConfig(t *testing.T, path, extra string) {
	t.Helper()
	body := `{"models":[],"_marker":"` + extra + `"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestConfigWatcher_DebouncesBurstIntoSingleReload verifies that a burst of
// writes within the debounce window collapses into exactly one reload, and that
// each write resets the quiet timer (no reload until the file goes quiet).
func TestConfigWatcher_DebouncesBurstIntoSingleReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	interval := 10 * time.Millisecond
	debounce := 120 * time.Millisecond
	ch, stop := setupConfigWatcherPolling(path, interval, debounce, false)
	defer stop()

	// Burst of three writes, each spaced under the debounce window so each resets
	// the timer. No reload should fire during the burst.
	for i := 0; i < 3; i++ {
		writeConfig(t, path, time.Duration(i).String()+"-burst")
		time.Sleep(50 * time.Millisecond) // < debounce
	}

	// Nothing should have been delivered yet (the timer kept resetting).
	select {
	case <-ch:
		t.Fatal("reload fired during the burst; debounce did not reset the timer")
	default:
	}

	// After quiescence (> debounce), exactly one reload should arrive.
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("expected one reload after the file went quiet; got none")
	}

	// And no second reload for the same settled state.
	select {
	case <-ch:
		t.Fatal("unexpected second reload for an unchanged file")
	case <-time.After(debounce + 100*time.Millisecond):
	}
}
