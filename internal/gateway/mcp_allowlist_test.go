package gateway

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// TestMCPHostAllowlist verifies the MCP host allowlist resolution: an explicit
// list wins; otherwise it defaults to "*" — the full union of every agent's
// allowed tools, for internal/external parity (per-agent gating happens at
// execution time via the session_token, not at catalogue time).
func TestMCPHostAllowlist(t *testing.T) {
	explicit := &config.Config{}
	explicit.MCPHost.Tools = []string{"file_read", "web_search"}
	got := mcpHostAllowlist(explicit)
	if len(got) != 2 || got[0] != "file_read" || got[1] != "web_search" {
		t.Fatalf("explicit MCPHost.Tools not honored: %v", got)
	}

	empty := &config.Config{}
	empty.MCPHost.Tools = nil
	got = mcpHostAllowlist(empty)
	if len(got) != 1 || got[0] != "*" {
		t.Fatalf("empty MCPHost.Tools should default to [\"*\"] (full parity), got %v", got)
	}
}
