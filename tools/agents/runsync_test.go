// ClawEh
// License: MIT

package agents

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/providers"
)

// newRunSyncSpawner builds a Spawner whose manager has one configured model
// candidate and a fake full-pipeline runner that records the model it was given
// and returns usage.
func newRunSyncSpawner(t *testing.T) (*Spawner, *sync.Mutex, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var models []string
	mgr := NewSubagentManager(SubagentManagerConfig{
		Workspace:      t.TempDir(),
		Live:           NewLiveSet(),
		CallerAgentID:  "penny",
		SelfCandidates: []providers.FallbackCandidate{{Alias: "Pro", Model: "claude-x", Provider: "anthropic"}},
		RunFull: func(_ context.Context, _, _, _, model string, _ []string) (*global.SyncResult, error) {
			mu.Lock()
			models = append(models, model)
			mu.Unlock()
			return &global.SyncResult{
				Content: "done", Iterations: 3,
				TurnUsage: global.TurnUsage{Model: "claude-x", Provider: "anthropic", InputTokens: 10, OutputTokens: 5, CostUSD: 0.5},
			}, nil
		},
	})
	return NewSpawner(mgr), &mu, &models
}

func TestSpawner_RunSync_ReturnsContentAndUsage(t *testing.T) {
	sp, _, _ := newRunSyncSpawner(t)
	res, err := sp.RunSync(context.Background(), "task", "")
	if err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if res.Content != "done" || res.Iterations != 3 || res.Model != "claude-x" || res.InputTokens != 10 || res.OutputTokens != 5 || res.CostUSD != 0.5 {
		t.Fatalf("result = %+v, want content + usage from the run", res)
	}
}

func TestSpawner_RunSync_KnownModelPassesThrough(t *testing.T) {
	sp, mu, models := newRunSyncSpawner(t)
	// Alias and model name both match, case-insensitively.
	for _, m := range []string{"Pro", "pro", "claude-x"} {
		if _, err := sp.RunSync(context.Background(), "task", m); err != nil {
			t.Fatalf("RunSync(%q): %v", m, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*models) != 3 || (*models)[0] != "Pro" || (*models)[1] != "pro" || (*models)[2] != "claude-x" {
		t.Fatalf("runFull saw models %v, want the requested hints passed through", *models)
	}
}

func TestSpawner_RunSync_UnknownModelIsPermanent(t *testing.T) {
	sp, mu, models := newRunSyncSpawner(t)
	res, err := sp.RunSync(context.Background(), "task", "gemini-ultra")
	if res != nil || err == nil {
		t.Fatalf("got %+v / %v, want error for unknown model", res, err)
	}
	if !errors.Is(err, global.ErrModelNotAvailable) {
		t.Errorf("err %v must wrap ErrModelNotAvailable", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*models) != 0 {
		t.Errorf("runFull must not be invoked for an unknown model, saw %v", *models)
	}
}

func TestSpawner_RunSync_DepthErrorIsSentinel(t *testing.T) {
	sp, _, _ := newRunSyncSpawner(t)
	_, err := sp.RunSync(WithSpawnDepth(context.Background(), DefaultMaxSpawnDepth), "task", "")
	if !errors.Is(err, global.ErrSpawnDepthExceeded) {
		t.Fatalf("err %v must wrap ErrSpawnDepthExceeded", err)
	}
}

func TestSpawner_RunSync_NoRunnerIsUnavailable(t *testing.T) {
	var nilSpawner *Spawner
	if _, err := nilSpawner.RunSync(context.Background(), "task", ""); !errors.Is(err, global.ErrSpawnUnavailable) {
		t.Errorf("nil spawner: err %v must wrap ErrSpawnUnavailable", err)
	}
	// A manager without the full-pipeline runner cannot run sync work either.
	mgr := NewSubagentManager(SubagentManagerConfig{Workspace: t.TempDir(), Live: NewLiveSet()})
	if _, err := NewSpawner(mgr).RunSync(context.Background(), "task", ""); !errors.Is(err, global.ErrSpawnUnavailable) {
		t.Errorf("no runFull: err %v must wrap ErrSpawnUnavailable", err)
	}
}
