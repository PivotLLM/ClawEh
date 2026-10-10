// ClawEh
// License: MIT

package agentreg

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
)

// fakeInst is a registry instance that records its spec and how often it was
// closed.
type fakeInst struct {
	spec   Spec
	closes atomic.Int32
}

func (f *fakeInst) Close() error { f.closes.Add(1); return nil }

func (f *fakeInst) closed() bool { return f.closes.Load() > 0 }

// fakeHost builds fakeInsts, counts builds per id, records every instance it
// built and the retirements, and can be told to fail.
type fakeHost struct {
	mu         sync.Mutex
	builds     map[string]int
	built      []*fakeInst
	retired    []string
	failID     string // building this id fails
	failRetire bool
	onBuild    func(Spec) // called (unlocked) before each build returns
}

func newFakeHost() *fakeHost { return &fakeHost{builds: map[string]int{}} }

func (h *fakeHost) build(_ *config.Config, spec Spec) (*fakeInst, error) {
	if h.onBuild != nil {
		h.onBuild(spec)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if spec.ID == h.failID {
		return nil, errors.New("build refused")
	}
	h.builds[spec.ID]++
	inst := &fakeInst{spec: spec}
	h.built = append(h.built, inst)
	return inst, nil
}

func (h *fakeHost) retire(spec Spec, _ *fakeInst) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failRetire {
		return errors.New("session in use")
	}
	h.retired = append(h.retired, spec.ID)
	return nil
}

func (h *fakeHost) buildCount(id string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.builds[id]
}

func (h *fakeHost) instances() []*fakeInst {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.built)
}

func (h *fakeHost) hooks() Hooks[*fakeInst] {
	return Hooks[*fakeInst]{Build: h.build, Retire: h.retire, Owner: true}
}

func commitOK() bool { return true }

// testConfig is a configuration with a data directory (CLAW_HOME), agents
// alice (default, model m1) and bob, and the model m1.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv(global.EnvVarHome, t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "alice", Name: "Alice", Default: true, Models: []string{"m1"}, Tools: []string{"file_read", "file_write"}},
		{ID: "bob", Name: "Bob"},
	}
	cfg.Models = []config.ModelConfig{{ModelName: "m1", Model: "model-one", Provider: "p", Enabled: true}}
	return cfg
}

func mustNew(t *testing.T, cfg *config.Config, h *fakeHost) *Registry[*fakeInst] {
	t.Helper()
	r, err := New(cfg, h.hooks())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func mustFresh(t *testing.T, r *Registry[*fakeInst], cfg config.AgentConfig, opts ...FreshOption) string {
	t.Helper()
	id, err := r.CreateFresh(cfg, opts...)
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	return id
}

func mustClone(t *testing.T, r *Registry[*fakeInst], sourceID string, opts ...CloneOption) string {
	t.Helper()
	id, err := r.CreateClone(sourceID, opts...)
	if err != nil {
		t.Fatalf("CreateClone: %v", err)
	}
	return id
}

func mustGet(t *testing.T, r *Registry[*fakeInst], id string) *fakeInst {
	t.Helper()
	inst, ok := r.Get(id)
	if !ok {
		t.Fatalf("%s not registered", id)
	}
	return inst
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func TestConfigAgents(t *testing.T) {
	r := mustNew(t, testConfig(t), newFakeHost())
	if got := r.List(); len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("List = %v, want [alice bob] in configuration order", got)
	}
	if r.DefaultID() != "alice" || r.Default().spec.ID != "alice" {
		t.Fatalf("default = %q, want alice", r.DefaultID())
	}
	info, ok := r.Info("alice")
	if !ok || info.Spec.Origin != OriginConfig || info.Spec.StateDir != info.Spec.Workspace {
		t.Fatalf("config agent info = %+v; want origin config, state dir == workspace", info)
	}
	if len(r.ListTemp()) != 0 {
		t.Fatalf("ListTemp = %v, want none", r.ListTemp())
	}
}

func TestCreateFresh_GetListDelete(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)

	id := mustFresh(t, r, config.AgentConfig{ID: "ignored", Name: "Scratch", Models: []string{"m1"}})
	if len(id) != 36 || id == "ignored" {
		t.Fatalf("id = %q, want a UUID", id)
	}
	inst := mustGet(t, r, id)
	spec := inst.spec
	wantState := filepath.Join(cfg.DataDir(), global.InternalDir, TempDirName, id)
	if spec.Origin != OriginTemp || !spec.Fresh || spec.IsClone() || spec.Config.ID != id || spec.Config.Name != "Scratch" {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.StateDir != wantState || spec.Workspace != filepath.Join(wantState, "workspace") {
		t.Fatalf("dirs = %q / %q, want %q and its workspace/", spec.StateDir, spec.Workspace, wantState)
	}
	if info, err := os.Stat(spec.StateDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state dir must exist with mode 0700: %v %v", info, err)
	}
	if info, _ := r.Info(id); info.TTL != DefaultTTL || info.Created.IsZero() || info.LastUsed.IsZero() {
		t.Fatalf("info = %+v, want the default TTL and timestamps", info)
	}

	// Temporary agents are never what operators or routing see.
	if got := r.List(); len(got) != 2 {
		t.Fatalf("List = %v, must hold config agents only", got)
	}
	if got := r.ListTemp(); len(got) != 1 || got[0] != id {
		t.Fatalf("ListTemp = %v, want [%s]", got, id)
	}
	if got := r.All(); len(got) != 3 || got[2] != id {
		t.Fatalf("All = %v, want the config agents then %s", got, id)
	}
	if _, ok := r.GetConfigured(id); ok {
		t.Fatal("GetConfigured must not find a temporary agent")
	}
	if r.DefaultID() != "alice" {
		t.Fatal("a temporary agent must never become the default")
	}

	if err := r.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := r.Get(id); ok {
		t.Fatal("deleted agent still found")
	}
	if dirExists(spec.StateDir) {
		t.Fatal("deleted agent's directory still exists")
	}
	if !inst.closed() {
		t.Fatal("deleted agent's instance was not closed")
	}
	if len(h.retired) != 1 || h.retired[0] != id {
		t.Fatalf("retired = %v, want the host to release %s before removal", h.retired, id)
	}
}

func TestCreate_Refusals(t *testing.T) {
	r := mustNew(t, testConfig(t), newFakeHost())
	if _, err := r.CreateFresh(config.AgentConfig{Models: []string{"gone"}}); err == nil {
		t.Error("an agent naming an unconfigured model must be refused")
	}
	if _, err := r.CreateFresh(config.AgentConfig{}, Temp(0)); err == nil {
		t.Error("a zero TTL must be refused")
	}
	if _, err := r.CreateClone("alice", Temp(0)); err == nil {
		t.Error("a zero TTL must be refused for a clone")
	}
	if _, err := r.CreateClone(""); !errors.Is(err, ErrNotFound) {
		t.Errorf("clone of no agent: err = %v, want ErrNotFound", err)
	}
	if _, err := r.CreateClone("nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("clone of an unknown agent: err = %v, want ErrNotFound", err)
	}
	temp := mustFresh(t, r, config.AgentConfig{})
	if _, err := r.CreateClone(temp); !errors.Is(err, ErrNotFound) {
		t.Errorf("clone of a temporary agent: err = %v, want ErrNotFound", err)
	}
	if err := r.Delete("alice"); !errors.Is(err, ErrNotTemp) {
		t.Errorf("Delete(config agent) = %v, want ErrNotTemp", err)
	}
	if err := r.Delete("nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(unknown) = %v, want ErrNotFound", err)
	}
}

func TestCreateClone(t *testing.T) {
	r := mustNew(t, testConfig(t), newFakeHost())
	id := mustClone(t, r, "alice", EphemeralMemory(), Temp(time.Hour))
	spec := mustGet(t, r, id).spec
	alice := mustGet(t, r, "alice")
	if !spec.IsClone() || spec.SourceID != "alice" || !spec.Ephemeral || spec.Fresh {
		t.Fatalf("spec = %+v, want an ephemeral clone of alice", spec)
	}
	if spec.Workspace != alice.spec.Workspace {
		t.Fatalf("clone workspace = %q, want alice's %q", spec.Workspace, alice.spec.Workspace)
	}
	if spec.StateDir == alice.spec.StateDir || !strings.Contains(spec.StateDir, TempDirName) {
		t.Fatalf("clone state dir = %q, want its own under internal/temp", spec.StateDir)
	}
	if spec.Config.ID != id || spec.Config.Default || spec.Config.Name != "Alice" || len(spec.Config.Models) != 1 {
		t.Fatalf("clone config = %+v, want a copy of alice's with its own id", spec.Config)
	}
	// The copy is deep: changing it does not change alice.
	spec.Config.Models[0] = "changed"
	if alice.spec.Config.Models[0] != "m1" {
		t.Fatal("the clone's configuration aliases alice's")
	}
	if got := spec.Label(); got != "alice (clone "+id[:8]+")" {
		t.Fatalf("Label = %q", got)
	}
	if r.HomeID(id) != "alice" || r.HomeID("bob") != "bob" {
		t.Fatalf("HomeID: clone → %q, bob → %q", r.HomeID(id), r.HomeID("bob"))
	}
	if info, _ := r.Info(id); info.TTL != time.Hour {
		t.Fatalf("TTL = %s, want 1h", info.TTL)
	}
}

// The Inserted hook runs once the new agent is visible.
func TestCreate_InsertedHookSeesAgent(t *testing.T) {
	h := newFakeHost()
	var r *Registry[*fakeInst]
	var seen []string
	hooks := h.hooks()
	hooks.Inserted = func(spec Spec, _ *fakeInst) {
		if _, ok := r.Get(spec.ID); !ok {
			t.Errorf("Inserted ran before %s was registered", spec.ID)
		}
		seen = append(seen, spec.ID)
	}
	var err error
	if r, err = New(testConfig(t), hooks); err != nil {
		t.Fatalf("New: %v", err)
	}
	id := mustClone(t, r, "alice")
	if len(seen) != 1 || seen[0] != id {
		t.Fatalf("Inserted saw %v, want [%s]", seen, id)
	}
}

// Creations build in parallel: nothing registry-wide is held while one builds.
func TestCreate_Concurrent(t *testing.T) {
	const n = 6
	h := newFakeHost()
	r := mustNew(t, testConfig(t), h)
	var inBuild atomic.Int32
	all := make(chan struct{})
	h.onBuild = func(Spec) {
		if inBuild.Add(1) == n {
			close(all)
		}
		select {
		case <-all:
		case <-time.After(5 * time.Second):
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			if _, err := r.CreateClone("alice", EphemeralMemory()); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Create: %v", err)
	}
	select {
	case <-all:
	default:
		t.Fatalf("only %d of %d creations were building at once", inBuild.Load(), n)
	}
	if got := len(r.ListTemp()); got != n {
		t.Fatalf("ListTemp has %d agents, want %d", got, n)
	}
}

func TestDeleteRefusedMidTurn(t *testing.T) {
	r := mustNew(t, testConfig(t), newFakeHost())
	id := mustFresh(t, r, config.AgentConfig{})
	end, _ := r.BeginTurn(id, mustGet(t, r, id))
	if info, _ := r.Info(id); !info.InTurn {
		t.Fatal("Info must report the turn")
	}
	if err := r.Delete(id); !errors.Is(err, ErrBusy) {
		t.Fatalf("Delete mid-turn = %v, want ErrBusy", err)
	}
	if _, ok := r.Get(id); !ok {
		t.Fatal("a refused delete removed the agent")
	}
	end()
	end() // ending twice is harmless
	if err := r.Delete(id); err != nil {
		t.Fatalf("Delete after the turn: %v", err)
	}
}

// DeleteWhenIdle deletes an idle agent at once and one in a turn as soon as
// its last turn ends; it refuses what Delete refuses otherwise.
func TestDeleteWhenIdle(t *testing.T) {
	r := mustNew(t, testConfig(t), newFakeHost())

	idle := mustFresh(t, r, config.AgentConfig{})
	if err := r.DeleteWhenIdle(idle); err != nil {
		t.Fatalf("DeleteWhenIdle of an idle agent: %v", err)
	}
	if _, ok := r.Get(idle); ok {
		t.Fatal("an idle agent was not deleted at once")
	}

	busy := mustFresh(t, r, config.AgentConfig{})
	end1, _ := r.BeginTurn(busy, mustGet(t, r, busy))
	end2, _ := r.BeginTurn(busy, mustGet(t, r, busy))
	if err := r.DeleteWhenIdle(busy); !errors.Is(err, ErrDeletePending) {
		t.Fatalf("DeleteWhenIdle mid-turn = %v, want ErrDeletePending", err)
	}
	if _, ok := r.Get(busy); !ok {
		t.Fatal("an agent in a turn was deleted")
	}
	end1()
	time.Sleep(50 * time.Millisecond)
	if _, ok := r.Get(busy); !ok {
		t.Fatal("deleted while another turn was running")
	}
	end2()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := r.Get(busy); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not deleted after its last turn ended")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := r.DeleteWhenIdle("alice"); !errors.Is(err, ErrNotTemp) {
		t.Errorf("DeleteWhenIdle of a config agent = %v, want ErrNotTemp", err)
	}
	if err := r.DeleteWhenIdle("no-such-agent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteWhenIdle of an unknown agent = %v, want ErrNotFound", err)
	}
}

// CreateCloneInTurn begins the turn before the agent is visible: it cannot be
// deleted until the turn ends.
func TestCreateCloneInTurn(t *testing.T) {
	r := mustNew(t, testConfig(t), newFakeHost())
	id, end, err := r.CreateCloneInTurn("alice")
	if err != nil {
		t.Fatalf("CreateCloneInTurn: %v", err)
	}
	if err := r.Delete(id); !errors.Is(err, ErrBusy) {
		t.Fatalf("Delete of a just-created agent = %v, want ErrBusy", err)
	}
	end()
	if err := r.Delete(id); err != nil {
		t.Fatalf("Delete after the turn: %v", err)
	}
}

func TestDelete_KeepsEntryWhenNotReleased(t *testing.T) {
	h := newFakeHost()
	r := mustNew(t, testConfig(t), h)
	id := mustFresh(t, r, config.AgentConfig{})
	inst := mustGet(t, r, id)
	h.failRetire = true
	if err := r.Delete(id); err == nil {
		t.Fatal("Delete succeeded although the host could not release the agent")
	}
	if _, ok := r.Get(id); !ok || inst.closed() || !dirExists(inst.spec.StateDir) {
		t.Fatal("an agent that could not be released was removed, closed or wiped")
	}
}

func TestReload_KeepsAndRebuildsTempAgents(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	fresh := mustFresh(t, r, config.AgentConfig{Models: []string{"m1"}})
	clone := mustClone(t, r, "alice")
	before := mustGet(t, r, fresh)
	cloneBefore := mustGet(t, r, clone)
	created, _ := r.Info(fresh)

	// Alice moves and loses file_write; the clone follows its source.
	next := testConfig(t)
	next.Agents.List[0].Workspace = filepath.Join(t.TempDir(), "alice-elsewhere")
	next.Agents.List[0].Tools = []string{"file_read"}
	committed := false
	if err := r.Reload(context.Background(), next, h.build, func() bool { committed = true; return true }); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !committed {
		t.Fatal("commit was not called")
	}
	after := mustGet(t, r, fresh)
	if after == before || h.buildCount(fresh) != 2 {
		t.Fatalf("fresh agent: rebuilt=%v builds=%d; want kept and rebuilt", after != before, h.buildCount(fresh))
	}
	if !before.closed() || !cloneBefore.closed() {
		t.Fatal("the replaced temporary instances were not closed")
	}
	if info, _ := r.Info(fresh); !info.Created.Equal(created.Created) {
		t.Fatal("reload lost the agent's creation time")
	}
	c := mustGet(t, r, clone)
	if c.spec.Workspace != next.Agents.List[0].Workspace {
		t.Fatalf("clone workspace = %q, want alice's new one", c.spec.Workspace)
	}
	if !slices.Equal(c.spec.Config.Tools, []string{"file_read"}) || c.spec.Config.ID != clone {
		t.Fatalf("clone config = %+v, want alice's current one under the clone's id", c.spec.Config)
	}
	if got := r.ListTemp(); len(got) != 2 {
		t.Fatalf("ListTemp = %v, want both temporary agents kept", got)
	}
}

func TestReload_DeletesUnbuildableTempAgents(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	withModel := mustFresh(t, r, config.AgentConfig{Models: []string{"m1"}})
	bobClone := mustClone(t, r, "bob")
	stateOf := func(id string) string { i, _ := r.Info(id); return i.Spec.StateDir }
	withModelDir, bobCloneDir := stateOf(withModel), stateOf(bobClone)

	// m1 is gone and bob is disabled.
	next := testConfig(t)
	next.Models = nil
	off := false
	next.Agents.List[0].Models = nil
	next.Agents.List[1].Enabled = &off
	if err := r.Reload(context.Background(), next, h.build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	for _, id := range []string{withModel, bobClone} {
		if _, ok := r.Get(id); ok {
			t.Errorf("%s survived a reload that removed what it needs", id)
		}
	}
	if dirExists(withModelDir) || dirExists(bobCloneDir) {
		t.Error("a deleted temporary agent's directory was left behind")
	}
}

// A clone whose turn has just started is left exactly as it is by a reload,
// even one that removes its source: it is neither disposed nor replaced, and
// the first sweep after its turn deletes it.
func TestReload_LeavesAgentInTurnAlone(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	id, end, err := r.CreateCloneInTurn("bob", EphemeralMemory())
	if err != nil {
		t.Fatalf("CreateCloneInTurn: %v", err)
	}
	inst := mustGet(t, r, id)

	next := testConfig(t)
	next.Agents.List = next.Agents.List[:1] // bob is gone
	if err := r.Reload(context.Background(), next, h.build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := mustGet(t, r, id); got != inst || inst.closed() || !dirExists(inst.spec.StateDir) {
		t.Fatal("a reload disposed or replaced an agent in a turn")
	}
	end()
	if n := r.Sweep(time.Now()); n != 1 {
		t.Fatalf("Sweep deleted %d, want the clone whose source is gone", n)
	}
}

// A busy agent that turns busy while the reload builds is re-checked at the
// commit and keeps its instance; the rebuilt one is closed.
func TestReload_RechecksBusyAtCommit(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	id := mustClone(t, r, "alice")
	inst := mustGet(t, r, id)
	var end func()
	h.onBuild = func(spec Spec) {
		if spec.ID == id && end == nil {
			end, _ = r.BeginTurn(id, inst) // the turn starts while the reload is building
		}
	}
	if err := r.Reload(context.Background(), testConfig(t), h.build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := mustGet(t, r, id); got != inst || inst.closed() {
		t.Fatal("an agent that went into a turn during the reload was replaced or closed")
	}
	for _, b := range h.instances() {
		if b.spec.ID == id && b != inst && !b.closed() {
			t.Fatal("the unused rebuilt instance was not closed")
		}
	}
	end()
}

// A Delete while a reload is building is kept at its commit.
func TestReload_KeepsConcurrentDelete(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	doomed := mustFresh(t, r, config.AgentConfig{})
	deleted := false
	h.onBuild = func(spec Spec) {
		if spec.ID == doomed && !deleted {
			h.onBuild = nil
			deleted = true
			if err := r.Delete(doomed); err != nil {
				t.Errorf("Delete during reload: %v", err)
			}
		}
	}
	if err := r.Reload(context.Background(), testConfig(t), h.build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, ok := r.Get(doomed); ok {
		t.Fatal("an agent deleted during the reload came back")
	}
	for _, b := range h.instances() {
		if b.spec.ID == doomed && !b.closed() {
			t.Fatal("the rebuilt instance of the deleted agent was left open")
		}
	}
}

// An abandoned reload (cancelled context or vetoed commit) changes nothing and
// closes every instance it built.
func TestReload_AbandonedChangesNothingClosesBuilt(t *testing.T) {
	for name, run := range map[string]func(r *Registry[*fakeInst], h *fakeHost, next *config.Config) error{
		"cancelled": func(r *Registry[*fakeInst], h *fakeHost, next *config.Config) error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return r.Reload(ctx, next, h.build, commitOK)
		},
		"vetoed": func(r *Registry[*fakeInst], h *fakeHost, next *config.Config) error {
			return r.Reload(context.Background(), next, h.build, func() bool { return false })
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newFakeHost()
			r := mustNew(t, testConfig(t), h)
			temp := mustFresh(t, r, config.AgentConfig{})
			old := mustGet(t, r, temp)
			builtBefore := len(h.instances())
			next := testConfig(t)
			next.Agents.List = append(next.Agents.List, config.AgentConfig{ID: "carol"})
			if err := run(r, h, next); err == nil {
				t.Fatal("an abandoned reload reported success")
			}
			if len(r.List()) != 2 || mustGet(t, r, temp) != old || old.closed() {
				t.Fatal("an abandoned reload changed the registry")
			}
			for _, b := range h.instances()[builtBefore:] {
				if !b.closed() {
					t.Fatalf("instance %s built by the abandoned reload was left open", b.spec.ID)
				}
			}
		})
	}
}

func TestReload_BuildFailureChangesNothing(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	h.failID = "bob"
	if err := r.Reload(context.Background(), testConfig(t), h.build, commitOK); err == nil {
		t.Fatal("Reload succeeded although a config agent failed to build")
	}
	if _, ok := r.Get("bob"); !ok {
		t.Fatal("a failed reload dropped an agent")
	}
}

// A reload that cannot release a temporary agent it must delete keeps it.
func TestReload_DisposeKeepsEntryWhenNotReleased(t *testing.T) {
	h := newFakeHost()
	r := mustNew(t, testConfig(t), h)
	id := mustClone(t, r, "bob")
	inst := mustGet(t, r, id)
	h.failRetire = true
	next := testConfig(t)
	next.Agents.List = next.Agents.List[:1]
	if err := r.Reload(context.Background(), next, h.build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, ok := r.Get(id); !ok || inst.closed() || !dirExists(inst.spec.StateDir) {
		t.Fatal("an agent that could not be released was removed, closed or wiped")
	}
}

func TestSweep_DeletesIdleSkipsBusy(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	h := newFakeHost()
	hooks := h.hooks()
	hooks.Now = clock
	r, err := New(testConfig(t), hooks)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	idle := mustFresh(t, r, config.AgentConfig{}, Temp(time.Hour))
	busy := mustFresh(t, r, config.AgentConfig{}, Temp(time.Hour))
	recent := mustFresh(t, r, config.AgentConfig{}, Temp(time.Hour))
	end, _ := r.BeginTurn(busy, mustGet(t, r, busy))

	now = now.Add(50 * time.Minute)
	endRecent, _ := r.BeginTurn(recent, mustGet(t, r, recent))
	endRecent() // used 50 minutes in

	if n := r.Sweep(now.Add(20 * time.Minute)); n != 1 {
		t.Fatalf("Sweep deleted %d, want only the idle agent", n)
	}
	if _, ok := r.Get(idle); ok {
		t.Error("the idle agent survived the sweep")
	}
	if _, ok := r.Get(busy); !ok {
		t.Error("the sweep deleted an agent in a turn")
	}
	if _, ok := r.Get(recent); !ok {
		t.Error("the sweep deleted an agent used within its TTL")
	}
	if _, ok := r.Get("alice"); !ok {
		t.Error("the sweep touched a config agent")
	}
	end()
}

func TestPersistence_RoundTrip(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	fresh := mustFresh(t, r, config.AgentConfig{Name: "Scratch", Models: []string{"m1"}}, Temp(2*time.Hour))
	clone := mustClone(t, r, "alice")
	ephemeral := mustClone(t, r, "alice", EphemeralMemory())
	ephemeralDir := mustGet(t, r, ephemeral).spec.StateDir

	statePath := filepath.Join(cfg.DataDir(), global.InternalDir, StateFileName)
	info, err := os.Stat(statePath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s must exist with mode 0600: %v %v", statePath, info, err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var file stateFile
	if err := json.Unmarshal(data, &file); err != nil || len(file.Agents) != 2 {
		t.Fatalf("state file = %s (%v), want the two non-ephemeral agents", data, err)
	}
	for _, rec := range file.Agents {
		if rec.ID == ephemeral {
			t.Fatal("an ephemeral clone was saved")
		}
		if rec.ID == clone && rec.Config != nil {
			t.Fatal("a clone's configuration was saved; it must always come from its source")
		}
	}

	// A restart: alice's tools changed meanwhile.
	cfg.Agents.List[0].Tools = []string{"file_read"}
	r2 := mustNew(t, cfg, newFakeHost())
	got := mustGet(t, r2, fresh)
	if got.spec.Config.Name != "Scratch" || !got.spec.Fresh {
		t.Fatalf("fresh agent not restored: %+v", got.spec)
	}
	if info, _ := r2.Info(fresh); info.TTL != 2*time.Hour {
		t.Fatalf("restored TTL = %s, want 2h", info.TTL)
	}
	c := mustGet(t, r2, clone)
	alice := mustGet(t, r2, "alice")
	if c.spec.SourceID != "alice" || c.spec.Workspace != alice.spec.Workspace ||
		!slices.Equal(c.spec.Config.Tools, []string{"file_read"}) {
		t.Fatalf("clone not restored from alice's current config: %+v", c.spec)
	}
	if _, ok := r2.Get(ephemeral); ok || dirExists(ephemeralDir) {
		t.Fatal("an ephemeral clone (a run the restart interrupted) survived the restart")
	}

	// A restart whose configuration lost alice deletes her clone.
	cfg2 := testConfig(t)
	cfg2.Agents.List = []config.AgentConfig{{ID: "bob", Default: true}}
	copyStateFile(t, statePath, filepath.Join(cfg2.DataDir(), global.InternalDir, StateFileName))
	r3 := mustNew(t, cfg2, newFakeHost())
	if _, ok := r3.Get(clone); ok {
		t.Fatal("a clone whose source is gone was restored")
	}
	if _, ok := r3.Get(fresh); !ok {
		t.Fatal("the fresh agent should still be restored")
	}
}

// Close closes every instance once, however often it is called, and the
// registry refuses work afterwards.
func TestClose_Idempotent(t *testing.T) {
	h := newFakeHost()
	r := mustNew(t, testConfig(t), h)
	mustFresh(t, r, config.AgentConfig{})
	r.Close()
	r.Close()
	for _, b := range h.instances() {
		if n := b.closes.Load(); n != 1 {
			t.Fatalf("%s closed %d times, want 1", b.spec.ID, n)
		}
	}
	if _, err := r.CreateFresh(config.AgentConfig{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Create after Close = %v, want ErrClosed", err)
	}
}

func copyStateFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestOwner_SurvivesReloadAndRestart: the agent recorded with OwnedBy stays on
// a temporary agent through a reload and a restart; an agent created without
// it has none.
func TestOwner_SurvivesReloadAndRestart(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	clone := mustClone(t, r, "alice", OwnedBy("Bob"))
	fresh := mustFresh(t, r, config.AgentConfig{Models: []string{"m1"}}, OwnedBy("bob"))
	unowned := mustClone(t, r, "alice")

	check := func(t *testing.T, r *Registry[*fakeInst], when string) {
		t.Helper()
		for id, want := range map[string]string{clone: "bob", fresh: "bob", unowned: ""} {
			if got := mustGet(t, r, id).spec.Owner; got != want {
				t.Fatalf("%s: owner of %s = %q, want %q", when, id, got, want)
			}
		}
	}
	check(t, r, "created")

	if err := r.Reload(context.Background(), cfg, h.build, func() bool { return true }); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	check(t, r, "after reload")

	check(t, mustNew(t, cfg, newFakeHost()), "after restart")
}
