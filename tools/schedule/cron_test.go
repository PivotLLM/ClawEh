package schedule

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/cron"
	"github.com/PivotLLM/ClawEh/tools"
)

// testConfig builds a config where alice and bob each have a concrete default
// channel, boss has global_cron (may schedule for others), and nodefault has a
// binding that is NOT marked default (so it has no delivery channel).
func testConfig() *config.Config {
	peer := func(id string) *config.PeerMatch { return &config.PeerMatch{Kind: "channel", ID: id} }
	return &config.Config{
		Bindings: []config.AgentBinding{
			{AgentID: "alice", Default: true, Match: config.BindingMatch{Channel: "telegram-Alice", Peer: peer("chat-alice")}},
			{AgentID: "bob", Default: true, Match: config.BindingMatch{Channel: "telegram-Bob", Peer: peer("chat-bob")}},
			{AgentID: "tester", Default: true, Match: config.BindingMatch{Channel: "cli", Peer: peer("direct")}},
			// third: a Telegram-style default — channel-only binding (no peer) plus
			// an explicit DeliverTo chat id.
			{AgentID: "third", Default: true, DeliverTo: "12345", Match: config.BindingMatch{Channel: "telegram-Third"}},
			{AgentID: "nodefault", Match: config.BindingMatch{Channel: "slack", Peer: peer("c1")}},
		},
		Agents: config.AgentsConfig{List: []config.AgentConfig{
			{ID: "alice"}, {ID: "bob"}, {ID: "tester"}, {ID: "third"}, {ID: "boss", GlobalCron: true}, {ID: "nodefault"},
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
	result := tool.Execute(agentCtx("alice"), map[string]any{
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
	if jobs[0].AgentID != "alice" {
		t.Fatalf("job should be addressed to alice, got %q", jobs[0].AgentID)
	}
	if jobs[0].Payload.Channel != "" || jobs[0].Payload.To != "" {
		t.Fatalf("destination must be resolved at fire time, not stored; got %s/%s",
			jobs[0].Payload.Channel, jobs[0].Payload.To)
	}

	// Fire it: delivery resolves alice's default channel.
	out, err := tool.ExecuteJob(context.Background(), &jobs[0])
	if err != nil || out != "ok" {
		t.Fatalf("ExecuteJob = %q, %v, want ok", out, err)
	}
}

// TestCronTool_CrossAgentRequiresGlobalCron verifies an ordinary agent cannot
// schedule for another, but a global_cron agent can.
func TestCronTool_CrossAgentRequiresGlobalCron(t *testing.T) {
	tool := newTestCronTool(t)

	// alice (no global_cron) targeting bob → denied.
	denied := tool.Execute(agentCtx("alice"), map[string]any{
		"action": "add", "agent": "bob", "message": "x", "at_seconds": float64(60),
	})
	if !denied.IsError || !strings.Contains(denied.ForLLM, "global_cron") {
		t.Fatalf("cross-agent without global_cron should be denied, got: isErr=%v %s", denied.IsError, denied.ForLLM)
	}

	// boss (global_cron) targeting bob → allowed, job addressed to bob.
	ok := tool.Execute(agentCtx("boss"), map[string]any{
		"action": "add", "agent": "bob", "message": "weekly report", "every_seconds": float64(3600),
	})
	if ok.IsError {
		t.Fatalf("boss scheduling for bob should succeed, got: %s", ok.ForLLM)
	}
	jobs := tool.cronService.ListJobs(true)
	if len(jobs) != 1 || jobs[0].AgentID != "bob" {
		t.Fatalf("job should be addressed to bob, got %+v", jobs)
	}
}

// TestCronTool_TelegramDeliverTo verifies an agent whose default channel is a
// broadly-bound Telegram bot (no peer) but has an explicit DeliverTo can both
// schedule and fire — the cron deliver-to path.
func TestCronTool_TelegramDeliverTo(t *testing.T) {
	tool := newTestCronTool(t)
	add := tool.Execute(agentCtx("third"), map[string]any{
		"action": "add", "message": "drink water", "every_seconds": float64(3600),
	})
	if add.IsError {
		t.Fatalf("third add should succeed via deliver_to, got: %s", add.ForLLM)
	}
	jobs := tool.cronService.ListJobs(true)
	if len(jobs) != 1 || jobs[0].AgentID != "third" {
		t.Fatalf("expected one job addressed to third, got %+v", jobs)
	}
	if out, err := tool.ExecuteJob(context.Background(), &jobs[0]); err != nil || out != "ok" {
		t.Fatalf("third job ExecuteJob = %q, want ok", out)
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
	if out, err := tool.ExecuteJob(context.Background(), job); err != nil || out != "ok" {
		t.Fatalf("operator job ExecuteJob = %q, %v, want ok", out, err)
	}

	// With neither agent id nor explicit channel/to → skipped.
	bare := &cron.CronJob{ID: "op2", Payload: cron.CronPayload{Message: "x"}}
	if _, err := tool.ExecuteJob(context.Background(), bare); err == nil {
		t.Fatal("job with no agent and no channel/to must fail, so the scheduler records and alerts it")
	}
}

// TestCronTool_GetJob returns full detail for one job and errors on a bad id.
func TestCronTool_GetJob(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := agentCtx("alice")
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

	// Alice creates a job.
	add := tool.Execute(agentCtx("alice"), map[string]any{
		"action": "add", "message": "alice's reminder", "every_seconds": float64(3600),
	})
	if add.IsError {
		t.Fatalf("alice add failed: %s", add.ForLLM)
	}
	jobID := tool.cronService.ListJobs(true)[0].ID

	// Bob cannot see it.
	bob := agentCtx("bob")
	list := tool.Execute(bob, map[string]any{"action": "list"})
	if !strings.Contains(list.ForLLM, "No scheduled jobs") {
		t.Fatalf("bob should see no jobs, got: %s", list.ForLLM)
	}
	// Bob cannot get/remove/disable it (reported as not found).
	for _, action := range []string{"get", "remove", "disable"} {
		res := tool.Execute(bob, map[string]any{"action": action, "job_id": jobID})
		if !res.IsError || !strings.Contains(res.ForLLM, "not found") {
			t.Fatalf("bob %s on alice's job should be 'not found', got: isErr=%v %s",
				action, res.IsError, res.ForLLM)
		}
	}

	// Alice still sees and can remove her own job.
	if l := tool.Execute(agentCtx("alice"), map[string]any{"action": "list"}); !strings.Contains(l.ForLLM, "alice's reminder") {
		t.Fatalf("alice should see her own job, got: %s", l.ForLLM)
	}
	if r := tool.Execute(agentCtx("alice"), map[string]any{"action": "remove", "job_id": jobID}); r.IsError {
		t.Fatalf("alice should be able to remove her own job, got: %s", r.ForLLM)
	}
}
