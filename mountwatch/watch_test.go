// ClawEh
// License: MIT

package mountwatch

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
)

func TestDetectNewFiles_OnlyNewPathsFire(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.md")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First scan with no marker → baseline: nothing fires, .claw created.
	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("baseline should report no new files, got %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, markerFile)); err != nil {
		t.Fatalf(".claw marker should be created on baseline: %v", err)
	}

	// Append to the existing file (new mtime, same path) → must NOT fire.
	if err := os.WriteFile(old, []byte("x-appended"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("editing an existing file must not fire, got %v", got)
	}

	// A genuinely new file (new path) → fires once.
	newFile := filepath.Join(dir, "sub", "new.md")
	if err := os.MkdirAll(filepath.Dir(newFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := detectNewFiles("notes", dir)
	if len(got) != 1 || got[0] != "notes/sub/new.md" {
		t.Fatalf("want [notes/sub/new.md], got %v", got)
	}

	// Recorded set advanced → a second scan reports nothing, and editing the
	// now-known new file still does not fire.
	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("after advancing, expected nothing, got %v", got)
	}
	os.WriteFile(newFile, []byte("y2"), 0o644)
	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("editing a now-known file must not fire, got %v", got)
	}
}

func TestDetectNewFiles_IgnoresMarkerAndHidden(t *testing.T) {
	dir := t.TempDir()
	detectNewFiles("notes", dir) // baseline, creates .claw

	// A hidden file and the marker itself must never be reported.
	os.WriteFile(filepath.Join(dir, ".secret"), []byte("h"), 0o644)
	if got := detectNewFiles("notes", dir); len(got) != 0 {
		t.Fatalf("hidden files and the marker must be ignored, got %v", got)
	}
}

// TestWatcherStopIsIdempotent pins the contract the gateway relies on: its
// shutdown path runs on every config reload as well as at exit, so Stop is
// reached more than once for the same Watcher. A second close of the stop
// channel used to panic the whole process ("close of closed channel"), taking
// the gateway down on the second config change of its life.
func TestWatcherStopIsIdempotent(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour)
	w.Start()
	w.Stop()
	w.Stop() // must not panic
}

// TestWatcherStopBeforeStart covers the other order: a Watcher built but never
// started is still stopped by the cleanup path.
func TestWatcherStopBeforeStart(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour)
	w.Stop()
	w.Stop()
}

// TestNilWatcherStop covers the zero value the gatewayServices struct holds
// before setup runs.
func TestNilWatcherStop(t *testing.T) {
	var w *Watcher
	w.Stop()
}

// TestWatcherStopConcurrent covers the shape the gateway can actually produce:
// a config reload calling Stop while the shutdown path does the same. sync.Once
// has to serialise them, and the wait must not return before the scan goroutine
// has exited.
func TestWatcherStopConcurrent(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour)
	w.Start()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Stop()
		}()
	}
	wg.Wait()

	// The loop is gone: another Stop is still a no-op rather than a panic.
	w.Stop()
}

// TestWatcherStopStopsTheLoop pins that Stop actually ends the goroutine rather
// than merely being safe to call — the reload path replaces the watcher, so a
// stopped one that kept scanning would double up notifications after every
// config change.
func TestWatcherStopStopsTheLoop(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Millisecond)
	w.Start()
	time.Sleep(10 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return; the scan goroutine never exited")
	}
}
