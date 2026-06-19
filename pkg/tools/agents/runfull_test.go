package agents

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/routing"
)

// TestRun_UsesRunFullWithSubagentSession verifies the wait-mode spawn routes
// through the injected full-pipeline runner with an isolated sub-agent session
// (self-spawn → owner id) and passes the model through.
func TestRun_UsesRunFullWithSubagentSession(t *testing.T) {
	var (
		mu        sync.Mutex
		gotAgent  string
		gotKey    string
		gotTask   string
		gotModel  string
		callCount int
	)
	mgr := NewSubagentManager(SubagentManagerConfig{
		Workspace:     t.TempDir(),
		Live:          NewLiveSet(),
		CallerAgentID: "penny",
		RunFull: func(_ context.Context, agentID, sessionKey, task, model string) (string, int, error) {
			mu.Lock()
			defer mu.Unlock()
			callCount++
			gotAgent, gotKey, gotTask, gotModel = agentID, sessionKey, task, model
			return "chapter drafted", 3, nil
		},
	})

	res, err := mgr.Run(context.Background(), "write chapter 4", "chap", "", "cli", "direct", "Pro")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("runFull called %d times, want 1", callCount)
	}
	if gotAgent != "penny" {
		t.Errorf("self-spawn should target owner 'penny', got %q", gotAgent)
	}
	if !routing.IsSubagentSessionKey(gotKey) {
		t.Errorf("session %q should be a sub-agent session", gotKey)
	}
	if !strings.Contains(gotKey, "penny") {
		t.Errorf("session %q should be scoped to the target agent", gotKey)
	}
	if gotTask != "write chapter 4" || gotModel != "Pro" {
		t.Errorf("task/model not passed through: %q / %q", gotTask, gotModel)
	}
	if res.IsError || !strings.Contains(res.ForLLM, "chapter drafted") {
		t.Errorf("result should carry the runFull output, got: %+v", res)
	}
	if !strings.Contains(res.ForLLM, "Iterations: 3") {
		t.Errorf("result should report the iteration count, got: %s", res.ForLLM)
	}
}
