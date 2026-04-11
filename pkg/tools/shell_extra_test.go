package tools

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// TestExecTool_SetAllowPatterns_ValidPatterns verifies SetAllowPatterns accepts valid patterns.
func TestExecTool_SetAllowPatterns_ValidPatterns(t *testing.T) {
	cfg := config.DefaultConfig()
	tool, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}

	err = tool.SetAllowPatterns([]string{`^echo\s`, `^ls\b`})
	if err != nil {
		t.Errorf("SetAllowPatterns() error = %v, want nil", err)
	}
}

// TestExecTool_SetAllowPatterns_InvalidPattern verifies SetAllowPatterns rejects invalid regex.
func TestExecTool_SetAllowPatterns_InvalidPattern(t *testing.T) {
	cfg := config.DefaultConfig()
	tool, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}

	err = tool.SetAllowPatterns([]string{`[invalid`})
	if err == nil {
		t.Error("SetAllowPatterns() expected error for invalid pattern, got nil")
	}
}

// TestExecTool_SetAllowPatterns_EmptySlice verifies SetAllowPatterns with empty slice.
func TestExecTool_SetAllowPatterns_EmptySlice(t *testing.T) {
	cfg := config.DefaultConfig()
	tool, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}

	err = tool.SetAllowPatterns([]string{})
	if err != nil {
		t.Errorf("SetAllowPatterns([]) error = %v, want nil", err)
	}
}

// TestNewExecToolWithConfig_DenyPatternsDisabled verifies deny patterns disabled path.
func TestNewExecToolWithConfig_DenyPatternsDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.EnableDenyPatterns = false

	tool, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
}

// TestNewExecToolWithConfig_CustomDenyPatterns verifies custom deny patterns are compiled.
func TestNewExecToolWithConfig_CustomDenyPatterns(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.EnableDenyPatterns = true
	cfg.Tools.Exec.CustomDenyPatterns = []string{`^rm\s+-rf\s+/`}

	tool, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
}

// TestNewExecToolWithConfig_InvalidCustomDenyPattern verifies invalid deny pattern returns error.
func TestNewExecToolWithConfig_InvalidCustomDenyPattern(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.EnableDenyPatterns = true
	cfg.Tools.Exec.CustomDenyPatterns = []string{`[invalid`}

	_, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
	if err == nil {
		t.Error("NewExecToolWithConfig() expected error for invalid deny pattern")
	}
}

// TestNewExecToolWithConfig_CustomAllowPatterns verifies custom allow patterns are compiled.
func TestNewExecToolWithConfig_CustomAllowPatterns(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.CustomAllowPatterns = []string{`^echo\b`}

	tool, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
}

// TestNewExecToolWithConfig_InvalidCustomAllowPattern verifies invalid allow pattern returns error.
func TestNewExecToolWithConfig_InvalidCustomAllowPattern(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.CustomAllowPatterns = []string{`[invalid`}

	_, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
	if err == nil {
		t.Error("NewExecToolWithConfig() expected error for invalid allow pattern")
	}
}

// TestNewExecToolWithConfig_CustomTimeout verifies custom timeout is applied.
func TestNewExecToolWithConfig_CustomTimeout(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.TimeoutSeconds = 120

	tool, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
}

// TestNewExecToolWithConfig_NilConfig verifies nil config uses defaults.
func TestNewExecToolWithConfig_NilConfig(t *testing.T) {
	tool, err := NewExecToolWithConfig(t.TempDir(), false, nil)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig(nil) error = %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool with nil config")
	}
}
