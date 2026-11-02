// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// workerTestConfig has Alice on "one" with worker models two, three
// (disabled) and four, allowed to spawn Bob; Bob is on "one" with worker
// model four.
func workerTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.Models = []config.ModelConfig{
		{ModelName: "one", Model: "one-wire", Provider: "p", Enabled: true},
		{ModelName: "two", Model: "two-wire", Provider: "p", Enabled: true},
		{ModelName: "three", Model: "three-wire", Provider: "p", Enabled: false},
		{ModelName: "four", Model: "four-wire", Provider: "p", Enabled: true},
	}
	cfg.Agents.List = []config.AgentConfig{
		{
			ID: "alice", Name: "Alice", Default: true, Models: []string{"one"},
			Subagents: &config.SubagentsConfig{AllowAgents: []string{"bob"}, Models: []string{"two", "three", "four"}},
		},
		{ID: "bob", Name: "Bob", Models: []string{"one"}, Subagents: &config.SubagentsConfig{Models: []string{"four"}}},
	}
	return cfg
}

func workerCandidateNames(cands []providers.FallbackCandidate) []string {
	names := make([]string, 0, len(cands))
	for _, c := range cands {
		names = append(names, candidateName(c))
	}
	return names
}

// An agent's worker candidates are its resolved worker models (enabled ones
// only) when it has any, else its own candidates.
func TestWorkerCandidates_Resolve(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	tests := []struct {
		name      string
		subagents *config.SubagentsConfig
		want      []string
	}{
		{name: "unset", subagents: nil, want: []string{"one"}},
		{name: "allowlist only", subagents: &config.SubagentsConfig{AllowAgents: []string{"bob"}}, want: []string{"one"}},
		{name: "set, disabled skipped", subagents: &config.SubagentsConfig{Models: []string{"two", "three", "four"}}, want: []string{"two", "four"}},
		{name: "all disabled or unknown", subagents: &config.SubagentsConfig{Models: []string{"three", "nope"}}, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := workerTestConfig(t)
			ac := config.AgentConfig{ID: "alice", Name: "Alice", Models: []string{"one"}, Subagents: tt.subagents}
			inst := mustNewAgentInstance(t, &ac, &cfg.Agents.Defaults, cfg, &mockProvider{})
			if got := workerCandidateNames(inst.WorkerCandidates); !slices.Equal(got, tt.want) {
				t.Errorf("WorkerCandidates = %v, want %v", got, tt.want)
			}
			if got := workerCandidateNames(inst.Candidates); !slices.Equal(got, []string{"one"}) {
				t.Errorf("Candidates = %v, want [one]", got)
			}
		})
	}
}

// workerRun records the clone a sub-agent run used and the wire models it
// called.
type workerRun struct {
	provider    *modelRecordingProvider
	mu          sync.Mutex
	cloneModels []string
	cloneModel  string
}

// newWorkerLoop builds a loop on cfg whose provider records each sub-agent
// clone's model list and every call's wire model.
func newWorkerLoop(t *testing.T, cfg *config.Config) (*AgentLoop, *workerRun) {
	t.Helper()
	rec := &workerRun{provider: &modelRecordingProvider{LLMProvider: &mockProvider{}}}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), rec.provider, nil)
	rec.provider.LLMProvider = &inspectingProvider{LLMProvider: &mockProvider{}, inspect: func(ctx context.Context) {
		if info, ok := al.GetRegistry().Info(providers.AgentIDFromContext(ctx)); ok && info.Spec.IsClone() {
			rec.mu.Lock()
			rec.cloneModels, rec.cloneModel = slices.Clone(info.Spec.Config.Models), info.Spec.CloneModel
			rec.mu.Unlock()
		}
	}}
	return al, rec
}

// check fails t unless the run's clone had models, CloneModel model, and
// called firstCall first.
func (r *workerRun) check(t *testing.T, models []string, model, firstCall string) {
	t.Helper()
	r.mu.Lock()
	cloneModels, cloneModel := r.cloneModels, r.cloneModel
	r.mu.Unlock()
	if !slices.Equal(cloneModels, models) || cloneModel != model {
		t.Errorf("clone models = %v, clone model = %q; want %v, %q", cloneModels, cloneModel, models, model)
	}
	r.provider.mu.Lock()
	defer r.provider.mu.Unlock()
	if len(r.provider.models) == 0 || r.provider.models[0] != firstCall {
		t.Errorf("models called = %v, want %s first", r.provider.models, firstCall)
	}
}

func spawnMgrOf(t *testing.T, al *AgentLoop, id string) *toolsagents.SubagentManager {
	t.Helper()
	inst, ok := al.GetRegistry().Get(id)
	if !ok || inst.spawnMgr == nil {
		t.Fatalf("agent %s has no spawn manager", id)
	}
	return inst.spawnMgr
}

// A worker runs on its agent's worker models: the configured list when set,
// the agent's own models when not, or one explicit worker model.
func TestSubagentSpawn_WorkerModels(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	tests := []struct {
		name      string
		unset     bool // Alice has no worker models
		target    string
		model     string
		wantList  []string
		wantModel string
		wantCall  string
	}{
		{name: "self, worker models set", wantList: []string{"two", "four"}, wantCall: "two-wire"},
		{name: "self, worker models unset", unset: true, wantList: []string{"one"}, wantCall: "one-wire"},
		{name: "self, explicit worker model not a chat model", model: "four", wantList: []string{"four"}, wantModel: "four", wantCall: "four-wire"},
		{name: "targeted Bob uses Bob's worker models", target: "bob", wantList: []string{"four"}, wantCall: "four-wire"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := workerTestConfig(t)
			if tt.unset {
				cfg.Agents.List[0].Subagents.Models = nil
			}
			al, rec := newWorkerLoop(t, cfg)
			res, err := toolsagents.NewSpawner(spawnMgrOf(t, al, "alice")).Spawn(context.Background(), global.SpawnRequest{
				Mode: global.SpawnAndWait, Task: "do it", TargetAgentID: tt.target, Model: tt.model,
			})
			if err != nil || res.IsError {
				t.Fatalf("Spawn: %v %+v", err, res)
			}
			rec.check(t, tt.wantList, tt.wantModel, tt.wantCall)
		})
	}
}

// A model that is one of the agent's chat models but not one of its worker
// models is refused by the spawner and by the runner, and makes no clone.
func TestSubagentSpawn_ChatModelNotWorkerModelRefused(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al, _ := newWorkerLoop(t, workerTestConfig(t))
	spawner := toolsagents.NewSpawner(spawnMgrOf(t, al, "alice"))
	res, err := spawner.Spawn(context.Background(), global.SpawnRequest{Mode: global.SpawnAndWait, Task: "do it", Model: "one"})
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("Spawn on a chat model = %v %+v, want a refusal", err, res)
	}
	if _, err = spawner.RunSync(context.Background(), "do it", "one"); !errors.Is(err, global.ErrModelNotAvailable) {
		t.Fatalf("RunSync on a chat model = %v, want ErrModelNotAvailable", err)
	}
	_, release, err := al.runSubagentTask(context.Background(), "alice", "do it", "one", "", nil)
	release()
	if !errors.Is(err, global.ErrModelNotAvailable) {
		t.Fatalf("runSubagentTask on a chat model = %v, want ErrModelNotAvailable", err)
	}
	if temps := al.GetRegistry().ListTemp(); len(temps) != 0 {
		t.Fatalf("a refused spawn created clones: %v", temps)
	}
}

// A Maestro dispatch with no model (RunSync with "") runs on the agent's
// worker models.
func TestRunSync_NoModelUsesWorkerModels(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al, rec := newWorkerLoop(t, workerTestConfig(t))
	res, err := toolsagents.NewSpawner(spawnMgrOf(t, al, "alice")).RunSync(context.Background(), "do it", "")
	if err != nil || res.Content != "Mock response" {
		t.Fatalf("RunSync: %v %+v", err, res)
	}
	rec.check(t, []string{"two", "four"}, "", "two-wire")
}

// An agent whose worker models are all unavailable launches no worker: the
// spawn is refused, never run on the agent's own models.
func TestSubagentSpawn_NoWorkerModelRefused(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	cfg := workerTestConfig(t)
	cfg.Agents.List[0].Subagents.Models = []string{"three"}
	al, _ := newWorkerLoop(t, cfg)
	spawner := toolsagents.NewSpawner(spawnMgrOf(t, al, "alice"))
	if _, err := spawner.RunSync(context.Background(), "do it", ""); !errors.Is(err, global.ErrModelNotAvailable) {
		t.Fatalf("RunSync = %v, want ErrModelNotAvailable", err)
	}
	res, err := spawner.Spawn(context.Background(), global.SpawnRequest{Mode: global.SpawnAndWait, Task: "do it"})
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("Spawn = %v %+v, want a refusal", err, res)
	}
	_, release, err := al.runSubagentTask(context.Background(), "alice", "do it", "", "", nil)
	release()
	if !errors.Is(err, global.ErrModelNotAvailable) {
		t.Fatalf("runSubagentTask = %v, want ErrModelNotAvailable", err)
	}
	if temps := al.GetRegistry().ListTemp(); len(temps) != 0 {
		t.Fatalf("a refused spawn created clones: %v", temps)
	}
}
