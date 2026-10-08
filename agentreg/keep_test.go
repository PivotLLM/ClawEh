// ClawEh
// License: MIT

package agentreg

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
)

// A temp_agents.json restore cannot use keeps everything: the file, every
// directory under the temp root, and the file stays untouched while the
// process runs, even when a new temporary agent is created.
func TestRestore_UnusableStateFileKeepsEverything(t *testing.T) {
	cases := map[string]func(t *testing.T, statePath string){
		"corrupt": func(t *testing.T, statePath string) {
			writeFile(t, statePath, []byte("{not json"))
		},
		"empty": func(t *testing.T, statePath string) {
			writeFile(t, statePath, nil)
		},
		"newer version": func(t *testing.T, statePath string) {
			data, _ := json.Marshal(stateFile{Version: stateFileVersion + 1})
			writeFile(t, statePath, data)
		},
		"unreadable": func(t *testing.T, statePath string) {
			// A directory where the file should be: reading it fails.
			if err := os.MkdirAll(filepath.Join(statePath, "x"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			h := newFakeHost()
			r := mustNew(t, cfg, h)
			id := mustCreate(t, r, config.AgentConfig{}, CloneOf("alice"))
			dir := mustGet(t, r, id).spec.StateDir
			r.Close()

			statePath := filepath.Join(cfg.DataDir(), global.InternalDir, StateFileName)
			if err := os.RemoveAll(statePath); err != nil {
				t.Fatal(err)
			}
			prepare(t, statePath)
			before := readMaybe(statePath)

			r2 := mustNew(t, cfg, newFakeHost())
			if _, ok := r2.Get(id); ok {
				t.Fatal("an agent was restored from an unusable state file")
			}
			if !dirExists(dir) {
				t.Fatal("a temporary agent's directory was removed although its list could not be read")
			}
			mustCreate(t, r2, config.AgentConfig{}, CloneOf("alice"))
			if after := readMaybe(statePath); !bytes.Equal(before, after) {
				t.Fatalf("the unusable state file was rewritten: %q → %q", before, after)
			}
			if !dirExists(dir) {
				t.Fatal("a temporary agent's directory was removed by a later creation")
			}
		})
	}
}

// A temporary agent that fails to build at restart for a reason that is not
// the configuration (an I/O error) keeps its directory and its record, and is
// restored at the next start.
func TestRestore_BuildErrorKeepsAgentForNextStart(t *testing.T) {
	cfg := testConfig(t)
	r := mustNew(t, cfg, newFakeHost())
	id := mustCreate(t, r, config.AgentConfig{}, CloneOf("alice"))
	other := mustCreate(t, r, config.AgentConfig{Models: []string{"m1"}})
	dir := mustGet(t, r, id).spec.StateDir
	r.Close()

	failing := newFakeHost()
	failing.failID = id
	r2 := mustNew(t, cfg, failing)
	if _, ok := r2.Get(id); ok {
		t.Fatal("an agent whose build failed was registered")
	}
	if !dirExists(dir) {
		t.Fatal("a build error deleted the temporary agent's directory")
	}
	// A save while it is unrestored keeps its record.
	if err := r2.Delete(other); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	r2.Close()

	r3 := mustNew(t, cfg, newFakeHost())
	if _, ok := r3.Get(id); !ok {
		t.Fatal("the agent kept after a build error was not restored at the next start")
	}
}

// A reload whose rebuild of a temporary agent fails for a reason that is not
// the configuration keeps the agent on its instance; it is neither closed nor
// deleted.
func TestReload_BuildErrorKeepsTempAgent(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	id := mustCreate(t, r, config.AgentConfig{}, CloneOf("alice"))
	inst := mustGet(t, r, id)
	h.failID = id
	if err := r.Reload(context.Background(), cfg, h.build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got, ok := r.Get(id); !ok || got != inst || inst.closed() || !dirExists(inst.spec.StateDir) {
		t.Fatal("a temporary agent whose rebuild failed was replaced, closed or deleted")
	}
	h.failID = ""
	if err := r.Reload(context.Background(), cfg, h.build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := mustGet(t, r, id); got == inst || !inst.closed() {
		t.Fatal("the next reload did not rebuild the kept agent")
	}
}

// Config instances a reload replaces are closed: reloading many times leaves
// only the live instances open.
func TestReload_ClosesReplacedConfigInstances(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	for range 5 {
		if err := r.Reload(context.Background(), cfg, h.build, commitOK); err != nil {
			t.Fatalf("Reload: %v", err)
		}
	}
	if open := openInstances(h); open != len(r.All()) {
		t.Fatalf("%d instances open after 5 reloads, want %d (one per agent)", open, len(r.All()))
	}
}

// A replaced config instance in a turn stays open until its turn ends, and
// no new turn can begin on it.
func TestReload_ReplacedConfigInstanceInTurnClosedWhenIdle(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	old := mustGet(t, r, "alice")
	end, current := r.BeginTurn("alice", old)
	if !current {
		t.Fatal("BeginTurn refused the current instance")
	}
	if err := r.Reload(context.Background(), cfg, h.build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if old.closed() {
		t.Fatal("a config instance was closed under its turn")
	}
	if _, current := r.BeginTurn("alice", old); current {
		t.Fatal("a turn began on a replaced config instance")
	}
	end()
	waitFor(t, old.closed, "the replaced config instance to be closed after its turn")
	if old.closes.Load() != 1 {
		t.Fatalf("closed %d times, want 1", old.closes.Load())
	}
}

// A replaced instance the host cannot release yet is retried by the sweep.
func TestSweep_RetriesUnreleasedReplacedInstances(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	old := mustGet(t, r, "alice")
	h.failRetire = true
	if err := r.Reload(context.Background(), cfg, h.build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if old.closed() {
		t.Fatal("an instance the host could not release was closed")
	}
	r.Sweep(time.Now())
	if old.closed() {
		t.Fatal("the sweep closed an instance the host still could not release")
	}
	h.mu.Lock()
	h.failRetire = false
	h.mu.Unlock()
	r.Sweep(time.Now())
	if !old.closed() {
		t.Fatal("the sweep did not close the instance once it could be released")
	}
}

func openInstances(h *fakeHost) int {
	n := 0
	for _, inst := range h.instances() {
		if !inst.closed() {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readMaybe(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}
