package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/global"
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
		RunFull: func(_ context.Context, _, _, _ string, _ []string) (*global.SyncResult, func(), error) {
			return &global.SyncResult{Content: "SENSITIVE WORKER OUTPUT", Iterations: 2}, func() {}, nil
		},
	})

	res, err := mgr.Run(context.Background(), "do work", "job", "", "cli", "direct", "", nil)
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
	entries, err := os.ReadDir(filepath.Join(ws, "tasks"))
	if err != nil {
		t.Fatalf("read tasks dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-results.json") {
			b, readErr := os.ReadFile(filepath.Join(ws, "tasks", e.Name()))
			if readErr != nil {
				t.Fatalf("read %s: %v", e.Name(), readErr)
			}
			if strings.Contains(string(b), "SENSITIVE WORKER OUTPUT") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("worker output should be persisted to a -results.json file under tasks/")
	}
}

// TestRun_UsesRunFull verifies the wait-mode spawn routes through the injected
// full-pipeline runner (self-spawn → owner id), passes the task and model
// through, and releases the worker once its result is recorded.
func TestRun_UsesRunFull(t *testing.T) {
	var (
		mu        sync.Mutex
		gotAgent  string
		gotTask   string
		gotModel  string
		callCount int
		released  int
	)
	ws := t.TempDir()
	mgr := NewSubagentManager(SubagentManagerConfig{
		Workspace:     ws,
		Live:          NewLiveSet(),
		CallerAgentID: "penny",
		RunFull: func(_ context.Context, agentID, task, model string, _ []string) (*global.SyncResult, func(), error) {
			mu.Lock()
			defer mu.Unlock()
			callCount++
			gotAgent, gotTask, gotModel = agentID, task, model
			return &global.SyncResult{Content: "chapter drafted", Iterations: 3}, func() {
				// The result file is written before the worker is released.
				if recs := listStatusRecords(tasksDirFor(ws)); len(recs) != 1 || recs[0].Status != StatusDone {
					t.Errorf("worker released before its result was recorded: %+v", recs)
				}
				released++
			}, nil
		},
	})

	res, err := mgr.Run(context.Background(), "write chapter 4", "chap", "", "cli", "direct", "Pro", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("runFull called %d times, want 1", callCount)
	}
	if gotAgent != "penny" {
		t.Errorf("self-spawn should target owner 'penny', got %q", gotAgent)
	}
	if released != 1 {
		t.Errorf("worker released %d times, want 1", released)
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

// TestRun_PassesMediaToRunFull verifies wait-mode spawn hands attached media
// refs through to the full-pipeline runner.
func TestRun_PassesMediaToRunFull(t *testing.T) {
	var (
		mu       sync.Mutex
		gotMedia []string
	)
	mgr := NewSubagentManager(SubagentManagerConfig{
		Workspace:     t.TempDir(),
		Live:          NewLiveSet(),
		CallerAgentID: "penny",
		RunFull: func(_ context.Context, _, _, _ string, media []string) (*global.SyncResult, func(), error) {
			mu.Lock()
			defer mu.Unlock()
			gotMedia = media
			return &global.SyncResult{Content: "looked at it", Iterations: 1}, func() {}, nil
		},
	})

	refs := []string{"media://11111111-1111-1111-1111-111111111111"}
	if _, err := mgr.Run(context.Background(), "describe the image", "img", "", "cli", "direct", "", refs); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gotMedia) != 1 || gotMedia[0] != refs[0] {
		t.Errorf("media not passed through: %v", gotMedia)
	}
}
