// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

var turnIDPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)

func TestNewTurnIDShape(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		id := newTurnID()
		if !turnIDPattern.MatchString(id) {
			t.Fatalf("turn id %q is not 8 hex characters", id)
		}
		seen[id] = true
	}
	if len(seen) < 45 {
		t.Errorf("turn ids barely vary: %d distinct of 50", len(seen))
	}
}

func TestTurnFieldsStampsOnlyInsideTurn(t *testing.T) {
	fields := turnFields(context.Background(), map[string]any{"a": 1})
	if _, ok := fields["turn_id"]; ok {
		t.Error("turn_id set outside a turn")
	}
	ctx := withTurnID(context.Background(), "deadbeef")
	fields = turnFields(ctx, map[string]any{"a": 1})
	if fields["turn_id"] != "deadbeef" || fields["a"] != 1 {
		t.Errorf("fields = %v", fields)
	}
	if turnIDFrom(context.Background()) != "" {
		t.Error("turnIDFrom on a bare context should be empty")
	}
}

// logLines parses the captured zerolog stream into one map per line.
func logLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		lines = append(lines, ev)
	}
	return lines
}

// TestRunTurn_LogsCarryTurnID: a turn started by runTurn stamps one id, and
// the per-turn log lines (inbound, routing, outbound) all carry it.
func TestRunTurn_LogsCarryTurnID(t *testing.T) {
	var buf safeBufLoop
	restore := logger.RedirectForTest(&buf)
	defer restore()

	al := newTestAgentLoop(t).al
	msg := bus.InboundMessage{
		Channel:  "cli",
		SenderID: "tester",
		ChatID:   "direct",
		Content:  "hello",
	}
	al.runTurn(context.Background(), context.Background(), msg)

	want := map[string]bool{
		"Routed message":              false,
		"Published outbound response": false,
	}
	var turnID string
	for _, ev := range logLines(t, buf.String()) {
		msgText, ok := ev["message"].(string)
		if !ok {
			continue
		}
		isProcessing := strings.HasPrefix(msgText, "Processing message from ")
		if _, tracked := want[msgText]; !tracked && !isProcessing {
			continue
		}
		id, ok := ev["turn_id"].(string)
		if !ok || !turnIDPattern.MatchString(id) {
			t.Fatalf("%q line has turn_id %q, want 8 hex: %v", msgText, id, ev)
		}
		if turnID == "" {
			turnID = id
		} else if id != turnID {
			t.Fatalf("%q line has turn_id %q, other lines have %q", msgText, id, turnID)
		}
		if isProcessing {
			continue
		}
		want[msgText] = true
	}
	if turnID == "" {
		t.Fatalf("no per-turn log line found in output:\n%s", buf.String())
	}
	for m, ok := range want {
		if !ok {
			t.Errorf("no %q line with the turn id; output:\n%s", m, buf.String())
		}
	}
}

// TestProcessMessage_DirectCallGetsTurnID: a direct call (CLI, cron) that does
// not come through runTurn still gets an id, and an id already on the context
// is kept.
func TestProcessMessage_DirectCallGetsTurnID(t *testing.T) {
	var buf safeBufLoop
	restore := logger.RedirectForTest(&buf)
	defer restore()

	al := newTestAgentLoop(t).al
	if _, err := al.ProcessDirect(context.Background(), "hello", "direct-session"); err != nil {
		t.Fatalf("ProcessDirect: %v", err)
	}
	ctx := withTurnID(context.Background(), "feedf00d")
	if _, err := al.ProcessDirect(ctx, "hello again", "direct-session"); err != nil {
		t.Fatalf("ProcessDirect: %v", err)
	}

	var ids []string
	for _, ev := range logLines(t, buf.String()) {
		m, ok := ev["message"].(string)
		if !ok || m != "Routed message" {
			continue
		}
		id, ok := ev["turn_id"].(string)
		if !ok {
			t.Fatalf("Routed message turn_id is %T, want string: %v", ev["turn_id"], ev)
		}
		ids = append(ids, id)
	}
	if len(ids) != 2 {
		t.Fatalf("Routed message lines = %d, want 2:\n%s", len(ids), buf.String())
	}
	if !turnIDPattern.MatchString(ids[0]) {
		t.Errorf("first direct turn id = %q, want generated 8 hex", ids[0])
	}
	if ids[1] != "feedf00d" {
		t.Errorf("second direct turn id = %q, want the caller's feedf00d", ids[1])
	}
}

// TestToolCall_AuditRecorded: every tool the loop runs lands in the audit log
// with the agent, session, channel, sender, redacted arguments, outcome,
// duration and the turn id — and never the raw argument content.
func TestToolCall_AuditRecorded(t *testing.T) {
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

	al := newTestAgentLoop(t).al
	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	agentInstance.Tools.Register(&noopWriteFile{})
	if agentInstance.Config != nil {
		agentInstance.Config.Tools = []string{"*"}
	}

	secret := strings.Repeat("S", 4096)
	argsJSON, err := json.Marshal(map[string]any{"path": "/tmp/diary.txt", "content": secret})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	agentInstance.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			{ToolCalls: []providers.ToolCall{{
				ID: "tc-1", Type: "function", Name: "file_write",
				Function: &providers.FunctionCall{Name: "file_write", Arguments: string(argsJSON)},
			}}},
			{Content: "done"},
		},
		errors: []error{nil, nil},
	}

	opts := processOptions{
		SessionKey: "audit-loop",
		Channel:    "slack",
		ChatID:     "C123",
		SenderID:   "U42",
	}
	cm, releaseCM := al.getContextManager(agentInstance, opts.SessionKey)
	defer releaseCM()

	ctx := withTurnID(context.Background(), "cafe0001")
	messages := []providers.Message{{Role: "user", Content: "go"}}
	if _, _, _, _, _, err = al.runLLMIteration(ctx, agentInstance, messages, opts, cm, nil); err != nil {
		t.Fatalf("runLLMIteration: %v", err)
	}

	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = audit.Default().Flush(flushCtx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	rows, err := audit.Default().Query(context.Background(), audit.Filter{Kind: audit.KindToolCall})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("tool_call rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.Agent != agentInstance.ID || got.Session != "audit-loop" || got.Channel != "slack" ||
		got.Sender != "U42" || got.Tool != "file_write" || got.Outcome != audit.OutcomeOK ||
		got.TurnID != "cafe0001" || got.DurationMS < 0 {
		t.Errorf("row = %+v", got)
	}
	if strings.Contains(got.Details, secret) || strings.Contains(got.Details, "SSSS") {
		t.Fatal("raw tool argument content reached the audit log")
	}
	var details struct {
		ChatID string         `json:"chat_id"`
		Args   map[string]any `json:"args"`
	}
	if err := json.Unmarshal([]byte(got.Details), &details); err != nil {
		t.Fatalf("details not JSON: %v: %s", err, got.Details)
	}
	if details.ChatID != "C123" {
		t.Errorf("details.chat_id = %q", details.ChatID)
	}
	if details.Args["path"] != "/tmp/diary.txt" || details.Args["content_bytes"] != float64(len(secret)) {
		t.Errorf("redacted args = %v", details.Args)
	}
}

// TestToolCall_AuditOutcomeError: a failing tool is recorded as an error.
func TestToolCall_AuditOutcomeError(t *testing.T) {
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

	al := newTestAgentLoop(t).al
	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance.Config != nil {
		agentInstance.Config.Tools = []string{"*"}
	}
	agentInstance.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			{ToolCalls: []providers.ToolCall{{
				ID: "tc-1", Type: "function", Name: "no_such_tool",
				Function: &providers.FunctionCall{Name: "no_such_tool", Arguments: "{}"},
			}}},
			{Content: "done"},
		},
		errors: []error{nil, nil},
	}
	opts := processOptions{SessionKey: "audit-err", Channel: "cli", ChatID: "direct"}
	cm, releaseCM := al.getContextManager(agentInstance, opts.SessionKey)
	defer releaseCM()
	messages := []providers.Message{{Role: "user", Content: "go"}}
	if _, _, _, _, _, err := al.runLLMIteration(context.Background(), agentInstance, messages, opts, cm, nil); err != nil {
		t.Fatalf("runLLMIteration: %v", err)
	}

	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := audit.Default().Flush(flushCtx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	rows, err := audit.Default().Query(context.Background(), audit.Filter{Kind: audit.KindToolCall})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 1 || rows[0].Tool != "no_such_tool" || rows[0].Outcome != audit.OutcomeError {
		t.Fatalf("rows = %+v, want one error row for no_such_tool", rows)
	}
}
