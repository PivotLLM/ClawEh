// ClawEh
// License: MIT

package agent

import "testing"

// TestDiscoveryHidesTool covers the in-loop pinning decision: a discovery-eligible
// tool is hidden only when discovery is active AND its namespace is not pinned via
// always_shown_namespaces (matched with MatchVisibility, so mcp_ is stripped and a
// namespace prefix matches).
func TestDiscoveryHidesTool(t *testing.T) {
	cases := []struct {
		name        string
		active      bool
		alwaysShown []string
		tool        string
		wantHidden  bool
	}{
		{"discovery off: never hidden", false, nil, "maestro_file_list", false},
		{"discovery off with pin: still visible", false, []string{"maestro"}, "maestro_file_list", false},
		{"discovery on, nothing pinned: hidden", true, nil, "maestro_file_list", true},
		{"discovery on, namespace pinned: visible", true, []string{"maestro"}, "maestro_file_list", false},
		{"discovery on, other namespace pinned: hidden", true, []string{"fusion"}, "maestro_file_list", true},
		{"discovery on, star pins everything: visible", true, []string{"*"}, "maestro_file_list", false},
		{"pin strips mcp_ prefix for upstream tools", true, []string{"trello"}, "mcp_trello_search", false},
		{"pin does not match a different mcp server", true, []string{"trello"}, "mcp_github_search", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := discoveryHidesTool(c.active, c.alwaysShown, c.tool); got != c.wantHidden {
				t.Fatalf("discoveryHidesTool(%v, %v, %q) = %v, want %v",
					c.active, c.alwaysShown, c.tool, got, c.wantHidden)
			}
		})
	}
}
