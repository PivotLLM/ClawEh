// ClawEh
// License: MIT

package agentreg

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
)

// gate blocks the build of the spec it matches until released, and says when
// that build has started.
type gate struct {
	match   func(Spec) bool
	started chan struct{}
	release chan struct{}
	once    atomic.Bool
}

func newGate(match func(Spec) bool) *gate {
	return &gate{match: match, started: make(chan struct{}), release: make(chan struct{})}
}

func (g *gate) onBuild(spec Spec) {
	if g.match(spec) && g.once.CompareAndSwap(false, true) {
		close(g.started)
		<-g.release
	}
}

func waitOrFail(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func notWithin(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return false
	case <-time.After(100 * time.Millisecond):
		return true
	}
}

// A Create that started before a Reload is finished before the reload
// commits, and the reload then rebuilds the new agent against the new
// configuration: nothing is kept on the old one.
func TestCreate_OrderedBeforeReload(t *testing.T) {
	h := newFakeHost()
	r := mustNew(t, testConfig(t), h)
	g := newGate(func(s Spec) bool { return s.IsClone() })
	h.onBuild = g.onBuild
	next := testConfig(t)
	next.Agents.List[0].Tools = []string{"file_read"}

	created := make(chan string, 1)
	go func() {
		id, err := r.Create(config.AgentConfig{}, CloneOf("alice"))
		if err != nil {
			t.Errorf("Create: %v", err)
		}
		created <- id
	}()
	waitOrFail(t, g.started, "the clone build")

	var committed atomic.Bool
	reloaded := make(chan struct{})
	go func() {
		defer close(reloaded)
		if err := r.Reload(context.Background(), next, h.build, func() bool { committed.Store(true); return true }); err != nil {
			t.Errorf("Reload: %v", err)
		}
	}()
	if !notWithin(reloaded) || committed.Load() {
		t.Fatal("a reload committed while a creation was building")
	}
	close(g.release)
	id := <-created
	waitOrFail(t, reloaded, "the reload")
	if c := mustGet(t, r, id); !slices.Equal(c.spec.Config.Tools, []string{"file_read"}) {
		t.Fatalf("clone config tools = %v, want the reloaded alice's", c.spec.Config.Tools)
	}
}

// A Create that starts while a Reload is building waits for it and builds
// against the new configuration.
func TestCreate_OrderedAfterReload(t *testing.T) {
	h := newFakeHost()
	r := mustNew(t, testConfig(t), h)
	g := newGate(func(s Spec) bool { return s.ID == "alice" })
	next := testConfig(t)
	next.Agents.List[0].Tools = []string{"file_read"}

	reloaded := make(chan struct{})
	go func() {
		defer close(reloaded)
		build := func(c *config.Config, s Spec) (*fakeInst, error) {
			g.onBuild(s)
			return h.build(c, s)
		}
		if err := r.Reload(context.Background(), next, build, commitOK); err != nil {
			t.Errorf("Reload: %v", err)
		}
	}()
	waitOrFail(t, g.started, "the reload's build")

	created := make(chan struct{})
	var id string
	go func() {
		defer close(created)
		var err error
		if id, err = r.Create(config.AgentConfig{}, CloneOf("alice")); err != nil {
			t.Errorf("Create: %v", err)
		}
	}()
	if !notWithin(created) {
		t.Fatal("a creation ran while a reload was building")
	}
	close(g.release)
	waitOrFail(t, reloaded, "the reload")
	waitOrFail(t, created, "the creation")
	if c := mustGet(t, r, id); !slices.Equal(c.spec.Config.Tools, []string{"file_read"}) {
		t.Fatalf("clone config tools = %v, want the reloaded alice's", c.spec.Config.Tools)
	}
	if h.buildCount(id) != 1 {
		t.Fatalf("clone built %d times, want once", h.buildCount(id))
	}
}

// A non-owner creates its temporary agents in a private root, never under
// the owner's internal/temp, and removes that root when it closes.
func TestNonOwner_PrivateRoot(t *testing.T) {
	cfg := testConfig(t)
	shared := filepath.Join(cfg.DataDir(), global.InternalDir, TempDirName)
	r, err := New(cfg, Hooks[*fakeInst]{Build: newFakeHost().build})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r.TempRoot() != "" {
		t.Fatal("a non-owner made a temp root before creating anything")
	}
	id := mustCreate(t, r, config.AgentConfig{}, CloneOf("alice"), EphemeralMemory())
	root := r.TempRoot()
	state := mustGet(t, r, id).spec.StateDir
	if root == "" || strings.HasPrefix(state, shared) || !strings.HasPrefix(state, root) {
		t.Fatalf("state dir %q, root %q: want a private root outside %q", state, root, shared)
	}
	if dirExists(shared) {
		t.Fatal("a non-owner created the shared temp root")
	}
	r.Close()
	if dirExists(root) {
		t.Fatal("the private root survived Close")
	}
}

// An entry put back after a failed release whose id is taken meanwhile is
// closed, not leaked.
func TestPutBack_TakenIDClosesInstance(t *testing.T) {
	h := newFakeHost()
	r := mustNew(t, testConfig(t), h)
	id := mustCreate(t, r, config.AgentConfig{})
	r.mu.Lock()
	e := r.entries[id]
	r.entries[id] = &entry[*fakeInst]{inst: &fakeInst{spec: e.spec}, spec: e.spec, meta: e.meta}
	r.mu.Unlock()
	r.putBack(e)
	if !e.inst.closed() {
		t.Fatal("an instance with nowhere to go back to was leaked")
	}
}

// Only the owner of the data directory restores, cleans up and saves; any
// other process sharing it (`claw agent` beside the service) leaves the
// service's temporary agents and their list alone.
func TestOwner_OnlyOwnerTouchesSharedState(t *testing.T) {
	cfg := testConfig(t)
	statePath := filepath.Join(cfg.DataDir(), global.InternalDir, StateFileName)
	tempRoot := filepath.Join(cfg.DataDir(), global.InternalDir, TempDirName)

	owner := mustNew(t, cfg, newFakeHost())
	saved := mustCreate(t, owner, config.AgentConfig{})
	inFlight := mustCreate(t, owner, config.AgentConfig{}, CloneOf("alice"), EphemeralMemory())
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("owner did not save: %v", err)
	}

	// A second, non-owner process on the same data directory.
	other, err := New(cfg, Hooks[*fakeInst]{Build: newFakeHost().build})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := other.Get(saved); ok {
		t.Fatal("a non-owner restored the owner's temporary agents")
	}
	if !dirExists(filepath.Join(tempRoot, inFlight)) || !dirExists(filepath.Join(tempRoot, saved)) {
		t.Fatal("a non-owner removed the owner's temporary agent directories")
	}
	mine, err := other.Create(config.AgentConfig{})
	if err != nil {
		t.Fatalf("non-owner Create: %v", err)
	}
	if err = other.Delete(mine); err != nil {
		t.Fatalf("non-owner Delete: %v", err)
	}
	after, err := os.ReadFile(statePath)
	if err != nil || string(after) != string(before) {
		t.Fatal("a non-owner rewrote temp_agents.json")
	}

	// The owner, restarted, restores and cleans as before.
	again := mustNew(t, cfg, newFakeHost())
	if _, ok := again.Get(saved); !ok {
		t.Fatal("the owner did not restore its saved agent")
	}
	if dirExists(filepath.Join(tempRoot, inFlight)) {
		t.Fatal("the owner did not remove the unsaved directory at start")
	}
}

// A turn that resolved an instance which was then replaced or deleted is
// refused, so it never runs on a closed instance.
func TestBeginTurn_RefusesStaleInstance(t *testing.T) {
	h := newFakeHost()
	r := mustNew(t, testConfig(t), h)
	id := mustCreate(t, r, config.AgentConfig{})
	stale := mustGet(t, r, id)
	if err := r.Reload(context.Background(), testConfig(t), h.build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if end, ok := r.BeginTurn(id, stale); ok {
		end()
		t.Fatal("a turn began on a replaced instance")
	}
	cur := mustGet(t, r, id)
	end, ok := r.BeginTurn(id, cur)
	if !ok {
		t.Fatal("a turn on the current instance was refused")
	}
	end()
	if err := r.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := r.BeginTurn(id, cur); ok {
		t.Fatal("a turn began on a deleted agent")
	}
}

// An agent that cannot be released while the registry is closing is closed,
// not leaked.
func TestRetireFailureAfterClose_ClosesInstance(t *testing.T) {
	h := newFakeHost()
	r := mustNew(t, testConfig(t), h)
	id := mustCreate(t, r, config.AgentConfig{})
	inst := mustGet(t, r, id)
	h.failRetire = true
	r.mu.Lock()
	e := r.entries[id]
	delete(r.entries, id)
	r.mu.Unlock()
	r.Close()
	r.dispose(e, "test") // retire fails after Close has run
	if n := inst.closes.Load(); n != 1 {
		t.Fatalf("instance closed %d times, want 1", n)
	}

	// A replaced instance that cannot be released is closed by Close.
	h2 := newFakeHost()
	r2 := mustNew(t, testConfig(t), h2)
	id2 := mustCreate(t, r2, config.AgentConfig{})
	old := mustGet(t, r2, id2)
	h2.failRetire = true
	if err := r2.Reload(context.Background(), testConfig(t), h2.build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if old.closed() {
		t.Fatal("an instance that could not be released was closed under its holder")
	}
	r2.Close()
	if n := old.closes.Load(); n != 1 {
		t.Fatalf("replaced instance closed %d times by Close, want 1", n)
	}
}
