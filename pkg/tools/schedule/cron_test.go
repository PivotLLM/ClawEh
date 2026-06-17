package schedule

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/cron"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

func newTestCronTool(t *testing.T) *CronTool {
	t.Helper()
	storePath := filepath.Join(t.TempDir(), "cron.json")
	cronService := cron.NewCronService(storePath, nil)
	msgBus := bus.NewMessageBus()
	return NewCronTool(cronService, msgBus)
}

// TestCronTool_AddJobRequiresSessionContext verifies fail-closed when channel/chatID missing.
func TestCronTool_AddJobRequiresSessionContext(t *testing.T) {
	tool := newTestCronTool(t)
	result := tool.Execute(context.Background(), map[string]any{
		"action":     "add",
		"message":    "reminder",
		"at_seconds": float64(60),
	})

	if !result.IsError {
		t.Fatal("expected error when session context is missing")
	}
	if !strings.Contains(result.ForLLM, "no session context") {
		t.Errorf("expected 'no session context' message, got: %s", result.ForLLM)
	}
}

// TestCronTool_AddJobCapturesSessionChannel verifies the job is created against
// the channel/chat from context (the model cannot choose a target) — a supplied
// channel/chat_id is ignored.
func TestCronTool_AddJobCapturesSessionChannel(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := tools.WithToolContext(context.Background(), "telegram-Amber", "chat-1")
	result := tool.Execute(ctx, map[string]any{
		"action":     "add",
		"message":    "time to stretch",
		"at_seconds": float64(600),
		"channel":    "slack-other", // must be ignored
		"chat_id":    "someone-else", // must be ignored
	})
	if result.IsError {
		t.Fatalf("expected add to succeed, got: %s", result.ForLLM)
	}

	jobs := tool.cronService.ListJobs(true)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Payload.Channel != "telegram-Amber" || jobs[0].Payload.To != "chat-1" {
		t.Fatalf("job target should be the session channel/chat, got %s/%s",
			jobs[0].Payload.Channel, jobs[0].Payload.To)
	}
}

// TestCronTool_GetJob returns full detail for one job and errors on a bad id.
func TestCronTool_GetJob(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := tools.WithToolContext(context.Background(), "telegram-Amber", "chat-1")
	add := tool.Execute(ctx, map[string]any{
		"action":     "add",
		"message":    "daily standup reminder",
		"every_seconds": float64(3600),
	})
	if add.IsError {
		t.Fatalf("add failed: %s", add.ForLLM)
	}
	jobID := tool.cronService.ListJobs(true)[0].ID

	got := tool.Execute(ctx, map[string]any{"action": "get", "job_id": jobID})
	if got.IsError {
		t.Fatalf("get failed: %s", got.ForLLM)
	}
	for _, want := range []string{jobID, "daily standup reminder", "every 3600s", "enabled"} {
		if !strings.Contains(got.ForLLM, want) {
			t.Fatalf("get output missing %q:\n%s", want, got.ForLLM)
		}
	}

	missing := tool.Execute(ctx, map[string]any{"action": "get", "job_id": "nope"})
	if !missing.IsError {
		t.Fatalf("expected error for unknown job id, got: %s", missing.ForLLM)
	}
}
