package agents

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/providers"
)

func candList() []providers.FallbackCandidate {
	return []providers.FallbackCandidate{
		{Provider: "p", Model: "flash-wire", Alias: "Flash"},
		{Provider: "p", Model: "pro-wire", Alias: "Pro"},
	}
}

func TestMatchAndPromoteCandidate(t *testing.T) {
	c := candList()
	// Match by alias (case-insensitive).
	if m, ok := MatchCandidate(c, "pro"); !ok || m.Model != "pro-wire" {
		t.Fatalf("alias match failed: %+v ok=%v", m, ok)
	}
	// Match by wire model.
	if _, ok := MatchCandidate(c, "flash-wire"); !ok {
		t.Fatal("wire-model match failed")
	}
	// No match.
	if _, ok := MatchCandidate(c, "gpt-9"); ok {
		t.Fatal("unexpected match for unknown model")
	}
	// Tolerant of surrounding quotes and extra whitespace.
	for _, in := range []string{` "Pro" `, "'pro'", "`Pro`", "  pro  ", "“Pro”"} {
		if m, ok := MatchCandidate(c, in); !ok || m.Model != "pro-wire" {
			t.Fatalf("normalized match failed for %q: %+v ok=%v", in, m, ok)
		}
	}
	// Blank never matches.
	if _, ok := MatchCandidate(c, "   "); ok {
		t.Fatal("blank model should not match")
	}
}

func TestSpawner_InvalidModel_IsError(t *testing.T) {
	mgr := NewSubagentManager(SubagentManagerConfig{
		Workspace:      t.TempDir(),
		Live:           NewLiveSet(),
		SelfCandidates: candList(),
		RunFull:        echoRunFull,
	})
	sp := NewSpawner(mgr)

	res, err := sp.Spawn(context.Background(), global.SpawnRequest{
		Mode: global.SpawnAndWait, Task: "write a chapter", Model: "gpt-9",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.ForLLM, "not available") {
		t.Fatalf("expected an 'not available' error result, got: %+v", res)
	}
	// The error should list the allowed models.
	if !strings.Contains(res.ForLLM, "Flash") || !strings.Contains(res.ForLLM, "Pro") {
		t.Fatalf("error should list the worker models, got: %s", res.ForLLM)
	}
}

func TestSpawner_ValidModel_Runs(t *testing.T) {
	mgr := NewSubagentManager(SubagentManagerConfig{
		Workspace:      t.TempDir(),
		Live:           NewLiveSet(),
		SelfCandidates: candList(),
		RunFull:        echoRunFull,
	})
	sp := NewSpawner(mgr)

	res, err := sp.Spawn(context.Background(), global.SpawnRequest{
		Mode: global.SpawnAndWait, Task: "write a chapter", Model: "Pro",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("valid model spawn should succeed, got: %s", res.ForLLM)
	}
}

// Spawn and RunSync launch a worker only on one of the executing agent's
// worker models: they refuse an agent whose configured worker models are all
// unavailable, and a model outside them, before anything runs. An agent with
// no configured worker models and no models at all runs as before. A
// targeted spawn checks the target's worker models.
func TestSpawner_WorkerModelCheck(t *testing.T) {
	bobCands := []providers.FallbackCandidate{{Provider: "p", Model: "lite-wire", Alias: "Lite"}}
	tests := []struct {
		name    string
		self    []providers.FallbackCandidate
		unset   bool // no agent has configured worker models
		target  string
		model   string
		wantErr string // "" = runs
	}{
		{name: "no worker models", self: nil, wantErr: `no worker model is available for agent "alice"`},
		{name: "no worker models, model given", self: nil, model: "Pro", wantErr: `no worker model is available for agent "alice"`},
		{name: "none configured, no models", self: nil, unset: true},
		{name: "none configured, no models, model given", self: nil, unset: true, model: "Pro", wantErr: `model "Pro" is not available for agent "alice"`},
		{name: "model outside", self: candList(), model: "gpt-9", wantErr: `model "gpt-9" is not available for agent "alice"`},
		{name: "model inside", self: candList(), model: "pro"},
		{name: "no model", self: candList()},
		{name: "targeted, Bob's model", self: candList(), target: "bob", model: "Lite"},
		{name: "targeted, caller's model", self: candList(), target: "bob", model: "Pro", wantErr: `model "Pro" is not available for agent "bob"`},
		{name: "targeted, Bob has none", self: candList(), target: "bob-empty", wantErr: `no worker model is available for agent "bob-empty"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ran int
			mgr := NewSubagentManager(SubagentManagerConfig{
				Workspace: t.TempDir(), Live: NewLiveSet(), CallerAgentID: "alice", SelfCandidates: tt.self,
				CandidateResolver: func(id string) ([]providers.FallbackCandidate, bool) {
					switch id {
					case "bob":
						return bobCands, true
					case "bob-empty":
						return nil, true
					}
					return nil, false
				},
				WorkerModelsSet: func(string) bool { return !tt.unset },
				RunFull: func(_ context.Context, _, task, _, _ string, _ []string) (*global.SyncResult, func(), error) {
					ran++
					return &global.SyncResult{Content: task, Iterations: 1}, func() {}, nil
				},
			})
			sp := NewSpawner(mgr)

			res, err := sp.Spawn(context.Background(), global.SpawnRequest{
				Mode: global.SpawnAndWait, Task: "do it", TargetAgentID: tt.target, Model: tt.model,
			})
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			if tt.wantErr == "" {
				if res.IsError {
					t.Fatalf("Spawn refused: %s", res.ForLLM)
				}
			} else if !res.IsError || !strings.Contains(res.ForLLM, tt.wantErr) {
				t.Fatalf("Spawn = %+v, want a refusal containing %q", res, tt.wantErr)
			}
			want := 0
			if tt.wantErr == "" {
				want = 1
			}

			if tt.target == "" { // RunSync is a self-spawn only
				_, err = sp.RunSync(context.Background(), "do it", tt.model)
				if tt.wantErr == "" {
					if err != nil {
						t.Fatalf("RunSync: %v", err)
					}
					want++
				} else if !errors.Is(err, global.ErrModelNotAvailable) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("RunSync = %v, want ErrModelNotAvailable containing %q", err, tt.wantErr)
				}
			}
			if ran != want {
				t.Errorf("worker ran %d times, want %d", ran, want)
			}
		})
	}
}
