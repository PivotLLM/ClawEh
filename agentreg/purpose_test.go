// ClawEh
// License: MIT

package agentreg

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/clock"
)

// A temporary agent's purpose and a clone's model override are kept on
// rebuilds and restored after a restart.
func TestPurposeAndCloneModel_SurviveReloadAndRestart(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.List[0].Models = []string{"m1", "m2"}
	cfg.Models = append(cfg.Models, config.ModelConfig{ModelName: "m2", Model: "model-two", Provider: "p", Enabled: true})
	r := mustNew(t, cfg, newFakeHost())
	clone := mustClone(t, r, "alice", CloneModel("m2"), WithPurpose("forum"), OwnedBy("alice"))
	fresh := mustFresh(t, r, config.AgentConfig{Models: []string{"m1"}}, WithPurpose("forum"))
	plain := mustClone(t, r, "alice")

	check := func(t *testing.T, r *Registry[*fakeInst], when string) {
		t.Helper()
		c := mustGet(t, r, clone).spec
		if c.Purpose != "forum" || c.CloneModel != "m2" || !slices.Equal(c.Config.Models, []string{"m2"}) || c.Owner != "alice" {
			t.Errorf("%s: clone spec = purpose %q model %q models %v owner %q, want forum/m2/[m2]/alice",
				when, c.Purpose, c.CloneModel, c.Config.Models, c.Owner)
		}
		if f := mustGet(t, r, fresh).spec; f.Purpose != "forum" {
			t.Errorf("%s: fresh purpose = %q, want forum", when, f.Purpose)
		}
		if p := mustGet(t, r, plain).spec; p.Purpose != "" || p.CloneModel != "" || !slices.Equal(p.Config.Models, []string{"m1", "m2"}) {
			t.Errorf("%s: plain clone = purpose %q model %q models %v, want alice's list", when, p.Purpose, p.CloneModel, p.Config.Models)
		}
	}
	check(t, r, "created")
	if err := r.Reload(t.Context(), cfg, newFakeHost().build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	check(t, r, "reloaded")
	check(t, mustNew(t, cfg, newFakeHost()), "restarted")
}

// TestCloneModel_CloneOnly: CloneModel is not a fresh-agent option, and a
// clone on an unconfigured model is refused.
func TestCloneModel_CloneOnly(t *testing.T) {
	r := mustNew(t, testConfig(t), newFakeHost())
	if _, ok := CloneModel("m1").(FreshOption); ok {
		t.Fatal("CloneModel is a FreshOption")
	}
	if _, err := r.CreateClone("alice", CloneModel("nope")); err == nil {
		t.Fatal("a clone on an unconfigured model was created")
	}
}

// Touch keeps an idle temporary agent past its TTL and refuses unknown and
// config agents.
func TestTouch(t *testing.T) {
	fc := clock.NewFake(time.Now())
	h := newFakeHost()
	hooks := h.hooks()
	hooks.Clock = fc
	r, err := New(testConfig(t), hooks)
	if err != nil {
		t.Fatal(err)
	}
	id := mustFresh(t, r, config.AgentConfig{}, Temp(time.Hour))
	fc.Advance(50 * time.Minute)
	if err := r.Touch(id); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if n := r.Sweep(fc.Now().Add(20 * time.Minute)); n != 0 {
		t.Fatalf("Sweep deleted %d agents; the touched agent is within its TTL", n)
	}
	if err := r.Touch("no-such-agent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Touch(unknown) = %v, want ErrNotFound", err)
	}
	if err := r.Touch("alice"); !errors.Is(err, ErrNotTemp) {
		t.Errorf("Touch(config agent) = %v, want ErrNotTemp", err)
	}
}

// A clone whose model override is no longer one of its source's models is
// deleted on reload, like one whose model is gone.
func TestCloneModel_DeletedWhenSourceDropsIt(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.List[0].Models = []string{"m1", "m2"}
	cfg.Models = append(cfg.Models, config.ModelConfig{ModelName: "m2", Model: "model-two", Provider: "p", Enabled: true})
	r := mustNew(t, cfg, newFakeHost())
	clone := mustClone(t, r, "alice", CloneModel("m2"))
	kept := mustClone(t, r, "alice", CloneModel("m1"))

	next := testConfig(t)
	next.Models = cfg.Models
	next.Agents.List[0].Models = []string{"m1"}
	if err := r.Reload(t.Context(), next, newFakeHost().build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, ok := r.Get(clone); ok {
		t.Error("a clone on a model its source no longer has survived the reload")
	}
	if _, ok := r.Get(kept); !ok {
		t.Error("a clone on a model its source still has was deleted")
	}
}
