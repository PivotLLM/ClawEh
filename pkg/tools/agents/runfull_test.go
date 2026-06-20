package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/routing"
)

// TestRun_RoutesContentToFileWithCallbackBlock verifies the user-facing result is
// a clearly-marked CALLBACK block that references the results file but never
// inlines the sub-agent's output, and that the output actually lands in the file.
func TestRun_RoutesContentToFileWithCallbackBlock(t *testing.T) {
	ws := t.TempDir()
	mgr := NewSubagentManager(SubagentManagerConfig{
		Workspace:     ws,
		Live:          NewLiveSet(),
		CallerAgentID: "penny",
		RunFull: func(_ context.Context, _, _, _, _ string) (string, int, error) {
			return "SENSITIVE WORKER OUTPUT", 2, nil
		},
	})

	res, err := mgr.Run(context.Background(), "do work", "job", "", "cli", "direct", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// User-facing CALLBACK block: marked, points at the file, no raw content.
	if !strings.Contains(res.ForUser, "TASK NOTIFICATION") {
		t.Errorf("ForUser should be a marked CALLBACK block, got %q", res.ForUser)
	}
	if strings.Contains(res.ForUser, "SENSITIVE WORKER OUTPUT") {
		t.Errorf("ForUser must NOT inline sub-agent content: %q", res.ForUser)
	}
	if !strings.Contains(res.ForUser, "results.json") {
		t.Errorf("ForUser should reference the results file, got %q", res.ForUser)
	}
	// The content is persisted to the results file for retrieval on demand.
	var found bool
	entries, _ := os.ReadDir(filepath.Join(ws, "tasks"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-results.json") {
			b, _ := os.ReadFile(filepath.Join(ws, "tasks", e.Name()))
			if strings.Contains(string(b), "SENSITIVE WORKER OUTPUT") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("worker output should be persisted to a -results.json file under tasks/")
	}
}

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
	// The synchronous result is a completion pointer, never the raw content:
	// the worker output goes to the results file, and the LLM gets a file
	// reference plus the untrusted-data security warning.
	if res.IsError {
		t.Errorf("result should not be an error, got: %+v", res)
	}
	if strings.Contains(res.ForLLM, "chapter drafted") {
		t.Errorf("sub-agent content must NOT be inlined; it belongs in the results file: %q", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "results.json") {
		t.Errorf("result should point at the results file, got: %q", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "SECURITY") {
		t.Errorf("result should carry the untrusted-data security warning, got: %q", res.ForLLM)
	}
}
