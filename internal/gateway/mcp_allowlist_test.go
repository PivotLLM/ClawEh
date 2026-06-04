package gateway

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// TestMCPHostAllowlist verifies the MCP host allowlist resolution: an explicit
// list wins; otherwise it defaults to the DefaultEnabled tool set (so marking a
// tool DefaultEnabled exposes it over MCP without a separate hand-maintained list).
func TestMCPHostAllowlist(t *testing.T) {
	explicit := &config.Config{}
	explicit.MCPHost.Tools = []string{"files_read", "web_search"}
	got := mcpHostAllowlist(explicit)
	if len(got) != 2 || got[0] != "files_read" || got[1] != "web_search" {
		t.Fatalf("explicit MCPHost.Tools not honored: %v", got)
	}

	empty := &config.Config{}
	empty.MCPHost.Tools = nil
	got = mcpHostAllowlist(empty)
	has := func(n string) bool {
		for _, x := range got {
			if x == n {
				return true
			}
		}
		return false
	}
	if len(got) == 0 || !has("find_tools_regex") {
		t.Fatalf("empty MCPHost.Tools should default to the DefaultEnabled set, got %v", got)
	}
}
