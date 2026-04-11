package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// Tests for guardCommand edge cases not covered by existing tests.

func TestExecTool_AllowPatterns_BlocksNonMatchingCommands(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.EnableDenyPatterns = false

	tool, err := NewExecToolWithConfig("", true, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}

	// Set an allow-pattern that only permits 'echo'.
	if err := tool.SetAllowPatterns([]string{`^echo\b`}); err != nil {
		t.Fatalf("SetAllowPatterns() error = %v", err)
	}

	ctx := WithToolContext(context.Background(), "cli", "direct")

	// 'echo hello' should be allowed.
	result := tool.Execute(ctx, map[string]any{"command": "echo hello"})
	if result.IsError && strings.Contains(result.ForLLM, "not in allowlist") {
		t.Errorf("echo should be allowed by allowlist: %s", result.ForLLM)
	}

	// 'ls -la' should be blocked since it doesn't match the allow pattern.
	result = tool.Execute(ctx, map[string]any{"command": "ls -la"})
	if !result.IsError {
		t.Error("ls should be blocked by allowlist")
	}
	if !strings.Contains(result.ForLLM, "not in allowlist") {
		t.Errorf("error = %q, want 'not in allowlist'", result.ForLLM)
	}
}

func TestExecTool_PathTraversal_Blocked(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.EnableDenyPatterns = false

	tool, err := NewExecToolWithConfig(dir, true, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}
	tool.SetRestrictToWorkspace(true)

	ctx := WithToolContext(context.Background(), "cli", "direct")

	result := tool.Execute(ctx, map[string]any{
		"command": "cat ../../../etc/passwd",
	})
	if !result.IsError {
		t.Error("path traversal '../' should be blocked")
	}
}

func TestExecTool_Execute_AllowRemote(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.AllowRemote = true

	tool, err := NewExecToolWithConfig("", false, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}

	// With allowRemote=true, should work from any channel.
	result := tool.Execute(context.Background(), map[string]any{
		"command": "echo allow-remote-test",
	})
	// May succeed or fail depending on command availability; just no panic.
	_ = result
}
