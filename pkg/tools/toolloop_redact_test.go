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

func (writeFileSilentTool) Name() string        { return "file_write" }
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
						Name: "file_write",
						Function: &providers.FunctionCall{
							Name:      "file_write",
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

// TestRunToolLoop_DispatchLogOmitsArgs verifies the toolloop.go per-tool-call
// dispatch log names the tool but carries NO arguments at all — tool args
// routinely contain memory/file content that must never reach the logs. There is
// also no longer a paired raw-args DBG line.
func TestRunToolLoop_DispatchLogOmitsArgs(t *testing.T) {
	secret := strings.Repeat("S", 10240)
	out := runToolLoopWriteFileOnce(t, secret)

	infLine, dbgLine := findToolloopDispatchLines(t, out)
	if infLine == "" {
		t.Fatalf("expected INF 'Tool call dispatched' line in toolloop output: %s", out)
	}
	if !strings.Contains(infLine, "file_write") {
		t.Errorf("dispatch line should name the tool: %s", infLine)
	}
	if strings.Contains(infLine, secret) || strings.Contains(infLine, `"args"`) {
		t.Fatalf("tool args leaked into dispatch INF log line: %s", infLine)
	}
	if dbgLine != "" {
		t.Fatalf("raw-args DBG line should no longer be emitted: %s", dbgLine)
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
