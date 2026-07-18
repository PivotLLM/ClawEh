package agents

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

func TestSpawnDepth_ContextRoundTrip(t *testing.T) {
	if d := SpawnDepth(context.Background()); d != 0 {
		t.Fatalf("unset depth = %d, want 0", d)
	}
	if d := SpawnDepth(WithSpawnDepth(context.Background(), 2)); d != 2 {
		t.Fatalf("depth = %d, want 2", d)
	}
}

func TestSpawner_DepthGuard_RefusesAtMax(t *testing.T) {
	sp := newTestSpawner(t)
	ctx := WithSpawnDepth(context.Background(), DefaultMaxSpawnDepth)
	res, err := sp.Spawn(ctx, global.SpawnRequest{Mode: global.SpawnAndWait, Task: "x"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res == nil || !res.IsError || !strings.Contains(res.ForLLM, "maximum sub-agent depth") {
		t.Fatalf("expected depth refusal at depth=%d, got %+v", DefaultMaxSpawnDepth, res)
	}
}

func TestSpawner_DepthGuard_AllowsBelowMax(t *testing.T) {
	sp := newTestSpawner(t)
	ctx := WithSpawnDepth(context.Background(), DefaultMaxSpawnDepth-1)
	res, err := sp.Spawn(ctx, global.SpawnRequest{Mode: global.SpawnAndWait, Task: "do it"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("depth %d (< max) must be allowed, got %+v", DefaultMaxSpawnDepth-1, res)
	}
}

func TestSpawner_RunSync_DepthGuard_RefusesAtMax(t *testing.T) {
	sp := newTestSpawner(t)
	_, err := sp.RunSync(WithSpawnDepth(context.Background(), DefaultMaxSpawnDepth), "task", "")
	if err == nil || !strings.Contains(err.Error(), "maximum sub-agent depth") {
		t.Fatalf("expected depth refusal, got %v", err)
	}
}

func TestSpawner_ConfiguredMaxDepth_OverridesDefault(t *testing.T) {
	sp := newTestSpawner(t)
	sp.SetMaxDepth(1) // tighter than the default of 3

	// depth 0 is allowed (0 < 1)...
	if res, _ := sp.Spawn(WithSpawnDepth(context.Background(), 0), global.SpawnRequest{
		Mode: global.SpawnAndWait, Task: "ok",
	}); res == nil || res.IsError {
		t.Fatalf("depth 0 must be allowed under max=1, got %+v", res)
	}
	// ...but depth 1 is refused, proving the configured bound (not the default) applies.
	res, _ := sp.Spawn(WithSpawnDepth(context.Background(), 1), global.SpawnRequest{
		Mode: global.SpawnAndWait, Task: "nope",
	})
	if res == nil || !res.IsError || !strings.Contains(res.ForLLM, "maximum sub-agent depth (1)") {
		t.Fatalf("depth 1 must be refused under configured max=1, got %+v", res)
	}
}

// TestDepthPropagation_AcrossAsyncDetach is the regression test for the async
// path: SpawnCallback runs on a detached context, so the spawning agent's depth
// must be persisted on the task record and restored before runFull, or a
// callback-spawned worker would reset to depth 0 and escape the bound.
func TestDepthPropagation_AcrossAsyncDetach(t *testing.T) {
	var mu sync.Mutex
	var gotDepth int
	done := make(chan struct{})

	mgr := NewSubagentManager(SubagentManagerConfig{
		Workspace: t.TempDir(),
		Live:      NewLiveSet(),
		RunFull: func(ctx context.Context, _, _, _, _ string) (string, int, error) {
			mu.Lock()
			gotDepth = SpawnDepth(ctx)
			mu.Unlock()
			close(done)
			return "ok", 1, nil
		},
	})
	sp := NewSpawner(mgr)

	// Parent at depth 2 fires a callback spawn; runRecord must invoke runFull with
	// depth 2 preserved (the +1 to depth 3 happens inside the real runSubagentTask,
	// which this fake runFull stands in for).
	if _, err := sp.Spawn(WithSpawnDepth(context.Background(), 2), global.SpawnRequest{
		Mode: global.SpawnCallback, Name: "n", Task: "t",
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runFull was not invoked")
	}
	mu.Lock()
	d := gotDepth
	mu.Unlock()
	if d != 2 {
		t.Fatalf("async runFull ran at depth %d, want 2 (parent depth lost across detach)", d)
	}
}
