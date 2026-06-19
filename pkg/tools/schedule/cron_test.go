package schedule

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/cron"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// testConfig builds a config where amber and karen each have a concrete default
// channel, boss has global_cron (may schedule for others), and nodefault has a
// binding that is NOT marked default (so it has no delivery channel).
func testConfig() *config.Config {
	peer := func(id string) *config.PeerMatch { return &config.PeerMatch{Kind: "channel", ID: id} }
	return &config.Config{
		Bindings: []config.AgentBinding{
			{AgentID: "amber", Default: true, Match: config.BindingMatch{Channel: "telegram-Amber", Peer: peer("chat-amber")}},
			{AgentID: "karen", Default: true, Match: config.BindingMatch{Channel: "telegram-Karen", Peer: peer("chat-karen")}},
			{AgentID: "tester", Default: true, Match: config.BindingMatch{Channel: "cli", Peer: peer("direct")}},
			{AgentID: "nodefault", Match: config.BindingMatch{Channel: "slack", Peer: peer("c1")}},
		},
		Agents: config.AgentsConfig{List: []config.AgentConfig{
			{ID: "amber"}, {ID: "karen"}, {ID: "tester"}, {ID: "boss", GlobalCron: true}, {ID: "nodefault"},
		}},
	}
}

func newTestCronTool(t *testing.T) *CronTool {
	t.Helper()
	storePath := filepath.Join(t.TempDir(), "cron.json")
	cronService := cron.NewCronService(storePath, nil)
	msgBus := bus.NewMessageBus()
	cfg := testConfig()
	return NewCronTool(cronService, msgBus, func() *config.Config { return cfg })
}

// agentCtx builds a tool context carrying the caller's session key (cron derives
// the caller agent id from it). Channel/chat are no longer used by add.
func agentCtx(agentID string) context.Context {
	return tools.WithSessionKey(context.Background(), "agent:"+agentID+":main")
}

// TestCronTool_AddRequiresDefaultChannel verifies add fails when the target agent
// has no default channel configured.
func TestCronTool_AddRequiresDefaultChannel(t *testing.T) {
	tool := newTestCronTool(t)
	result := tool.Execute(agentCtx("nodefault"), map[string]any{
		"action":     "add",
		"message":    "reminder",
		"at_seconds": float64(60),
	})
	if !result.IsError {
		t.Fatal("expected error when agent has no default channel")
	}
	if !strings.Contains(result.ForLLM, "no default channel") {
		t.Errorf("expected 'no default channel' message, got: %s", result.ForLLM)
	}
}

// TestCronTool_AddAddressesAgent verifies the job is addressed to the agent (not
// a captured channel), with no destination stored on the payload — and that
// ExecuteJob resolves the agent's default channel at fire time.
func TestCronTool_AddAddressesAgent(t *testing.T) {
	tool := newTestCronTool(t)
	result := tool.Execute(agentCtx("amber"), map[string]any{
		"action":     "add",
		"message":    "time to stretch",
		"at_seconds": float64(600),
	})
	if result.IsError {
		t.Fatalf("expected add to succeed, got: %s", result.ForLLM)
	}

	jobs := tool.cronService.ListJobs(true)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].AgentID != "amber" {
		t.Fatalf("job should be addressed to amber, got %q", jobs[0].AgentID)
	}
	if jobs[0].Payload.Channel != "" || jobs[0].Payload.To != "" {
		t.Fatalf("destination must be resolved at fire time, not stored; got %s/%s",
			jobs[0].Payload.Channel, jobs[0].Payload.To)
	}

	// Fire it: delivery resolves amber's default channel.
	out := tool.ExecuteJob(context.Background(), &jobs[0])
	if out != "ok" {
		t.Fatalf("ExecuteJob = %q, want ok", out)
	}
}

// TestCronTool_CrossAgentRequiresGlobalCron verifies an ordinary agent cannot
// schedule for another, but a global_cron agent can.
func TestCronTool_CrossAgentRequiresGlobalCron(t *testing.T) {
	tool := newTestCronTool(t)

	// amber (no global_cron) targeting karen → denied.
	denied := tool.Execute(agentCtx("amber"), map[string]any{
		"action": "add", "agent": "karen", "message": "x", "at_seconds": float64(60),
	})
	if !denied.IsError || !strings.Contains(denied.ForLLM, "global_cron") {
		t.Fatalf("cross-agent without global_cron should be denied, got: isErr=%v %s", denied.IsError, denied.ForLLM)
	}

	// boss (global_cron) targeting karen → allowed, job addressed to karen.
	ok := tool.Execute(agentCtx("boss"), map[string]any{
		"action": "add", "agent": "karen", "message": "weekly report", "every_seconds": float64(3600),
	})
	if ok.IsError {
		t.Fatalf("boss scheduling for karen should succeed, got: %s", ok.ForLLM)
	}
	jobs := tool.cronService.ListJobs(true)
	if len(jobs) != 1 || jobs[0].AgentID != "karen" {
		t.Fatalf("job should be addressed to karen, got %+v", jobs)
	}
}

// TestCronTool_ExecuteJobOperatorFallback verifies an operator/CLI job (no agent
// id, explicit channel/to on the payload) still delivers to that explicit target.
func TestCronTool_ExecuteJobOperatorFallback(t *testing.T) {
	tool := newTestCronTool(t)
	job := &cron.CronJob{
		ID:      "op1",
		Payload: cron.CronPayload{Message: "operator job", Channel: "slack", To: "C9", PeerKind: "channel"},
	}
	if out := tool.ExecuteJob(context.Background(), job); out != "ok" {
		t.Fatalf("operator job ExecuteJob = %q, want ok", out)
	}

	// With neither agent id nor explicit channel/to → skipped.
	bare := &cron.CronJob{ID: "op2", Payload: cron.CronPayload{Message: "x"}}
	if out := tool.ExecuteJob(context.Background(), bare); out == "ok" {
		t.Fatal("job with no agent and no channel/to should be skipped")
	}
}

// TestCronTool_GetJob returns full detail for one job and errors on a bad id.
func TestCronTool_GetJob(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := agentCtx("amber")
	add := tool.Execute(ctx, map[string]any{
		"action": "add", "message": "daily standup reminder", "every_seconds": float64(3600),
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
	add := tool.Execute(agentCtx("amber"), map[string]any{
		"action": "add", "message": "amber's reminder", "every_seconds": float64(3600),
	})
	if add.IsError {
		t.Fatalf("amber add failed: %s", add.ForLLM)
	}
	jobID := tool.cronService.ListJobs(true)[0].ID

	// Karen cannot see it.
	karen := agentCtx("karen")
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
	if l := tool.Execute(agentCtx("amber"), map[string]any{"action": "list"}); !strings.Contains(l.ForLLM, "amber's reminder") {
		t.Fatalf("amber should see her own job, got: %s", l.ForLLM)
	}
	if r := tool.Execute(agentCtx("amber"), map[string]any{"action": "remove", "job_id": jobID}); r.IsError {
		t.Fatalf("amber should be able to remove her own job, got: %s", r.ForLLM)
	}
}
