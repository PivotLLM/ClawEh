package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// stubProvider returns a canned response or error each Chat call.
type stubProvider struct {
	resp *providers.LLMResponse
	err  error
}

func (s *stubProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	return s.resp, s.err
}

func (s *stubProvider) GetDefaultModel() string { return "stub" }

func collectLLMEvents(t *testing.T, buf *bytes.Buffer) (dispatches, finishes int, finish map[string]any) {
	t.Helper()
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log line: %v", err)
		}
		switch rec["message"] {
		case "LLM dispatch":
			dispatches++
		case "LLM finish":
			finishes++
			finish = rec
		}
	}
	return
}

func TestRunToolLoop_EmitsDispatchAndFinishOnSuccess(t *testing.T) {
	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()

	resp := &providers.LLMResponse{
		Content: "hi from Alice",
		Status: &providers.DispatchStatus{
			Success:       true,
			Model:         "gpt-4o-2024-11-20",
			InputTokens:   3,
			OutputTokens:  2,
			StopReason:    "stop",
			BytesSent:     128,
			BytesReceived: 256,
		},
	}
	cfg := ToolLoopConfig{
		Provider:      &stubProvider{resp: resp},
		Model:         "gpt-4o",
		MaxIterations: 1,
	}
	_, err := RunToolLoop(context.Background(), cfg, []providers.Message{{Role: "user", Content: "hi"}}, "ch", "chat")
	if err != nil {
		t.Fatalf("RunToolLoop() err = %v", err)
	}

	dispatches, finishes, finish := collectLLMEvents(t, buf)
	if dispatches != 1 {
		t.Errorf("dispatch count = %d, want 1", dispatches)
	}
	if finishes != 1 {
		t.Errorf("finish count = %d, want 1", finishes)
	}
	if finish == nil {
		t.Fatal("missing finish record")
	}
	if v, _ := finish["success"].(bool); !v {
		t.Errorf("success = %v, want true", finish["success"])
	}
	if v, _ := finish["bytes_sent"].(float64); v != 128 {
		t.Errorf("bytes_sent = %v, want 128", finish["bytes_sent"])
	}
	if v, _ := finish["bytes_received"].(float64); v != 256 {
		t.Errorf("bytes_received = %v, want 256", finish["bytes_received"])
	}
	if finish["model"] != "gpt-4o-2024-11-20" {
		t.Errorf("model = %v, want gpt-4o-2024-11-20", finish["model"])
	}
}

func TestRunToolLoop_EmitsDispatchAndFinishOnError(t *testing.T) {
	buf := &bytes.Buffer{}
	restore := logger.RedirectForTest(buf)
	defer restore()

	cfg := ToolLoopConfig{
		Provider:      &stubProvider{err: errors.New("network failed for Bob")},
		Model:         "gpt-4o",
		MaxIterations: 1,
	}
	_, err := RunToolLoop(context.Background(), cfg, []providers.Message{{Role: "user", Content: "hi"}}, "ch", "chat")
	if err == nil {
		t.Fatal("expected error")
	}

	dispatches, finishes, finish := collectLLMEvents(t, buf)
	if dispatches != 1 || finishes != 1 {
		t.Errorf("dispatch=%d finish=%d, want 1/1", dispatches, finishes)
	}
	if v, _ := finish["success"].(bool); v {
		t.Errorf("success = %v, want false", finish["success"])
	}
	if finish["model"] != "gpt-4o" {
		t.Errorf("model = %v, want gpt-4o (fallback to requested)", finish["model"])
	}
	if _, has := finish["error"]; !has {
		t.Error("error field missing on failure")
	}
}
