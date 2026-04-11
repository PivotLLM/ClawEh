package tools

import (
	"context"
	"strings"
	"testing"
)

// TestCronTool_ListJobs_Empty verifies that listing with no jobs returns an appropriate message.
func TestCronTool_ListJobs_Empty(t *testing.T) {
	tool := newTestCronTool(t)
	result := tool.Execute(context.Background(), map[string]any{
		"action": "list",
	})

	if result.IsError {
		t.Fatalf("list should not error, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "No scheduled jobs") {
		t.Errorf("expected 'No scheduled jobs', got: %s", result.ForLLM)
	}
}

// TestCronTool_ListJobs_WithJob verifies that added jobs appear in the list.
func TestCronTool_ListJobs_WithJob(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")

	// Add a job first
	addResult := tool.Execute(ctx, map[string]any{
		"action":      "add",
		"message":     "remind me to stretch",
		"at_seconds":  float64(600),
	})
	if addResult.IsError {
		t.Fatalf("add failed: %s", addResult.ForLLM)
	}

	// Now list
	listResult := tool.Execute(context.Background(), map[string]any{
		"action": "list",
	})

	if listResult.IsError {
		t.Fatalf("list should not error, got: %s", listResult.ForLLM)
	}
	if !strings.Contains(listResult.ForLLM, "Scheduled jobs") {
		t.Errorf("expected 'Scheduled jobs', got: %s", listResult.ForLLM)
	}
}

// TestCronTool_RemoveJob_NotFound verifies error when removing non-existent job.
func TestCronTool_RemoveJob_NotFound(t *testing.T) {
	tool := newTestCronTool(t)
	result := tool.Execute(context.Background(), map[string]any{
		"action": "remove",
		"job_id": "nonexistent-job-id",
	})

	if !result.IsError {
		t.Fatal("expected error when removing non-existent job")
	}
	if !strings.Contains(result.ForLLM, "not found") {
		t.Errorf("expected 'not found' error, got: %s", result.ForLLM)
	}
}

// TestCronTool_RemoveJob_MissingID verifies error when job_id is missing.
func TestCronTool_RemoveJob_MissingID(t *testing.T) {
	tool := newTestCronTool(t)
	result := tool.Execute(context.Background(), map[string]any{
		"action": "remove",
	})

	if !result.IsError {
		t.Fatal("expected error when job_id is missing")
	}
	if !strings.Contains(result.ForLLM, "job_id is required") {
		t.Errorf("expected 'job_id is required', got: %s", result.ForLLM)
	}
}

// TestCronTool_RemoveJob_Success verifies successful job removal.
func TestCronTool_RemoveJob_Success(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")

	// Add a job to get a valid ID
	addResult := tool.Execute(ctx, map[string]any{
		"action":     "add",
		"message":    "test reminder",
		"at_seconds": float64(3600),
	})
	if addResult.IsError {
		t.Fatalf("add failed: %s", addResult.ForLLM)
	}

	// Extract job ID from the result message
	// Result format: "Cron job added: <name> (id: <id>)"
	msg := addResult.ForLLM
	idStart := strings.Index(msg, "(id: ")
	if idStart == -1 {
		t.Fatalf("could not find job ID in result: %s", msg)
	}
	idStart += len("(id: ")
	idEnd := strings.Index(msg[idStart:], ")")
	if idEnd == -1 {
		t.Fatalf("could not find end of job ID in result: %s", msg)
	}
	jobID := msg[idStart : idStart+idEnd]

	// Remove the job
	removeResult := tool.Execute(context.Background(), map[string]any{
		"action": "remove",
		"job_id": jobID,
	})

	if removeResult.IsError {
		t.Fatalf("remove failed: %s", removeResult.ForLLM)
	}
	if !strings.Contains(removeResult.ForLLM, "removed") {
		t.Errorf("expected 'removed', got: %s", removeResult.ForLLM)
	}
}

// TestCronTool_EnableJob_MissingID verifies error when job_id is missing for enable.
func TestCronTool_EnableJob_MissingID(t *testing.T) {
	tool := newTestCronTool(t)
	result := tool.Execute(context.Background(), map[string]any{
		"action": "enable",
	})

	if !result.IsError {
		t.Fatal("expected error when job_id is missing for enable")
	}
	if !strings.Contains(result.ForLLM, "job_id is required") {
		t.Errorf("expected 'job_id is required', got: %s", result.ForLLM)
	}
}

// TestCronTool_DisableJob_MissingID verifies error when job_id is missing for disable.
func TestCronTool_DisableJob_MissingID(t *testing.T) {
	tool := newTestCronTool(t)
	result := tool.Execute(context.Background(), map[string]any{
		"action": "disable",
	})

	if !result.IsError {
		t.Fatal("expected error when job_id is missing for disable")
	}
}

// TestCronTool_UnknownAction verifies error for unknown actions.
func TestCronTool_UnknownAction(t *testing.T) {
	tool := newTestCronTool(t)
	result := tool.Execute(context.Background(), map[string]any{
		"action": "fly",
	})

	if !result.IsError {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(result.ForLLM, "unknown action") {
		t.Errorf("expected 'unknown action', got: %s", result.ForLLM)
	}
}

// TestCronTool_MissingAction verifies error when action is missing.
func TestCronTool_MissingAction(t *testing.T) {
	tool := newTestCronTool(t)
	result := tool.Execute(context.Background(), map[string]any{})

	if !result.IsError {
		t.Fatal("expected error when action is missing")
	}
	if !strings.Contains(result.ForLLM, "action is required") {
		t.Errorf("expected 'action is required', got: %s", result.ForLLM)
	}
}

// TestCronTool_NameDescriptionParameters verifies the tool metadata.
func TestCronTool_NameDescriptionParameters(t *testing.T) {
	tool := newTestCronTool(t)

	if tool.Name() != "cron" {
		t.Errorf("Name() = %q, want cron", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() should not be empty")
	}
	params := tool.Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("Parameters().properties should be a map")
	}
	if _, ok := props["action"]; !ok {
		t.Error("Parameters() should include 'action'")
	}
}

// TestCronTool_AddJob_EverySeconds verifies recurring job creation with every_seconds.
func TestCronTool_AddJob_EverySeconds(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")

	result := tool.Execute(ctx, map[string]any{
		"action":        "add",
		"message":       "hourly check",
		"every_seconds": float64(3600),
	})

	if result.IsError {
		t.Fatalf("add with every_seconds failed: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "Cron job added") {
		t.Errorf("expected 'Cron job added', got: %s", result.ForLLM)
	}
}

// TestCronTool_AddJob_CronExpr verifies cron expression job creation.
func TestCronTool_AddJob_CronExpr(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")

	result := tool.Execute(ctx, map[string]any{
		"action":    "add",
		"message":   "daily standup",
		"cron_expr": "0 9 * * 1-5",
	})

	if result.IsError {
		t.Fatalf("add with cron_expr failed: %s", result.ForLLM)
	}
}

// TestCronTool_AddJob_MissingMessage verifies error when message is missing.
func TestCronTool_AddJob_MissingMessage(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")

	result := tool.Execute(ctx, map[string]any{
		"action":     "add",
		"at_seconds": float64(60),
		// No message
	})

	if !result.IsError {
		t.Fatal("expected error when message is missing")
	}
	if !strings.Contains(result.ForLLM, "message is required") {
		t.Errorf("expected 'message is required', got: %s", result.ForLLM)
	}
}

// TestCronTool_AddJob_NoSchedule verifies error when no schedule type is provided.
func TestCronTool_AddJob_NoSchedule(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")

	result := tool.Execute(ctx, map[string]any{
		"action":  "add",
		"message": "reminder with no schedule",
		// No at_seconds, every_seconds, or cron_expr
	})

	if !result.IsError {
		t.Fatal("expected error when no schedule is provided")
	}
	if !strings.Contains(result.ForLLM, "at_seconds") {
		t.Errorf("expected schedule hint in error, got: %s", result.ForLLM)
	}
}

// TestCronTool_EnableDisable_Success verifies enabling and disabling a job.
func TestCronTool_EnableDisable_Success(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")

	// Add a job
	addResult := tool.Execute(ctx, map[string]any{
		"action":     "add",
		"message":    "enable/disable test",
		"at_seconds": float64(3600),
	})
	if addResult.IsError {
		t.Fatalf("add failed: %s", addResult.ForLLM)
	}

	// Extract job ID
	msg := addResult.ForLLM
	idStart := strings.Index(msg, "(id: ")
	if idStart == -1 {
		t.Fatalf("could not find job ID in: %s", msg)
	}
	idStart += len("(id: ")
	idEnd := strings.Index(msg[idStart:], ")")
	if idEnd == -1 {
		t.Fatalf("could not find end of job ID in: %s", msg)
	}
	jobID := msg[idStart : idStart+idEnd]

	// Disable
	disableResult := tool.Execute(context.Background(), map[string]any{
		"action": "disable",
		"job_id": jobID,
	})
	if disableResult.IsError {
		t.Fatalf("disable failed: %s", disableResult.ForLLM)
	}
	if !strings.Contains(disableResult.ForLLM, "disabled") {
		t.Errorf("expected 'disabled', got: %s", disableResult.ForLLM)
	}

	// Enable
	enableResult := tool.Execute(context.Background(), map[string]any{
		"action": "enable",
		"job_id": jobID,
	})
	if enableResult.IsError {
		t.Fatalf("enable failed: %s", enableResult.ForLLM)
	}
	if !strings.Contains(enableResult.ForLLM, "enabled") {
		t.Errorf("expected 'enabled', got: %s", enableResult.ForLLM)
	}
}
