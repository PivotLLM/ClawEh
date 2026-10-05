// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
)

// A sub-agent's tool calls are audited under the agent it copies, so the
// agent filter still finds them, with the clone named in the details.
func TestToolCall_AuditCloneUnderSource(t *testing.T) {
	if err := audit.Init(t.TempDir()); err != nil {
		t.Fatalf("audit.Init: %v", err)
	}
	t.Cleanup(func() {
		if err := audit.Close(); err != nil {
			t.Errorf("audit.Close: %v", err)
		}
	})
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	provider := &sequenceProvider{
		responses: []*providers.LLMResponse{
			{ToolCalls: []providers.ToolCall{toolCallTo("tc-1", "file_write")}},
			{Content: "done"},
		},
		errors: []error{nil, nil},
	}
	al := newSubagentTestLoop(t, provider)
	al.RegisterTool(&noopWriteFile{})

	res, release, err := al.runSubagentTask(context.Background(), "main", "write it", "", nil)
	defer release()
	if err != nil {
		t.Fatalf("runSubagentTask: %v", err)
	}
	pk := routing.ParseAgentSessionKey(res.SessionKey)
	if pk == nil {
		t.Fatalf("session key %q", res.SessionKey)
	}

	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = audit.Default().Flush(flushCtx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	rows, err := audit.Default().Query(context.Background(), audit.Filter{Kind: audit.KindToolCall, Agent: "main"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("tool_call rows under main = %d, want the clone's one", len(rows))
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(rows[0].Details), &details); err != nil {
		t.Fatalf("details not JSON: %v", err)
	}
	if details["clone"] != agentreg.ShortID(pk.AgentID) {
		t.Fatalf("details = %s, want clone %q", rows[0].Details, agentreg.ShortID(pk.AgentID))
	}
}
