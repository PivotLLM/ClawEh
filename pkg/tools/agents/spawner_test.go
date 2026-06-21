package agents

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// MockLLMProvider echoes the last user message back as the assistant response so
// wait-mode results reference the task text.
type MockLLMProvider struct{}

func (m *MockLLMProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	content := ""
	for _, msg := range messages {
		if msg.Role == "user" {
			content = msg.Content
		}
	}
	return &providers.LLMResponse{Content: content}, nil
}

func (m *MockLLMProvider) GetDefaultModel() string { return "test-model" }
func (m *MockLLMProvider) SupportsTools() bool     { return false }
func (m *MockLLMProvider) GetContextWindow() int   { return 4096 }

func newTestSpawner(t *testing.T) *Spawner {
	t.Helper()
	mgr := NewSubagentManager(SubagentManagerConfig{
		Provider:     &MockLLMProvider{},
		DefaultModel: "test-model",
		Workspace:    t.TempDir(),
		Live:         NewLiveSet(),
	})
	return NewSpawner(mgr)
}

func TestSpawner_WaitMode_ReturnsResultSynchronously(t *testing.T) {
	sp := newTestSpawner(t)
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
	// Wait mode no longer inlines the worker's output — it writes the content to
	// the results file and returns a pointer with the security warning.
	if strings.Contains(res.ForLLM, "do the thing") {
		t.Errorf("worker output must NOT be inlined; it belongs in the results file: %q", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "results.json") || !strings.Contains(res.ForLLM, "SECURITY") {
		t.Errorf("expected a results-file pointer with a security warning; got %q", res.ForLLM)
	}
}

func TestSpawner_CallbackMode_DeliversPointer(t *testing.T) {
	sp := newTestSpawner(t)
	var (
		mu   sync.Mutex
		got  *global.Result
		done = make(chan struct{})
	)
	res, err := sp.Spawn(context.Background(), global.SpawnRequest{
		Mode: global.SpawnCallback,
		Name: "bg",
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
	// The callback references the results file and carries the security warning,
	// never the full content.
	if !strings.Contains(got.ForLLM, "results.json") || !strings.Contains(got.ForLLM, "SECURITY") {
		t.Errorf("expected a results-file pointer with a security warning, got %q", got.ForLLM)
	}
}

func TestSpawner_CallbackMode_RequiresName(t *testing.T) {
	sp := newTestSpawner(t)
	res, _ := sp.Spawn(context.Background(), global.SpawnRequest{
		Mode: global.SpawnCallback,
		Task: "no name given",
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected error result when name is missing for callback, got %+v", res)
	}
}

func TestSpawner_EmptyTask_IsError(t *testing.T) {
	sp := newTestSpawner(t)
	res, _ := sp.Spawn(context.Background(), global.SpawnRequest{Mode: global.SpawnAndWait, Task: "  "})
	if res == nil || !res.IsError {
		t.Fatalf("expected error result for empty task, got %+v", res)
	}
}

func TestSpawner_TargetedSpawn_AllowlistDeny(t *testing.T) {
	sp := newTestSpawner(t)
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
		{"callback", true, global.SpawnCallback, true},
		{"", true, global.SpawnCallback, true},
		{"", false, global.SpawnCallback, false}, // no async path → still callback, no push
		{"bogus", false, global.SpawnCallback, false},
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

// TestGlobalProvider_TaskToolsWiredToDeps verifies the provider exposes the three
// task tools and the spawn handler uses the injected spawner.
func TestGlobalProvider_TaskToolsWiredToDeps(t *testing.T) {
	defs := GlobalProvider.RegisterTools(global.Deps{Spawn: newTestSpawner(t)})
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"spawn", "status", "list"} {
		if !names[want] {
			t.Errorf("expected provider to expose %q; got %+v", want, names)
		}
	}

	var spawnDef *global.ToolDefinition
	for i := range defs {
		if defs[i].Name == "spawn" {
			spawnDef = &defs[i]
			break
		}
	}
	if spawnDef == nil {
		t.Fatal("spawn tool not found")
	}
	res, err := spawnDef.Handler(&global.ToolCall{
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
