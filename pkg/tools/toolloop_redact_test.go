package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/utils"
)

// writeFileSilentTool is a write_file-named tool that silently returns
// success — enough to drive RunToolLoop through its INF/DBG dispatch lines.
type writeFileSilentTool struct{}

func (writeFileSilentTool) Name() string        { return "write_file" }
func (writeFileSilentTool) Description() string { return "noop write_file for redaction tests" }
func (writeFileSilentTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (writeFileSilentTool) Execute(_ context.Context, _ map[string]any) *ToolResult {
	return &ToolResult{ForLLM: "ok", Silent: true}
}

// runToolLoopWriteFileOnce drives a single tool call through RunToolLoop
// with a write_file payload and returns the captured log output.
func runToolLoopWriteFileOnce(t *testing.T, secret string) string {
	t.Helper()

	var buf safeBuf
	restore := logger.RedirectForTest(&buf)
	defer restore()

	argsJSON, _ := json.Marshal(map[string]any{
		"path":    "/tmp/diary.txt",
		"content": secret,
	})

	provider := &mockLLMProvider{
		responses: []*providers.LLMResponse{
			{
				FinishReason: "tool_calls",
				ToolCalls: []providers.ToolCall{
					{
						ID:   "tc-1",
						Type: "function",
						Name: "write_file",
						Function: &providers.FunctionCall{
							Name:      "write_file",
							Arguments: string(argsJSON),
						},
					},
				},
			},
			{Content: "done", FinishReason: "stop"},
		},
	}

	registry := NewToolRegistry()
	registry.Register(writeFileSilentTool{})

	cfg := ToolLoopConfig{
		Provider:      provider,
		Model:         "mock-model",
		Tools:         registry,
		MaxIterations: 4,
	}

	messages := []providers.Message{{Role: "user", Content: "go"}}
	if _, err := RunToolLoop(context.Background(), cfg, messages, "cli", "direct"); err != nil {
		t.Fatalf("RunToolLoop: %v", err)
	}
	return buf.String()
}

func findToolloopDispatchLines(t *testing.T, out string) (infLine, dbgLine string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		msg, _ := ev["message"].(string)
		level, _ := ev["level"].(string)
		caller, _ := ev["caller"].(string)
		if !strings.HasPrefix(caller, "toolloop") {
			continue
		}
		switch {
		case msg == "Tool call dispatched" && level == "info":
			infLine = line
		case msg == "Tool call dispatched (raw args)" && level == "debug":
			dbgLine = line
		}
	}
	return infLine, dbgLine
}

// TestRunToolLoop_WriteFileToolCall_RedactsArgsAtInfo verifies the toolloop.go
// per-tool-call INF line carries content_bytes, not raw content.
//
// Mutation evidence: revert the RedactArgs(...) call back to
// utils.Truncate(string(json.Marshal(tc.Arguments)), 200) at toolloop.go and
// this test fails on the 'raw secret leaked' assertion.
func TestRunToolLoop_WriteFileToolCall_RedactsArgsAtInfo(t *testing.T) {
	secret := strings.Repeat("S", 10240)
	out := runToolLoopWriteFileOnce(t, secret)

	infLine, _ := findToolloopDispatchLines(t, out)
	if infLine == "" {
		t.Fatalf("expected INF 'Tool call dispatched' line in toolloop output: %s", out)
	}
	if strings.Contains(infLine, secret) {
		t.Fatalf("raw write_file content leaked into toolloop INF log line")
	}
	if !strings.Contains(infLine, "content_bytes") || !strings.Contains(infLine, "10240") {
		t.Errorf("INF line missing redacted content_bytes summary: %s", infLine)
	}
}

// TestRunToolLoop_WriteFileToolCall_DBGCarriesRawArgs verifies the paired DBG
// line in toolloop.go carries the full raw arguments.
//
// Mutation evidence: delete the DebugCF block at toolloop.go and this test
// fails with 'expected DBG raw-args companion line'.
func TestRunToolLoop_WriteFileToolCall_DBGCarriesRawArgs(t *testing.T) {
	secret := strings.Repeat("D", 4096)
	out := runToolLoopWriteFileOnce(t, secret)

	_, dbgLine := findToolloopDispatchLines(t, out)
	if dbgLine == "" {
		t.Fatalf("expected DBG 'Tool call dispatched (raw args)' line in toolloop output: %s", out)
	}
	if !strings.Contains(dbgLine, secret) {
		t.Errorf("DBG line should carry the raw args, got: %s", dbgLine)
	}
}

// TestRunToolLoop_WriteFileToolCall_NoTruncateCannotUncap verifies the
// global --no-truncate flag does not uncap toolloop.go's INF redaction.
func TestRunToolLoop_WriteFileToolCall_NoTruncateCannotUncap(t *testing.T) {
	utils.SetDisableTruncation(true)
	defer utils.SetDisableTruncation(false)

	secret := strings.Repeat("N", 8192)
	out := runToolLoopWriteFileOnce(t, secret)

	infLine, _ := findToolloopDispatchLines(t, out)
	if infLine == "" {
		t.Fatalf("expected INF 'Tool call dispatched' line: %s", out)
	}
	if strings.Contains(infLine, secret) {
		t.Fatalf("--no-truncate uncapped the toolloop INF redaction: raw secret leaked")
	}
	if !strings.Contains(infLine, "content_bytes") || !strings.Contains(infLine, "8192") {
		t.Errorf("INF line missing redacted content_bytes summary under --no-truncate: %s", infLine)
	}
}

// TestRedactArgs_NoTruncateCannotUncap is a direct unit test on the
// exported RedactArgs entry point: even with utils.SetDisableTruncation(true)
// the redaction fallback for unknown tools still bounds the result.
func TestRedactArgs_NoTruncateCannotUncap(t *testing.T) {
	utils.SetDisableTruncation(true)
	defer utils.SetDisableTruncation(false)

	args := map[string]any{
		"blob": strings.Repeat("X", 8192),
	}
	got := RedactArgs("unknown_tool_for_truncation_check", args)
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string redaction for unknown tool, got %T", got)
	}
	if len([]rune(s)) > 200+32 {
		t.Errorf("redaction unbounded under --no-truncate: %d runes", len([]rune(s)))
	}
	if !strings.Contains(s, "more)") {
		t.Errorf("expected truncation suffix '...(N more)', got %q", s[:min(120, len(s))])
	}
}
