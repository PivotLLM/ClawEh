package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// runReasoningIterationOnce drives one runLLMIteration with a direct-answer
// response that carries reasoning text, then returns the captured log buffer.
func runReasoningIterationOnce(t *testing.T, reasoning string) string {
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
	agentInstance.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			{Content: "the answer", Reasoning: reasoning},
		},
		errors: []error{nil},
	}

	messages := []providers.Message{{Role: "user", Content: "go"}}
	opts := processOptions{
		SessionKey:   "reasoning-loop",
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

// findLLMResponseLine pulls the DBG "LLM response" line out of a captured
// zerolog stream.
func findLLMResponseLine(out string) string {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if msg, _ := ev["message"].(string); msg == "LLM response" {
			return line
		}
	}
	return ""
}

// TestRunLLMIteration_LLMResponse_SuppressesReasoningByDefault verifies that
// the DBG "LLM response" line at loop.go logs only reasoning_chars and not the
// reasoning text itself when log_message_content is off (the default).
//
// Mutation evidence: drop the GetLogMessageContent() gate and always set
// respFields["reasoning"] and this test fails on the 'leaked' assertion.
func TestRunLLMIteration_LLMResponse_SuppressesReasoningByDefault(t *testing.T) {
	logger.SetLogMessageContent(false)
	secret := "step one is to consult the secret weather oracle"
	out := runReasoningIterationOnce(t, secret)

	line := findLLMResponseLine(out)
	if line == "" {
		t.Fatalf("expected DBG 'LLM response' line in output: %s", out)
	}
	if strings.Contains(line, secret) {
		t.Fatalf("reasoning text leaked into 'LLM response' log line: %s", line)
	}
	if !strings.Contains(line, "reasoning_chars") {
		t.Errorf("'LLM response' line missing reasoning_chars summary: %s", line)
	}
}

// TestRunLLMIteration_LLMResponse_IncludesReasoningWhenEnabled verifies that
// enabling log_message_content restores the full reasoning text for operators
// who opt in.
func TestRunLLMIteration_LLMResponse_IncludesReasoningWhenEnabled(t *testing.T) {
	logger.SetLogMessageContent(true)
	defer logger.SetLogMessageContent(false)

	secret := "step one is to consult the secret weather oracle"
	out := runReasoningIterationOnce(t, secret)

	line := findLLMResponseLine(out)
	if line == "" {
		t.Fatalf("expected DBG 'LLM response' line in output: %s", out)
	}
	if !strings.Contains(line, secret) {
		t.Errorf("reasoning text should appear when log_message_content is on: %s", line)
	}
}
