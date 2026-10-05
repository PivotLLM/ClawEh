package schedule

import (
	"testing"
)

// A sub-agent (a temporary clone, with its own agent id) schedules for the
// agent it is a copy of; without the home lookup the clone's own id is used
// and the job has nowhere to go.
func TestCronTool_CloneSchedulesForItsSource(t *testing.T) {
	add := map[string]any{"action": "add", "message": "stretch", "at_seconds": float64(600)}

	tool := newTestCronTool(t)
	if res := tool.Execute(agentCtx("c1"), add); !res.IsError {
		t.Fatalf("without the home lookup the clone's id has no default channel; got %s", res.ForLLM)
	}

	tool.SetHomeAgent(func(id string) string {
		if id == "c1" {
			return "alice"
		}
		return id
	})
	if res := tool.Execute(agentCtx("c1"), add); res.IsError {
		t.Fatalf("add from alice's clone: %s", res.ForLLM)
	}
	jobs := tool.cronService.ListJobs(true)
	if len(jobs) != 1 || jobs[0].AgentID != "alice" {
		t.Fatalf("jobs = %+v, want one job addressed to alice", jobs)
	}
}
