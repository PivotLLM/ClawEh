package gateway

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// TestMCPVisibilityList verifies per-endpoint visibility resolution: an explicit
// list wins; an empty list defaults to "*" — the full union of every agent's
// allowed tools, for internal/external parity (per-agent gating happens at
// execution time via the session_token, not at catalogue time).
func TestMCPVisibilityList(t *testing.T) {
	explicit := &config.Config{}
	explicit.MCPHost.InternalTools = []string{"file_read_bytes", "web_search"}
	got := mcpVisibilityList(explicit.MCPHost.InternalTools)
	if len(got) != 2 || got[0] != "file_read_bytes" || got[1] != "web_search" {
		t.Fatalf("explicit InternalTools not honored: %v", got)
	}

	// Empty external list → default "*".
	got = mcpVisibilityList((&config.Config{}).MCPHost.ExternalTools)
	if len(got) != 1 || got[0] != "*" {
		t.Fatalf("empty list should default to [\"*\"] (full parity), got %v", got)
	}
}
