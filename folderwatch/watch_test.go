// ClawEh
// License: MIT

package folderwatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
)

// detect runs one scan of dir for Alice, with her marker under the workspace ws.
func detect(w *Watcher, ws, dir string) []string {
	return w.detectNewFiles("alice", "Alice", dir, markerPath(ws, "alice", dir), false)
}

func TestDetectNewFiles_OnlyNewPathsFire(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil, nil)
	dir, ws := t.TempDir(), t.TempDir()
	old := filepath.Join(dir, "old.md")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First scan with no marker → baseline: nothing fires, the marker is
	// created in the agent's state and nothing is written in the folder.
	if got := detect(w, ws, dir); len(got) != 0 {
		t.Fatalf("baseline should report no new files, got %v", got)
	}
	if _, err := os.Stat(markerPath(ws, "alice", dir)); err != nil {
		t.Fatalf("marker should be created on baseline: %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatalf("the watched folder must hold only its own file, got %v, %v", entries, err)
	}

	// Append to the existing file (new mtime, same path) → must NOT fire.
	if err := os.WriteFile(old, []byte("x-appended"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detect(w, ws, dir); len(got) != 0 {
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
	got := detect(w, ws, dir)
	if len(got) != 1 || got[0] != newFile {
		t.Fatalf("want [%s], got %v", newFile, got)
	}

	// Recorded set advanced → a second scan reports nothing, and editing the
	// now-known new file still does not fire.
	if got := detect(w, ws, dir); len(got) != 0 {
		t.Fatalf("after advancing, expected nothing, got %v", got)
	}
	if err := os.WriteFile(newFile, []byte("y2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detect(w, ws, dir); len(got) != 0 {
		t.Fatalf("editing a now-known file must not fire, got %v", got)
	}
}

func TestDetectNewFiles_IgnoresHidden(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil, nil)
	dir, ws := t.TempDir(), t.TempDir()
	detect(w, ws, dir) // baseline

	// A hidden file (an old in-folder .claw marker among them) is never reported.
	for _, name := range []string{".secret", global.FolderMarkerFile} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("h"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := detect(w, ws, dir); len(got) != 0 {
		t.Fatalf("hidden files must be ignored, got %v", got)
	}
}

// TestWatcherStopIsIdempotent pins the contract the gateway relies on: its
// shutdown path runs on every config reload as well as at exit, so Stop is
// reached more than once for the same Watcher. A second close of the stop
// channel used to panic the whole process ("close of closed channel"), taking
// the gateway down on the second config change of its life.
func TestWatcherStopIsIdempotent(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil, nil)
	w.Start()
	w.Stop()
	w.Stop() // must not panic
}

// TestWatcherStopBeforeStart covers the other order: a Watcher built but never
// started is still stopped by the cleanup path.
func TestWatcherStopBeforeStart(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil, nil)
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
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil, nil)
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
	w := New(func() *config.Config { return nil }, nil, time.Millisecond, nil, nil)
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

// After an upgrade from a version that kept the marker in the folder, the
// first scan finds no marker in the agent's state and records the folder
// silently instead of reporting every file in it; the old in-folder marker is
// neither read nor touched.
func TestDetectNewFiles_UpgradeBaselinesSilently(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil, nil)
	dir, ws := t.TempDir(), t.TempDir()
	for _, name := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := "# claw allowed folder v1\na.md\n"
	oldMarker := filepath.Join(dir, global.FolderMarkerFile)
	if err := os.WriteFile(oldMarker, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := detect(w, ws, dir); len(got) != 0 {
		t.Fatalf("the first scan after the upgrade should baseline, got %v", got)
	}
	data, err := os.ReadFile(markerPath(ws, "alice", dir))
	if err != nil || string(data) != markerHeader+"\n"+dir+"\na.md\nb.md\n" {
		t.Fatalf("marker = %q, %v", data, err)
	}
	if data, err := os.ReadFile(oldMarker); err != nil || string(data) != old {
		t.Fatalf("old in-folder marker = %q, %v; want it untouched", data, err)
	}
}

// A marker recorded for another folder (or a damaged one) is read as missing:
// the scan baselines rather than reporting the folder's files.
func TestDetectNewFiles_ForeignMarkerBaselines(t *testing.T) {
	w := New(func() *config.Config { return nil }, nil, time.Hour, nil, nil)
	dir, ws := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := markerPath(ws, "alice", dir)
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(markerHeader+"\n/elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := detect(w, ws, dir); len(got) != 0 {
		t.Fatalf("a foreign marker should baseline, got %v", got)
	}
}

// A read-only folder is watched like any other: nothing is written in it, no
// alert is raised and a new file is still reported.
func TestDetectNewFiles_ReadOnlyFolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, permission tests don't apply")
	}
	rec := &alertRecorder{}
	w := New(func() *config.Config { return nil }, nil, time.Hour, rec, nil)
	dir, ws := t.TempDir(), t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	if got := detect(w, ws, dir); len(got) != 0 {
		t.Fatalf("baseline should report nothing, got %v", got)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "new.md")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	if got := detect(w, ws, dir); len(got) != 1 || got[0] != file {
		t.Fatalf("want [%s], got %v", file, got)
	}
	if got := detect(w, ws, dir); len(got) != 0 {
		t.Fatalf("a reported file must not be reported again, got %v", got)
	}
	if len(rec.alerts) != 0 {
		t.Fatalf("a read-only folder must not alert, got %+v", rec.alerts)
	}
}

// A new file in an allowed folder with notify on is announced to the owning
// agent's default chat, naming the agent, the folder and the file; a folder
// without notify is not watched.
func TestTick_NotifiesNewFileInAllowedFolder(t *testing.T) {
	watched := t.TempDir()
	quiet := t.TempDir()
	cfg := &config.Config{}
	cfg.Agents.BaseDir = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "alice", Name: "Alice", AllowedFolders: []config.AllowedFolderConfig{
		{Path: watched, Notify: true}, {Path: quiet},
	}}}
	cfg.Bindings = []config.AgentBinding{{
		AgentID: "alice", Default: true,
		Match: config.BindingMatch{Channel: "telegram", Peer: &config.PeerMatch{Kind: "direct", ID: "42"}},
	}}
	b := bus.NewMessageBus()
	w := New(func() *config.Config { return cfg }, b, time.Hour, nil, nil)

	w.tick() // baseline
	ws := filepath.Join(cfg.Agents.BaseDir, "alice")
	if _, err := os.Stat(markerPath(ws, "alice", watched)); err != nil {
		t.Fatalf("the watched folder has no marker in Alice's state: %v", err)
	}
	if _, err := os.Stat(markerPath(ws, "alice", quiet)); !os.IsNotExist(err) {
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

// Two agents watching the same folder each keep their own marker, so both are
// told about a new file.
func TestTick_TwoAgentsOnOneFolderBothNotified(t *testing.T) {
	shared := t.TempDir()
	cfg := &config.Config{}
	cfg.Agents.BaseDir = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "alice", Name: "Alice", AllowedFolders: []config.AllowedFolderConfig{{Path: shared, Notify: true}}},
		{ID: "bob", Name: "Bob", AllowedFolders: []config.AllowedFolderConfig{{Path: shared, Notify: true}}},
	}
	cfg.Bindings = []config.AgentBinding{
		{AgentID: "alice", Default: true, Match: config.BindingMatch{Channel: "telegram", Peer: &config.PeerMatch{Kind: "direct", ID: "1"}}},
		{AgentID: "bob", Default: true, Match: config.BindingMatch{Channel: "telegram", Peer: &config.PeerMatch{Kind: "direct", ID: "2"}}},
	}
	b := bus.NewMessageBus()
	w := New(func() *config.Config { return cfg }, b, time.Hour, nil, nil)

	w.tick() // baseline for both
	file := filepath.Join(shared, "report.md")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.tick()

	got := map[string]string{}
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		msg, ok := b.ConsumeInbound(ctx)
		cancel()
		if !ok {
			t.Fatalf("expected two notices, got %v", got)
		}
		got[msg.ChatID] = msg.Content
	}
	want := map[string]string{
		"1": "New file in Alice's allowed folder " + shared + ": " + file,
		"2": "New file in Bob's allowed folder " + shared + ": " + file,
	}
	if got["1"] != want["1"] || got["2"] != want["2"] {
		t.Fatalf("notices = %v, want %v", got, want)
	}
}

// A folder whose path holds a line break is not watched (it is listed as
// ignored instead); its list could never match it.
func TestNotifyFolders_SkipsControlCharacters(t *testing.T) {
	dir := t.TempDir()
	agent := &config.AgentConfig{ID: "alice", AllowedFolders: []config.AllowedFolderConfig{
		{Path: dir + "\nx", Notify: true}, {Path: dir, Notify: true},
	}}
	if got := notifyFolders(agent); len(got) != 1 || got[0] != dir {
		t.Fatalf("notifyFolders = %q, want only %q", got, dir)
	}
}

// Agents sharing a workspace still keep one marker each for the same folder.
func TestMarkerPath_PerAgent(t *testing.T) {
	if markerPath("/ws", "alice", "/srv/src") == markerPath("/ws", "bob", "/srv/src") {
		t.Fatal("two agents in one workspace share a marker")
	}
	if markerPath("/ws", "alice", "/srv/a") == markerPath("/ws", "alice", "/srv/b") {
		t.Fatal("two folders share a marker")
	}
}

// watchEnv is Alice with one folder and a default chat, and a State shared by
// the watchers built over it, as the gateway shares one across reloads.
type watchEnv struct {
	cfg    *config.Config
	folder string
	ws     string
	bus    *bus.MessageBus
	state  *State
}

func newWatchEnv(t *testing.T) *watchEnv {
	t.Helper()
	e := &watchEnv{cfg: &config.Config{}, folder: t.TempDir(), bus: bus.NewMessageBus(), state: NewState()}
	e.cfg.Agents.BaseDir = t.TempDir()
	e.ws = filepath.Join(e.cfg.Agents.BaseDir, "alice")
	e.cfg.Agents.List = []config.AgentConfig{{ID: "alice", Name: "Alice"}}
	e.cfg.Bindings = []config.AgentBinding{{
		AgentID: "alice", Default: true,
		Match: config.BindingMatch{Channel: "telegram", Peer: &config.PeerMatch{Kind: "direct", ID: "42"}},
	}}
	e.setFolders(config.AllowedFolderConfig{Path: e.folder, Notify: true})
	if err := os.WriteFile(filepath.Join(e.folder, "old.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *watchEnv) setFolders(f ...config.AllowedFolderConfig) {
	e.cfg.Agents.List[0].AllowedFolders = f
}

// watcher is the watcher a (re)load builds: a new one over the same State.
func (e *watchEnv) watcher() *Watcher {
	return New(func() *config.Config { return e.cfg }, e.bus, time.Hour, nil, e.state)
}

func (e *watchEnv) add(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(e.folder, name)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// notices drains the bus and returns the notices' contents.
func (e *watchEnv) notices() []string {
	var out []string
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		msg, ok := e.bus.ConsumeInbound(ctx)
		cancel()
		if !ok {
			return out
		}
		out = append(out, msg.Content)
	}
}

// A folder removed and added again, or whose notices are turned off and on,
// starts with a new list: files added meanwhile are not announced, and its
// old list is deleted while it is not watched.
func TestTick_FolderWatchedAgainStartsOver(t *testing.T) {
	for _, tc := range []struct {
		name string
		off  func(e *watchEnv)
	}{
		{"removed and added again", func(e *watchEnv) { e.setFolders() }},
		{"notices off and on", func(e *watchEnv) {
			e.setFolders(config.AllowedFolderConfig{Path: e.folder})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newWatchEnv(t)
			e.watcher().tick() // baseline
			marker := markerPath(e.ws, "alice", e.folder)
			if _, err := os.Stat(marker); err != nil {
				t.Fatal(err)
			}

			tc.off(e)
			e.watcher().tick()
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("the list of a folder no longer watched was kept: %v", err)
			}
			e.add(t, "while-off-1.md")

			e.setFolders(config.AllowedFolderConfig{Path: e.folder, Notify: true})
			e.watcher().tick()
			if got := e.notices(); len(got) != 0 {
				t.Fatalf("watching again announced %v", got)
			}
			file := e.add(t, "after.md")
			e.watcher().tick()
			if got := e.notices(); len(got) != 1 || !strings.HasSuffix(got[0], file) {
				t.Fatalf("notices = %v, want one for %s", got, file)
			}
		})
	}
}

// A folder that appears in the configuration while ClawEh runs starts over
// even when an old list of it is still on disk (written before the process
// lost track of it); at process start an existing list is read, so a file
// added while ClawEh was stopped is announced.
func TestTick_NewFolderAtRuntimeIgnoresOldList(t *testing.T) {
	e := newWatchEnv(t)
	marker := markerPath(e.ws, "alice", e.folder)
	stale := markerHeader + "\n" + e.folder + "\n"
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}

	// Process start: the old list (empty) is read, so old.md is new.
	if err := os.WriteFile(marker, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	e.watcher().tick()
	if got := e.notices(); len(got) != 1 || !strings.HasSuffix(got[0], "old.md") {
		t.Fatalf("at start: notices = %v, want old.md", got)
	}

	// Running, the folder not watched, then added with a stale list on disk
	// (as if restored): it baselines instead of reading it.
	e.setFolders()
	e.watcher().tick()
	if err := os.WriteFile(marker, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	e.setFolders(config.AllowedFolderConfig{Path: e.folder, Notify: true})
	e.watcher().tick()
	if got := e.notices(); len(got) != 0 {
		t.Fatalf("a folder added at run time announced %v", got)
	}
}

// Two agents sharing one workspace spelled differently (a trailing slash, a
// linked alias) keep each other's markers: both keep getting notices.
func TestTick_SharedWorkspaceSpelledTwoWays(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alias func(t *testing.T, ws string) string
	}{
		{"trailing slash", func(_ *testing.T, ws string) string { return ws + "/" }},
		{"linked alias", func(t *testing.T, ws string) string {
			t.Helper()
			link := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(ws, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newWatchEnv(t)
			ws := filepath.Join(e.cfg.Agents.BaseDir, "shared")
			if err := os.MkdirAll(ws, 0o700); err != nil {
				t.Fatal(err)
			}
			bobFolder := t.TempDir()
			e.cfg.Agents.List[0].Workspace = ws
			e.cfg.Agents.List = append(e.cfg.Agents.List, config.AgentConfig{
				ID: "bob", Name: "Bob", Workspace: tc.alias(t, ws),
				AllowedFolders: []config.AllowedFolderConfig{{Path: bobFolder, Notify: true}},
			})
			e.cfg.Bindings = append(e.cfg.Bindings, config.AgentBinding{
				AgentID: "bob", Default: true,
				Match: config.BindingMatch{Channel: "telegram", Peer: &config.PeerMatch{Kind: "direct", ID: "43"}},
			})
			w := e.watcher()
			w.tick() // baselines
			w.tick()
			for _, m := range []string{markerPath(ws, "alice", e.folder), markerPath(ws, "bob", bobFolder)} {
				if _, err := os.Stat(m); err != nil {
					t.Fatalf("a marker was removed: %v", err)
				}
			}
			a := e.add(t, "a.md")
			b := filepath.Join(bobFolder, "b.md")
			if err := os.WriteFile(b, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			w.tick()
			got := strings.Join(e.notices(), "\n")
			if !strings.Contains(got, a) || !strings.Contains(got, b) {
				t.Fatalf("notices = %q, want both agents told", got)
			}
		})
	}
}

// Markers of another agent sharing the workspace, and files that are not
// markers, are left alone.
func TestTick_KeepsOtherMarkers(t *testing.T) {
	e := newWatchEnv(t)
	e.cfg.Agents.List[0].Workspace = filepath.Join(e.cfg.Agents.BaseDir, "shared")
	e.cfg.Agents.List = append(e.cfg.Agents.List, config.AgentConfig{
		ID: "bob", Name: "Bob", Workspace: e.cfg.Agents.List[0].Workspace,
		AllowedFolders: []config.AllowedFolderConfig{{Path: e.folder, Notify: true}},
	})
	ws := e.cfg.Agents.List[0].Workspace
	e.watcher().tick()
	other := filepath.Join(ws, markerDir, "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.setFolders() // Alice stops watching; Bob still does
	e.watcher().tick()
	if _, err := os.Stat(markerPath(ws, "bob", e.folder)); err != nil {
		t.Errorf("Bob's list was removed: %v", err)
	}
	if _, err := os.Stat(markerPath(ws, "alice", e.folder)); !os.IsNotExist(err) {
		t.Errorf("Alice's list was kept: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a file that is not a list was removed: %v", err)
	}
}
