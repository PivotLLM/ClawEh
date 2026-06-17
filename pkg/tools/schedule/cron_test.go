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

// agentCtx builds a tool context for the given agent — channel/chat (captured as
// the job's destination) plus the session key cron uses for per-agent scoping.
func agentCtx(agentID, channel, chatID string) context.Context {
	ctx := tools.WithToolContext(context.Background(), channel, chatID)
	return tools.WithSessionKey(ctx, "agent:"+agentID+":main")
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
		t.Fatal("expected error when no delivery target is available")
	}
	if !strings.Contains(result.ForLLM, "no delivery target") {
		t.Errorf("expected 'no delivery target' message, got: %s", result.ForLLM)
	}
}

// TestCronTool_AddJobMCPFallback verifies that when the session has no bound
// channel (the MCP case), explicit channel/chat_id args are used, and the job is
// still owned by the calling agent (from the session key).
func TestCronTool_AddJobMCPFallback(t *testing.T) {
	tool := newTestCronTool(t)
	// Session key present (→ agent ownership) but NO ToolChannel/ChatID in context.
	ctx := tools.WithSessionKey(context.Background(), "agent:karen:main")
	res := tool.Execute(ctx, map[string]any{
		"action":    "add",
		"message":   "Karen morning",
		"cron_expr": "0 8 * * *",
		"channel":   "slack",
		"chat_id":   "C0ANLEQP5GQ",
	})
	if res.IsError {
		t.Fatalf("expected add to succeed via explicit channel/chat_id, got: %s", res.ForLLM)
	}
	jobs := tool.cronService.ListJobs(true)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Payload.Channel != "slack" || jobs[0].Payload.To != "C0ANLEQP5GQ" {
		t.Fatalf("job target should use explicit args, got %s/%s", jobs[0].Payload.Channel, jobs[0].Payload.To)
	}
	if jobs[0].AgentID != "karen" {
		t.Fatalf("job should be owned by caller karen, got %q", jobs[0].AgentID)
	}
}

// TestCronTool_AddJobCapturesSessionChannel verifies the job is created against
// the channel/chat from context (the model cannot choose a target) — a supplied
// channel/chat_id is ignored.
func TestCronTool_AddJobCapturesSessionChannel(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := agentCtx("amber", "telegram-Amber", "chat-1")
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
	ctx := agentCtx("amber", "telegram-Amber", "chat-1")
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

// TestCronTool_ScopedToAgent verifies one agent cannot see or manage another
// agent's jobs: list hides them, and get/remove/disable report "not found".
func TestCronTool_ScopedToAgent(t *testing.T) {
	tool := newTestCronTool(t)

	// Amber creates a job.
	amber := agentCtx("amber", "telegram-Amber", "chat-1")
	add := tool.Execute(amber, map[string]any{
		"action": "add", "message": "amber's reminder", "every_seconds": float64(3600),
	})
	if add.IsError {
		t.Fatalf("amber add failed: %s", add.ForLLM)
	}
	jobID := tool.cronService.ListJobs(true)[0].ID

	// Karen cannot see it.
	karen := agentCtx("karen", "telegram-Karen", "chat-2")
	list := tool.Execute(karen, map[string]any{"action": "list"})
	if !strings.Contains(list.ForLLM, "No scheduled jobs") {
		t.Fatalf("karen should see no jobs, got: %s", list.ForLLM)
	}
	// Karen cannot get/remove/disable it (reported as not found).
	for _, action := range []string{"get", "remove", "disable"} {
		res := tool.Execute(karen, map[string]any{"action": action, "job_id": jobID})
		if !res.IsError || !strings.Contains(res.ForLLM, "not found") {
			t.Fatalf("karen %s on amber's job should be 'not found', got: isErr=%v %s",
				action, res.IsError, res.ForLLM)
		}
	}

	// Amber still sees and can remove her own job.
	if l := tool.Execute(amber, map[string]any{"action": "list"}); !strings.Contains(l.ForLLM, "amber's reminder") {
		t.Fatalf("amber should see her own job, got: %s", l.ForLLM)
	}
	if r := tool.Execute(amber, map[string]any{"action": "remove", "job_id": jobID}); r.IsError {
		t.Fatalf("amber should be able to remove her own job, got: %s", r.ForLLM)
	}
}
