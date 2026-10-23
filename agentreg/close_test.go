// ClawEh
// License: MIT

package agentreg

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
)

// A config instance a reload replaced while a turn ran on it is in neither
// entries nor orphans, so Close does not close it. When that turn ends after
// Close has begun, the instance is still released and closed.
func TestReplacedInstanceRetiredAfterClose(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	old := mustGet(t, r, "alice")
	end, ok := r.BeginTurn("alice", old)
	if !ok {
		t.Fatal("BeginTurn refused alice's current instance")
	}
	if err := r.Reload(context.Background(), cfg, h.build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if mustGet(t, r, "alice") == old {
		t.Fatal("Reload kept alice's old instance")
	}
	if old.closed() {
		t.Fatal("a replaced instance in a turn was closed by Reload")
	}

	retired := make(chan string, 1)
	h.onRetire = func(spec Spec) { retired <- spec.ID }
	r.Close()
	if old.closed() {
		t.Fatal("Close closed a replaced instance still in a turn")
	}
	select {
	case id := <-retired:
		t.Fatalf("Close retired %s; the replaced instance is retired by its turn's end", id)
	default:
	}

	end() // the turn ends after Close has run
	if id := <-retired; id != "alice" {
		t.Fatalf("retired %s, want alice", id)
	}
	<-old.done
	if n := old.closes.Load(); n != 1 {
		t.Fatalf("replaced instance closed %d times, want once", n)
	}
}

// A deferred delete whose turn ends after Close has begun is dropped: the
// agent is neither released nor removed, and stays in temp_agents.json so the
// next start restores it.
func TestDeferredDeleteDroppedAfterClose(t *testing.T) {
	cfg := testConfig(t)
	h := newFakeHost()
	r := mustNew(t, cfg, h)
	id := mustFresh(t, r, config.AgentConfig{Name: "Bob"})
	inst := mustGet(t, r, id)
	end, _ := r.BeginTurn(id, inst)
	if err := r.DeleteWhenIdle(id); !errors.Is(err, ErrDeletePending) {
		t.Fatalf("DeleteWhenIdle mid-turn = %v, want ErrDeletePending", err)
	}

	retired := make(chan string, 1)
	h.onRetire = func(spec Spec) { retired <- spec.ID }
	r.Close()
	end() // would start the deferred delete; Close has begun, so it is dropped

	// Barrier: end() hands the delete to goBackground before it returns, so
	// a delete that was (wrongly) tracked has been added to the background
	// group by now; waiting on the group lets it finish before the checks.
	// (A second Close is no barrier: it returns at once.)
	r.background.Wait()
	select {
	case got := <-retired:
		t.Fatalf("%s was released after Close", got)
	default:
	}
	if !dirExists(inst.spec.StateDir) {
		t.Fatal("the agent's directory was removed after Close")
	}
	data, err := os.ReadFile(filepath.Join(cfg.DataDir(), global.InternalDir, StateFileName))
	if err != nil {
		t.Fatalf("read %s: %v", StateFileName, err)
	}
	var file stateFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("parse %s: %v", StateFileName, err)
	}
	found := false
	for _, rec := range file.Agents {
		found = found || rec.ID == id
	}
	if !found {
		t.Fatalf("%s no longer lists %s: %s", StateFileName, id, data)
	}
}
