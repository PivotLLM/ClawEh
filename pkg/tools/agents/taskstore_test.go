package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

func newTestManager(t *testing.T) (*SubagentManager, string) {
	t.Helper()
	ws := t.TempDir()
	mgr := NewSubagentManager(SubagentManagerConfig{
		Provider:     &MockLLMProvider{},
		DefaultModel: "test-model",
		Workspace:    ws,
		Live:         NewLiveSet(),
	})
	return mgr, ws
}

func TestSpawnCallback_Lifecycle(t *testing.T) {
	mgr, ws := newTestManager(t)
	dir := filepath.Join(ws, "tasks")

	done := make(chan *tools.ToolResult, 1)
	id, err := mgr.SpawnCallback("the work", "job1", "", "cli", "direct", "",
		func(_ context.Context, r *tools.ToolResult) { done <- r }, 0)
	if err != nil {
		t.Fatalf("SpawnCallback error: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty uuid")
	}
	if _, err := os.Stat(statusPath(dir, id)); err != nil {
		t.Fatalf("status file not written: %v", err)
	}

	var res *tools.ToolResult
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("callback not invoked")
	}
	if res == nil || res.IsError {
		t.Fatalf("expected success pointer, got %+v", res)
	}

	st, _ := mgr.TaskStatus(id)
	if st.Status != StatusDone {
		t.Errorf("status = %q, want done", st.Status)
	}
	if _, err := os.Stat(resultsPath(dir, id)); err != nil {
		t.Errorf("results file not written: %v", err)
	}
	if _, err := os.Stat(runPath(dir, id)); !os.IsNotExist(err) {
		t.Errorf("run marker should be removed after completion")
	}
}

func TestTaskStatus_Unknown(t *testing.T) {
	mgr, _ := newTestManager(t)
	st, err := mgr.TaskStatus("does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.Status != StatusUnknown {
		t.Errorf("status = %q, want unknown", st.Status)
	}
}

func TestTaskList_ReturnsTasks(t *testing.T) {
	mgr, _ := newTestManager(t)
	done := make(chan *tools.ToolResult, 1)
	if _, err := mgr.SpawnCallback("w", "listed", "", "cli", "direct", "",
		func(_ context.Context, r *tools.ToolResult) { done <- r }, 0); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	<-done
	list, err := mgr.TaskList()
	if err != nil {
		t.Fatalf("TaskList: %v", err)
	}
	if len(list) != 1 || list[0].Name != "listed" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

// writeInterrupted writes a status record + .run marker simulating a task that
// was interrupted (no live worker), with the given restarts and retryAfter.
func writeInterrupted(t *testing.T, dir, id, name string, restarts int, retryAfter int64) {
	t.Helper()
	rec := &TaskRecord{
		UUID: id, Name: name, Mode: "callback", Task: "echo me",
		Channel: "cli", ChatID: "direct", Status: StatusRunning,
		CreatedAt: nowRFC(), Restarts: restarts, RetryAfter: retryAfter,
		ResultsPath: relResultsPath(id),
	}
	if err := writeStatus(dir, rec); err != nil {
		t.Fatal(err)
	}
	if err := markRun(dir, id); err != nil {
		t.Fatal(err)
	}
}

func TestSupervise_RelaunchesInterruptedTask(t *testing.T) {
	mgr, ws := newTestManager(t)
	dir := filepath.Join(ws, "tasks")
	writeInterrupted(t, dir, "uuid-relaunch", "r", 0, nowEpoch()-1)

	done := make(chan *tools.ToolResult, 1)
	mgr.SuperviseOnce(nowEpoch(), func(_ *TaskRecord) tools.AsyncCallback {
		return func(_ context.Context, r *tools.ToolResult) { done <- r }
	})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted task was not relaunched/completed")
	}
	st, _ := mgr.TaskStatus("uuid-relaunch")
	if st.Status != StatusDone {
		t.Errorf("status = %q, want done", st.Status)
	}
	if st.Restarts != 1 {
		t.Errorf("restarts = %d, want 1", st.Restarts)
	}
}

func TestSupervise_GivesUpAfterMaxRestarts(t *testing.T) {
	mgr, ws := newTestManager(t)
	dir := filepath.Join(ws, "tasks")
	writeInterrupted(t, dir, "uuid-giveup", "g", global.TaskMaxRestarts, nowEpoch()-1)

	mgr.SuperviseOnce(nowEpoch(), func(*TaskRecord) tools.AsyncCallback { return nil })

	st, _ := mgr.TaskStatus("uuid-giveup")
	if st.Status != StatusError {
		t.Errorf("status = %q, want error", st.Status)
	}
	if _, err := os.Stat(runPath(dir, "uuid-giveup")); !os.IsNotExist(err) {
		t.Errorf("run marker should be removed after giving up")
	}
}

func TestSupervise_RespectsCooldown(t *testing.T) {
	mgr, ws := newTestManager(t)
	dir := filepath.Join(ws, "tasks")
	writeInterrupted(t, dir, "uuid-cooldown", "c", 0, nowEpoch()+3600) // not yet eligible

	mgr.SuperviseOnce(nowEpoch(), func(*TaskRecord) tools.AsyncCallback {
		t.Fatal("task should not be relaunched while cooling down")
		return nil
	})

	st, _ := mgr.TaskStatus("uuid-cooldown")
	if st.Status != StatusRunning {
		t.Errorf("status = %q, want running (untouched)", st.Status)
	}
}
