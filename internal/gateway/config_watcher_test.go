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
	ch, stop, _ := setupConfigWatcherPolling(path, interval, debounce, false)
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

// TestConfigWatcher_MarkAppliedSuppressesReload verifies that after a change is
// written and markApplied() is called (as the force-reload path does), the
// watcher advances its baseline and does NOT fire a redundant reload — the
// double-reload that was tearing down an active chat after the setup wizard.
func TestConfigWatcher_MarkAppliedSuppressesReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	interval := 10 * time.Millisecond
	debounce := 80 * time.Millisecond
	ch, stop, markApplied := setupConfigWatcherPolling(path, interval, debounce, false)
	defer stop()

	time.Sleep(3 * interval) // let the watcher capture its baseline

	// Simulate a force-reload: the config changes and the out-of-band path
	// applies it, then tells the watcher via markApplied().
	writeConfig(t, path, "force-applied")
	markApplied()

	// The watcher must not deliver a reload for the already-applied change.
	select {
	case <-ch:
		t.Fatal("watcher fired a redundant reload after markApplied()")
	case <-time.After(debounce + 200*time.Millisecond):
	}

	// A genuinely new change after markApplied still triggers a reload.
	writeConfig(t, path, "later-edit")
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("a new change after markApplied should still reload")
	}
}

// TestConfigWatcher_RetriesWhenConsumerBusy verifies that a change which cannot
// be delivered because the consumer is still draining a previous reload is NOT
// lost: once the consumer catches up, the pending change is delivered. This
// guards the bug where the applied marker advanced on a dropped send, so
// enabling an agent's tool suite silently required a restart.
func TestConfigWatcher_RetriesWhenConsumerBusy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	interval := 10 * time.Millisecond
	debounce := 60 * time.Millisecond
	ch, stop, _ := setupConfigWatcherPolling(path, interval, debounce, false)
	defer stop()

	// Let the watcher capture its baseline against the seed file before the first
	// real change, so A is reliably detected as new.
	time.Sleep(3 * interval)

	// First change A is delivered into the cap-1 buffer; we intentionally do NOT
	// read it yet, simulating a consumer still busy with a prior reload. Poll until
	// A is actually buffered so the next write is guaranteed to find a full buffer.
	writeConfig(t, path, "A")
	deadline := time.Now().Add(3 * time.Second)
	for len(ch) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("first reload (A) was never delivered to the buffer")
		}
		time.Sleep(interval)
	}

	// Second change B lands while the buffer is full → the send is dropped. With
	// the fix the watcher keeps retrying instead of advancing the applied marker.
	writeConfig(t, path, "BB")
	time.Sleep(5 * debounce) // let the debounce elapse and the dropped send retry

	// Drain A (consumer catches up).
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected the first reload (A) to be buffered")
	}

	// B must still be delivered — not lost.
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("change B was lost: watcher advanced past it without delivering")
	}
}
