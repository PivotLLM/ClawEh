// ClawEh
// License: MIT

package agentreg

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// workerConfig is testConfig with alice on m1 and worker models m2, m3.
func workerConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.Agents.List[0].Subagents = &config.SubagentsConfig{Models: []string{"m2", "m3"}}
	cfg.Models = append(cfg.Models,
		config.ModelConfig{ModelName: "m2", Model: "model-two", Provider: "p", Enabled: true},
		config.ModelConfig{ModelName: "m3", Model: "model-three", Provider: "p", Enabled: true})
	return cfg
}

// CloneModels replaces a clone's model list (trimmed, blanks dropped); an
// explicit CloneModel wins over it.
func TestCloneModels_SetsConfigModels(t *testing.T) {
	tests := []struct {
		name       string
		opts       []CloneOption
		wantModels []string
		wantList   []string
		wantModel  string
	}{
		{name: "list", opts: []CloneOption{CloneModels([]string{" m2 ", "", "m3"})}, wantModels: []string{"m2", "m3"}, wantList: []string{"m2", "m3"}},
		{name: "explicit model wins", opts: []CloneOption{CloneModels([]string{"m2", "m3"}), CloneModel("m1")}, wantModels: []string{"m1"}, wantModel: "m1"},
		{name: "explicit model wins in any order", opts: []CloneOption{CloneModel("m3"), CloneModels([]string{"m2"})}, wantModels: []string{"m3"}, wantModel: "m3"},
		{name: "empty list keeps the source's", opts: []CloneOption{CloneModels([]string{" "})}, wantModels: []string{"m1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mustNew(t, workerConfig(t), newFakeHost())
			spec := mustGet(t, r, mustClone(t, r, "alice", tt.opts...)).spec
			if !slices.Equal(spec.Config.Models, tt.wantModels) {
				t.Errorf("Config.Models = %v, want %v", spec.Config.Models, tt.wantModels)
			}
			if !slices.Equal(spec.CloneModels, tt.wantList) || spec.CloneModel != tt.wantModel {
				t.Errorf("CloneModels = %v, CloneModel = %q; want %v, %q", spec.CloneModels, spec.CloneModel, tt.wantList, tt.wantModel)
			}
		})
	}
}

// A clone's worker list is saved as clone_models and survives a reload and
// a restart.
func TestCloneModels_SurviveReloadAndRestart(t *testing.T) {
	cfg := workerConfig(t)
	r := mustNew(t, cfg, newFakeHost())
	id := mustClone(t, r, "alice", CloneModels([]string{"m2", "m3"}))

	data, err := os.ReadFile(r.statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var file stateFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	if len(file.Agents) != 1 || !slices.Equal(file.Agents[0].CloneModels, []string{"m2", "m3"}) {
		t.Fatalf("saved records = %+v, want one with clone_models [m2 m3]", file.Agents)
	}

	check := func(t *testing.T, r *Registry[*fakeInst], when string) {
		t.Helper()
		spec := mustGet(t, r, id).spec
		if !slices.Equal(spec.CloneModels, []string{"m2", "m3"}) || !slices.Equal(spec.Config.Models, []string{"m2", "m3"}) {
			t.Errorf("%s: CloneModels %v, Config.Models %v; want [m2 m3] for both", when, spec.CloneModels, spec.Config.Models)
		}
	}
	if err := r.Reload(t.Context(), cfg, newFakeHost().build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	check(t, r, "reloaded")
	check(t, mustNew(t, cfg, newFakeHost()), "restarted")
}

// A clone whose model is one of its source's worker models but not of its
// own models is kept on reload, also when named by its wire model id; one
// whose model is in neither list is deleted.
func TestCloneModel_WorkerModelKeptOnReload(t *testing.T) {
	cfg := workerConfig(t)
	r := mustNew(t, cfg, newFakeHost())
	worker := mustClone(t, r, "alice", CloneModel("m2"))
	byID := mustClone(t, r, "alice", CloneModel("model-two"))
	dropped := mustClone(t, r, "alice", CloneModel("m3"))

	next := workerConfig(t)
	next.Agents.List[0].Subagents.Models = []string{"m2"}
	if err := r.Reload(t.Context(), next, newFakeHost().build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, ok := r.Get(worker); !ok {
		t.Error("a clone on one of its source's worker models was deleted")
	}
	if _, ok := r.Get(byID); !ok {
		t.Error("a clone on a worker model named by its wire id was deleted")
	}
	if _, ok := r.Get(dropped); ok {
		t.Error("a clone on a model in neither of its source's lists survived the reload")
	}
}
