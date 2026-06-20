package agents

import (
	"strings"
	"testing"
)

// TestCompletionResult_OrderModeSecurity verifies the user-facing CALLBACK block
// orders Agent (with friendly mode) then Status then Task, points at the results
// file, and that the LLM-facing side carries the untrusted-data security warning.
func TestCompletionResult_OrderModeSecurity(t *testing.T) {
	sm := &SubagentManager{ownerAgentID: "penny"}
	rec := &TaskRecord{
		UUID: "u1", Name: "job", AgentID: "", Mode: "callback",
		Status: StatusDone, ResultsPath: "tasks/u1-results.json",
	}

	res := sm.completionResult(rec)
	fu := res.ForUser
	iAgent, iStatus, iTask := strings.Index(fu, "Agent:"), strings.Index(fu, "Status:"), strings.Index(fu, "Task '")
	if iAgent < 0 || iStatus < 0 || iTask < 0 || !(iAgent < iStatus && iStatus < iTask) {
		t.Errorf("expected order Agent < Status < Task, got:\n%s", fu)
	}
	if !strings.Contains(fu, "(background)") {
		t.Errorf("callback mode should render as (background), got:\n%s", fu)
	}
	if !strings.Contains(fu, "CALLBACK") || !strings.Contains(fu, "tasks/u1-results.json") {
		t.Errorf("block should be marked and point at the results file, got:\n%s", fu)
	}
	if strings.Contains(fu, "SECURITY") {
		t.Errorf("user block should not carry the LLM security warning")
	}
	if !strings.Contains(res.ForLLM, "SECURITY") || !strings.Contains(res.ForLLM, "tasks/u1-results.json") {
		t.Errorf("LLM block should carry the security warning + file pointer, got:\n%s", res.ForLLM)
	}

	// Wait mode renders the friendly "wait" term.
	rec.Mode = "wait"
	if !strings.Contains(sm.completionResult(rec).ForUser, "(wait)") {
		t.Errorf("wait mode should render as (wait)")
	}
}
