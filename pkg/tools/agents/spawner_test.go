package agents

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

func newTestSpawner() *Spawner {
	mgr := NewSubagentManager(SubagentManagerConfig{
		Provider:     &MockLLMProvider{},
		DefaultModel: "test-model",
		Workspace:    "/tmp/test",
	})
	return NewSpawner(mgr)
}

func TestSpawner_WaitMode_ReturnsResultSynchronously(t *testing.T) {
	sp := newTestSpawner()
	res, err := sp.Spawn(context.Background(), global.SpawnRequest{
		Mode: global.SpawnAndWait,
		Task: "do the thing",
	})
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("expected successful synchronous result, got %+v", res)
	}
	if res.Async {
		t.Error("wait mode result must not be marked Async")
	}
	if !strings.Contains(res.ForLLM, "do the thing") {
		t.Errorf("expected worker output to reference the task; got %q", res.ForLLM)
	}
}

func TestSpawner_CallbackMode_DeliversResult(t *testing.T) {
	sp := newTestSpawner()
	var (
		mu   sync.Mutex
		got  *global.Result
		done = make(chan struct{})
	)
	res, err := sp.Spawn(context.Background(), global.SpawnRequest{
		Mode: global.SpawnCallback,
		Task: "background work",
		OnResult: func(r *global.Result) {
			mu.Lock()
			got = r
			mu.Unlock()
			close(done)
		},
	})
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	if res == nil || !res.Async {
		t.Fatalf("callback mode should return an async acknowledgement, got %+v", res)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("callback was not invoked within timeout")
	}
	mu.Lock()
	defer mu.Unlock()
	if got == nil || got.IsError {
		t.Fatalf("expected a successful callback result, got %+v", got)
	}
}

func TestSpawner_DetachedMode_NoCallback(t *testing.T) {
	sp := newTestSpawner()
	res, err := sp.Spawn(context.Background(), global.SpawnRequest{
		Mode: global.SpawnDetached,
		Task: "fire and forget",
	})
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	if res == nil || !res.Async {
		t.Fatalf("detached mode should return an async acknowledgement, got %+v", res)
	}
}

func TestSpawner_EmptyTask_IsError(t *testing.T) {
	sp := newTestSpawner()
	res, _ := sp.Spawn(context.Background(), global.SpawnRequest{Mode: global.SpawnAndWait, Task: "  "})
	if res == nil || !res.IsError {
		t.Fatalf("expected error result for empty task, got %+v", res)
	}
}

func TestSpawner_TargetedSpawn_AllowlistDeny(t *testing.T) {
	sp := newTestSpawner()
	sp.SetAllowlistChecker(func(string) bool { return false })
	res, _ := sp.Spawn(context.Background(), global.SpawnRequest{
		Mode:          global.SpawnAndWait,
		Task:          "x",
		TargetAgentID: "bob",
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected allowlist denial to be an error result, got %+v", res)
	}
	if !strings.Contains(res.ForLLM, "bob") {
		t.Errorf("expected denial message to name the target agent; got %q", res.ForLLM)
	}
}

func TestSpawner_NilManager_IsError(t *testing.T) {
	sp := NewSpawner(nil)
	res, _ := sp.Spawn(context.Background(), global.SpawnRequest{Mode: global.SpawnAndWait, Task: "x"})
	if res == nil || !res.IsError {
		t.Fatalf("expected error result when manager is nil, got %+v", res)
	}
}

func TestResolveSpawnMode(t *testing.T) {
	notify := func(*global.Result) {}
	cases := []struct {
		in        string
		hasNotify bool
		want      global.SpawnMode
		wantSink  bool
	}{
		{"wait", true, global.SpawnAndWait, false},
		{"sync", false, global.SpawnAndWait, false},
		{"detached", true, global.SpawnDetached, false},
		{"callback", true, global.SpawnCallback, true},
		{"", true, global.SpawnCallback, true},
		{"", false, global.SpawnDetached, false}, // no async path → degrade
		{"bogus", false, global.SpawnDetached, false},
	}
	for _, tc := range cases {
		call := &global.ToolCall{}
		if tc.hasNotify {
			call.Notify = notify
		}
		mode, sink := resolveSpawnMode(tc.in, call)
		if mode != tc.want {
			t.Errorf("resolveSpawnMode(%q, notify=%v) mode = %v, want %v", tc.in, tc.hasNotify, mode, tc.want)
		}
		if (sink != nil) != tc.wantSink {
			t.Errorf("resolveSpawnMode(%q, notify=%v) sink present = %v, want %v", tc.in, tc.hasNotify, sink != nil, tc.wantSink)
		}
	}
}

// Compile-time assurance the global provider exposes the spawn tool and uses the
// injected spawner.
func TestGlobalProvider_SpawnToolWiredToDeps(t *testing.T) {
	defs := GlobalProvider.RegisterTools(global.Deps{Spawn: newTestSpawner()})
	if len(defs) != 1 || defs[0].Name != "spawn" {
		t.Fatalf("expected a single 'spawn' tool, got %+v", defs)
	}
	res, err := defs[0].Handler(&global.ToolCall{
		Ctx:  context.Background(),
		Args: map[string]any{"task": "hello", "mode": "wait"},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("expected successful wait-mode spawn via handler, got %+v", res)
	}
}
