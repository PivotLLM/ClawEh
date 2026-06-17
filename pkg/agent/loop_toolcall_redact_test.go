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

	if _, _, _, _, _, err := al.runLLMIteration(context.Background(), agentInstance, messages, opts, cm); err != nil {
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

// TestRunLLMIteration_DispatchLogOmitsArgs verifies the agent loop's per-tool-call
// dispatch log names the tool and carries iteration context, but NO arguments at
// all — tool args routinely contain memory/file content that must never reach the
// logs — and that there is no longer a paired raw-args DBG line carrying the
// secret.
func TestRunLLMIteration_DispatchLogOmitsArgs(t *testing.T) {
	secret := strings.Repeat("S", 10240)
	out := runWriteFileToolCallOnce(t, secret)

	infLine, dbgLine := findToolDispatchLines(t, out)
	if infLine == "" {
		t.Fatalf("expected INF 'Tool call dispatched' line in output: %s", out)
	}
	if !strings.Contains(infLine, "file_write") {
		t.Errorf("dispatch line should name the tool: %s", infLine)
	}
	// iteration context distinguishes this site from the registry's execution log.
	if !strings.Contains(infLine, `"iteration"`) {
		t.Errorf("dispatch line missing iteration context: %s", infLine)
	}
	if strings.Contains(infLine, `"args"`) || strings.Contains(infLine, secret) {
		t.Fatalf("tool args leaked into dispatch INF log line: %s", infLine)
	}
	if dbgLine != "" {
		t.Fatalf("raw-args DBG line should no longer be emitted: %s", dbgLine)
	}
	// The secret must not appear anywhere in the captured logs.
	if strings.Contains(out, secret) {
		t.Fatalf("raw tool content leaked into logs")
	}
}
