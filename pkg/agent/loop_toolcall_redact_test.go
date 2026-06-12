package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/PivotLLM/ClawEh/pkg/utils"
)

// safeBufLoop mirrors the safeBuf helper in pkg/tools tests: a sync-safe
// buffer for capturing zerolog output under -race.
type safeBufLoop struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBufLoop) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBufLoop) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// noopWriteFile models a write_file tool that just returns success. Its
// Name() matches the redaction switch in pkg/tools.RedactArgs, so the
// loop.go INF path will emit a content_bytes summary instead of raw content.
type noopWriteFile struct{}

func (n *noopWriteFile) Name() string        { return "file_write" }
func (n *noopWriteFile) Description() string { return "noop write_file for redaction tests" }
func (n *noopWriteFile) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (n *noopWriteFile) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	return &tools.ToolResult{ForLLM: "ok", Silent: true}
}

// runWriteFileToolCallOnce drives one runLLMIteration that dispatches a
// single write_file tool call with a 10 KiB content payload, then returns
// the captured log buffer for assertions. The 'secret' value is the
// raw content the INF line must NOT carry.
func runWriteFileToolCallOnce(t *testing.T, secret string) string {
	t.Helper()

	var buf safeBufLoop
	restore := logger.RedirectForTest(&buf)
	defer restore()

	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	agentInstance.Tools.Register(&noopWriteFile{})
	if agentInstance.Config != nil {
		agentInstance.Config.Tools = []string{"*"}
	}

	argsJSON, _ := json.Marshal(map[string]any{
		"path":    "/tmp/diary.txt",
		"content": secret,
	})
	agentInstance.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			{
				Content: "",
				ToolCalls: []providers.ToolCall{
					{
						ID:   "tc-1",
						Type: "function",
						Name: "file_write",
						Function: &providers.FunctionCall{
							Name:      "file_write",
							Arguments: string(argsJSON),
						},
					},
				},
			},
			{Content: "done"},
		},
		errors: []error{nil, nil},
	}

	messages := []providers.Message{{Role: "user", Content: "go"}}
	opts := processOptions{
		SessionKey:   "redact-loop",
		Channel:      "cli",
		ChatID:       "direct",
		UserMessage:  "go",
		SendResponse: false,
	}

	cm, releaseCM := al.getContextManager(agentInstance, opts.SessionKey)
	defer releaseCM()

	if _, _, _, _, err := al.runLLMIteration(context.Background(), agentInstance, messages, opts, cm); err != nil {
		t.Fatalf("runLLMIteration: %v", err)
	}

	return buf.String()
}

// findToolDispatchLines pulls the INF + DBG lines emitted by the tool call
// goroutine in loop.go out of a captured zerolog stream.
func findToolDispatchLines(t *testing.T, out string) (infLine, dbgLine string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		msg, _ := ev["message"].(string)
		level, _ := ev["level"].(string)
		switch {
		case msg == "Tool call dispatched" && level == "info":
			infLine = line
		case msg == "Tool call dispatched (raw args)" && level == "debug":
			dbgLine = line
		}
	}
	return infLine, dbgLine
}

// TestRunLLMIteration_WriteFileToolCall_RedactsArgsAtInfo verifies that the
// agent loop's per-tool-call INF log line at loop.go:~1970 carries the
// redacted summary (content_bytes) rather than the raw content payload.
//
// Mutation evidence: revert tools.RedactArgs(tc.Name, tc.Arguments) back to
// utils.Truncate(string(json.Marshal(tc.Arguments)), 200) at loop.go and
// this test fails on the 'raw secret leaked' assertion.
func TestRunLLMIteration_WriteFileToolCall_RedactsArgsAtInfo(t *testing.T) {
	secret := strings.Repeat("S", 10240)
	out := runWriteFileToolCallOnce(t, secret)

	infLine, _ := findToolDispatchLines(t, out)
	if infLine == "" {
		t.Fatalf("expected INF 'Tool call dispatched' line in output: %s", out)
	}
	if strings.Contains(infLine, secret) {
		t.Fatalf("raw write_file content leaked into INF log line")
	}
	if !strings.Contains(infLine, "content_bytes") || !strings.Contains(infLine, "10240") {
		t.Errorf("INF line missing redacted content_bytes summary: %s", infLine)
	}
	// The iteration field is the load-bearing piece of distinct info this
	// site carries over pkg/tools/registry.go's INF (no iteration there).
	if !strings.Contains(infLine, `"iteration"`) {
		t.Errorf("INF line missing iteration context (would make it redundant with registry INF): %s", infLine)
	}
}

// TestRunLLMIteration_WriteFileToolCall_DBGCarriesRawArgs verifies the
// paired DBG line at loop.go carries the full raw arguments so operators
// can still recover the payload with --log-level=debug.
//
// Mutation evidence: delete the DebugCF block at loop.go and this test
// fails with 'expected DBG raw-args companion line'.
func TestRunLLMIteration_WriteFileToolCall_DBGCarriesRawArgs(t *testing.T) {
	secret := strings.Repeat("D", 4096)
	out := runWriteFileToolCallOnce(t, secret)

	_, dbgLine := findToolDispatchLines(t, out)
	if dbgLine == "" {
		t.Fatalf("expected DBG 'Tool call dispatched (raw args)' line in output: %s", out)
	}
	if !strings.Contains(dbgLine, secret) {
		t.Errorf("DBG line should carry the raw args (full secret), got: %s", dbgLine)
	}
}

// TestRunLLMIteration_WriteFileToolCall_NoTruncateCannotUncap verifies that
// flipping the global --no-truncate flag (utils.SetDisableTruncation) has
// no effect on the redacted INF summary. The redaction path bypasses
// utils.Truncate entirely, so write_file content_bytes redaction is
// immune to the operator-facing knob that normally relaxes log truncation.
//
// Mutation evidence: if anyone replaces tools.RedactArgs with a
// utils.Truncate(...)-based preview at loop.go, this test fails because
// the raw secret reappears in the INF line under SetDisableTruncation(true).
func TestRunLLMIteration_WriteFileToolCall_NoTruncateCannotUncap(t *testing.T) {
	utils.SetDisableTruncation(true)
	defer utils.SetDisableTruncation(false)

	secret := strings.Repeat("N", 8192)
	out := runWriteFileToolCallOnce(t, secret)

	infLine, _ := findToolDispatchLines(t, out)
	if infLine == "" {
		t.Fatalf("expected INF 'Tool call dispatched' line in output: %s", out)
	}
	if strings.Contains(infLine, secret) {
		t.Fatalf("--no-truncate uncapped the INF redaction: raw secret leaked into log line")
	}
	if !strings.Contains(infLine, "content_bytes") || !strings.Contains(infLine, "8192") {
		t.Errorf("INF line missing redacted content_bytes summary under --no-truncate: %s", infLine)
	}
}
