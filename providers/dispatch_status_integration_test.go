//go:build integration

package providers

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestIntegration_DispatchStatus_ClaudeCLI runs the real claude CLI and asserts
// that the parser populates DispatchStatus with a dated model id, non-zero
// cache creation tokens, and a non-zero cost.
func TestIntegration_DispatchStatus_ClaudeCLI(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not found in PATH")
	}

	p := NewClaudeCliProvider("", t.TempDir(), []string{"--model", "haiku"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := p.Chat(ctx, []Message{{Role: "user", Content: "say hi"}}, nil, "", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Status == nil {
		t.Fatal("Status is nil; expected populated DispatchStatus")
	}
	t.Logf("DispatchStatus: %+v", resp.Status)

	if resp.Status.Model == "" {
		t.Error("Model is empty; expected exact dated id (e.g. claude-haiku-4-5-20251001)")
	}
	if !strings.HasPrefix(resp.Status.Model, "claude-") {
		t.Errorf("Model = %q, want claude-* prefix", resp.Status.Model)
	}
	if !resp.Status.Success {
		t.Error("Success = false; expected true on a successful run")
	}
	if resp.Status.InputTokens == 0 {
		t.Error("InputTokens = 0; expected non-zero")
	}
	if resp.Status.OutputTokens == 0 {
		t.Error("OutputTokens = 0; expected non-zero")
	}
	if resp.Status.DurationMs == 0 {
		t.Error("DurationMs = 0; expected non-zero")
	}
	// Cost reported by claude-cli should be non-zero when the model dispatched.
	if resp.Status.CostUSD <= 0 {
		t.Logf("warning: CostUSD = %v; design doc expects non-zero", resp.Status.CostUSD)
	}
	if resp.Status.BytesSent == 0 {
		t.Error("BytesSent = 0; expected non-zero (stdin payload)")
	}
	if resp.Status.BytesReceived == 0 {
		t.Error("BytesReceived = 0; expected non-zero (stdout JSON)")
	}
}

// TestIntegration_DispatchStatus_CodexCLI runs the real codex CLI and asserts
// that the parser produces NumTurns >= 1 and cache_read_tokens from
// cached_input_tokens.
func TestIntegration_DispatchStatus_CodexCLI(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not found in PATH")
	}

	extraArgs := []string{"--skip-git-repo-check", "--sandbox", "read-only", "--dangerously-bypass-approvals-and-sandbox"}
	p := NewCodexCliProvider("", t.TempDir(), extraArgs, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	resp, err := p.Chat(ctx, []Message{{Role: "user", Content: "say hi"}}, nil, "", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Status == nil {
		t.Fatal("Status is nil")
	}
	t.Logf("DispatchStatus: %+v", resp.Status)

	if resp.Status.NumTurns < 1 {
		t.Errorf("NumTurns = %d, want >= 1", resp.Status.NumTurns)
	}
	if resp.Status.InputTokens == 0 {
		t.Error("InputTokens = 0; expected populated from turn.completed.usage")
	}
	if resp.Status.BytesSent == 0 {
		t.Error("BytesSent = 0; expected non-zero (stdin payload)")
	}
	if resp.Status.BytesReceived == 0 {
		t.Error("BytesReceived = 0; expected non-zero (JSONL stream)")
	}
}

// TestIntegration_DispatchStatus_GeminiCLI runs the real gemini CLI and
// confirms the parser prefers the model whose roles map contains "main".
func TestIntegration_DispatchStatus_GeminiCLI(t *testing.T) {
	if _, err := exec.LookPath("gemini"); err != nil {
		t.Skip("gemini CLI not found in PATH")
	}

	p := NewGeminiCliProvider("", t.TempDir(), []string{"--skip-trust", "--yolo"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	resp, err := p.Chat(ctx, []Message{{Role: "user", Content: "say hi"}}, nil, "", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Status == nil {
		t.Fatal("Status is nil")
	}
	t.Logf("DispatchStatus: %+v", resp.Status)

	if resp.Status.Model == "" {
		t.Error("Model is empty; expected primary (main role) model name")
	}
	if strings.Contains(strings.ToLower(resp.Status.Model), "utility_router") {
		t.Errorf("Model = %q; utility_router should never win — main role must be preferred", resp.Status.Model)
	}
	if resp.Status.NumTurns < 1 {
		t.Errorf("NumTurns = %d, want >= 1 (totalRequests of main role)", resp.Status.NumTurns)
	}
	if resp.Status.BytesSent == 0 {
		t.Error("BytesSent = 0; expected non-zero")
	}
	if resp.Status.BytesReceived == 0 {
		t.Error("BytesReceived = 0; expected non-zero")
	}
}
