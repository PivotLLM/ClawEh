// ClawEh
// License: MIT

package agentreg

import (
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// Agent ids written with capitals are registered under their identity (lower
// case), live in the lower-case folder an existing install already has, and
// are found by any spelling.
func TestConfigAgents_CapitalisedIDs(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.List[0].ID = "Alice"
	cfg.Agents.List[1].ID = "BOB"
	r := mustNew(t, cfg, newFakeHost())

	if got := r.List(); len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("List = %v, want [alice bob]", got)
	}
	if r.DefaultID() != "alice" {
		t.Fatalf("DefaultID = %q, want alice", r.DefaultID())
	}
	for _, id := range []string{"bob", "Bob", "BOB"} {
		inst := mustGet(t, r, id)
		if inst.spec.ID != "bob" {
			t.Errorf("Get(%q) spec id = %q, want bob", id, inst.spec.ID)
		}
		if want := filepath.Join(cfg.BaseDir(), "bob"); inst.spec.Workspace != want {
			t.Errorf("Get(%q) workspace = %q, want %q", id, inst.spec.Workspace, want)
		}
	}
	if ws := ConfigWorkspace(&config.AgentConfig{ID: "Main"}, "/base"); ws != filepath.Join("/base", "default") {
		t.Errorf("ConfigWorkspace(Main) = %q, want /base/default", ws)
	}
	clone := mustClone(t, r, "Bob")
	if info, ok := r.Info(clone); !ok || info.Spec.SourceID != "bob" {
		t.Errorf("clone of Bob: info %+v, want source bob", info)
	}
}
