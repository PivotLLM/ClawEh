package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// readLogRecords decodes one JSON object per line out of buf.
func readLogRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("failed to decode log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func findRecord(records []map[string]any, message string) map[string]any {
	for _, rec := range records {
		if rec["message"] == message {
			return rec
		}
	}
	return nil
}

func TestEmitLLMFinishEvent_SuccessPath_UsesProviderStatus(t *testing.T) {
	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()

	resp := &providers.LLMResponse{
		Status: &providers.DispatchStatus{
			Success:             true,
			Model:               "claude-haiku-4-5-20251001",
			NumTurns:            1,
			InputTokens:         12,
			OutputTokens:        4,
			CacheReadTokens:     3,
			CacheCreationTokens: 100,
			StopReason:          "end_turn",
			CostUSD:             0.005,
			DurationMs:          150,
			BytesSent:           512,
			BytesReceived:       1024,
		},
	}

	emitLLMFinishEvent("Alice", 3, "anthropic_messages", "claude-haiku", time.Now().Add(-100*time.Millisecond), resp, nil)

	records := readLogRecords(t, buf)
	finish := findRecord(records, "LLM finish")
	if finish == nil {
		t.Fatalf("expected LLM finish record, got %v", records)
	}
	if finish["agent_id"] != "Alice" {
		t.Errorf("agent_id = %v, want Alice", finish["agent_id"])
	}
	if finish["provider"] != "anthropic_messages" {
		t.Errorf("provider = %v, want anthropic_messages", finish["provider"])
	}
	if finish["model"] != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %v, want claude-haiku-4-5-20251001", finish["model"])
	}
	if v, _ := finish["success"].(bool); !v {
		t.Errorf("success = %v, want true", finish["success"])
	}
	if v, _ := finish["input_tokens"].(float64); v != 12 {
		t.Errorf("input_tokens = %v, want 12", finish["input_tokens"])
	}
	if v, _ := finish["cache_creation_tokens"].(float64); v != 100 {
		t.Errorf("cache_creation_tokens = %v, want 100", finish["cache_creation_tokens"])
	}
	if v, _ := finish["bytes_sent"].(float64); v != 512 {
		t.Errorf("bytes_sent = %v, want 512", finish["bytes_sent"])
	}
	if v, _ := finish["bytes_received"].(float64); v != 1024 {
		t.Errorf("bytes_received = %v, want 1024", finish["bytes_received"])
	}
	if _, has := finish["error"]; has {
		t.Errorf("unexpected error field on success: %v", finish["error"])
	}
}

func TestEmitLLMFinishEvent_ErrorPathWithoutStatus_SynthesizesFallback(t *testing.T) {
	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()

	err := errors.New(strings.Repeat("x", 600))
	emitLLMFinishEvent("Bob", 1, "openai", "gpt-4o", time.Now().Add(-50*time.Millisecond), nil, err)

	records := readLogRecords(t, buf)
	finish := findRecord(records, "LLM finish")
	if finish == nil {
		t.Fatalf("expected LLM finish record, got %v", records)
	}
	if v, _ := finish["success"].(bool); v {
		t.Errorf("expected success=false, got %v", finish["success"])
	}
	if finish["model"] != "gpt-4o" {
		t.Errorf("model = %v, want gpt-4o (fallback to requested model)", finish["model"])
	}
	if finish["stop_reason"] != "error" {
		t.Errorf("stop_reason = %v, want error", finish["stop_reason"])
	}
	errStr, ok := finish["error"].(string)
	if !ok {
		t.Fatalf("error field missing or wrong type: %v", finish["error"])
	}
	if len(errStr) != 500 {
		t.Errorf("error len = %d, want 500 (truncated)", len(errStr))
	}
}

func TestEmitLLMFinishEvent_ErrorPathWithPartialStatus_BytesPreserved(t *testing.T) {
	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()

	resp := &providers.LLMResponse{
		Status: &providers.DispatchStatus{
			Success:    false,
			Model:      "claude-sonnet-4-5",
			StopReason: "error",
			BytesSent:  200,
		},
	}
	emitLLMFinishEvent("Alice", 2, "anthropic_messages", "claude-sonnet-4-5", time.Now(), resp, errors.New("network failed"))

	records := readLogRecords(t, buf)
	finish := findRecord(records, "LLM finish")
	if finish == nil {
		t.Fatalf("expected LLM finish record, got %v", records)
	}
	if v, _ := finish["bytes_sent"].(float64); v != 200 {
		t.Errorf("bytes_sent = %v, want 200 (best-effort byte counts preserved on error)", finish["bytes_sent"])
	}
	if v, _ := finish["bytes_received"].(float64); v != 0 {
		t.Errorf("bytes_received = %v, want 0", finish["bytes_received"])
	}
}
