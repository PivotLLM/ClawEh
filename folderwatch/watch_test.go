// ClawEh
// License: MIT

package folderwatch

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
)

func TestDetectNewFiles_OnlyNewPathsFire(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil)
	dir := t.TempDir()
	old := filepath.Join(dir, "old.md")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First scan with no marker → baseline: nothing fires, .claw created.
	if got := w.detectNewFiles("Alice", dir); len(got) != 0 {
		t.Fatalf("baseline should report no new files, got %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, markerFile)); err != nil {
		t.Fatalf(".claw marker should be created on baseline: %v", err)
	}

	// Append to the existing file (new mtime, same path) → must NOT fire.
	if err := os.WriteFile(old, []byte("x-appended"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := w.detectNewFiles("Alice", dir); len(got) != 0 {
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
	got := w.detectNewFiles("Alice", dir)
	if len(got) != 1 || got[0] != newFile {
		t.Fatalf("want [%s], got %v", newFile, got)
	}

	// Recorded set advanced → a second scan reports nothing, and editing the
	// now-known new file still does not fire.
	if got := w.detectNewFiles("Alice", dir); len(got) != 0 {
		t.Fatalf("after advancing, expected nothing, got %v", got)
	}
	if err := os.WriteFile(newFile, []byte("y2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := w.detectNewFiles("Alice", dir); len(got) != 0 {
		t.Fatalf("editing a now-known file must not fire, got %v", got)
	}
}

func TestDetectNewFiles_IgnoresMarkerAndHidden(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil)
	dir := t.TempDir()
	w.detectNewFiles("Alice", dir) // baseline, creates .claw

	// A hidden file and the marker itself must never be reported.
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("h"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := w.detectNewFiles("Alice", dir); len(got) != 0 {
		t.Fatalf("hidden files and the marker must be ignored, got %v", got)
	}
}

// TestWatcherStopIsIdempotent pins the contract the gateway relies on: its
// shutdown path runs on every config reload as well as at exit, so Stop is
// reached more than once for the same Watcher. A second close of the stop
// channel used to panic the whole process ("close of closed channel"), taking
// the gateway down on the second config change of its life.
func TestWatcherStopIsIdempotent(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil)
	w.Start()
	w.Stop()
	w.Stop() // must not panic
}

// TestWatcherStopBeforeStart covers the other order: a Watcher built but never
// started is still stopped by the cleanup path.
func TestWatcherStopBeforeStart(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil)
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
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil)
	w.Start()

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			w.Stop()
		})
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
	w := New(func() *config.Config { return nil }, nil, time.Millisecond, nil)
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

// A marker written for a mount (no header, entries prefixed with the mount's
// name) is read as missing: the first scan records the folder and reports
// nothing, instead of reporting every file in it once.
func TestDetectNewFiles_MountMarkerBaselines(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, markerFile), []byte("notes/a.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := w.detectNewFiles("Alice", dir); len(got) != 0 {
		t.Fatalf("a mount marker should baseline, got %v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, markerFile))
	if err != nil || string(data) != markerHeader+"\na.md\n" {
		t.Fatalf("marker = %q, %v", data, err)
	}
}

// A new file in an allowed folder with notify on is announced to the owning
// agent's default chat, naming the agent, the folder and the file; a folder
// without notify is not watched.
func TestTick_NotifiesNewFileInAllowedFolder(t *testing.T) {
	watched := t.TempDir()
	quiet := t.TempDir()
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{{ID: "alice", Name: "Alice", AllowedFolders: []config.AllowedFolderConfig{
		{Path: watched, Notify: true}, {Path: quiet},
	}}}
	cfg.Bindings = []config.AgentBinding{{
		AgentID: "alice", Default: true,
		Match: config.BindingMatch{Channel: "telegram", Peer: &config.PeerMatch{Kind: "direct", ID: "42"}},
	}}
	b := bus.NewMessageBus()
	w := New(func() *config.Config { return cfg }, b, time.Hour, nil)

	w.tick() // baseline
	if _, err := os.Stat(filepath.Join(quiet, markerFile)); !os.IsNotExist(err) {
		t.Fatal("a folder without notify got a marker")
	}
	file := filepath.Join(watched, "report.md")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(quiet, "other.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.tick()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, ok := b.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("no notice published")
	}
	want := "New file in Alice's allowed folder " + watched + ": " + file
	if msg.Content != want || msg.Channel != "telegram" || msg.ChatID != "42" || !msg.Internal {
		t.Fatalf("notice = %+v, want %q to telegram/42", msg, want)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if extra, ok := b.ConsumeInbound(ctx2); ok {
		t.Fatalf("unexpected second notice: %+v", extra)
	}
}
