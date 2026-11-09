package shell

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/tools"
)

// Tests for guardCommand edge cases not covered by existing tests.

func TestExecTool_AllowPatterns_BlocksNonMatchingCommands(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.EnableDenyPatterns = false

	tool, err := NewExecToolWithConfig("", cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}

	// Set an allow-pattern that only permits 'echo'.
	if err := tool.SetAllowPatterns([]string{`^echo\b`}); err != nil {
		t.Fatalf("SetAllowPatterns() error = %v", err)
	}

	ctx := tools.WithToolContext(context.Background(), "cli", "direct")

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
