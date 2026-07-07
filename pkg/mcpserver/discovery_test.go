// ClawEh
// License: MIT

package mcpserver

import "testing"

func TestDiscoveryConfig_BaseVisible(t *testing.T) {
	off := discoveryConfig{enabled: false}
	// cogmem is NOT in the list — it must still be shown, by rule.
	on := discoveryConfig{enabled: true, alwaysShown: []string{"file"}}

	cases := []struct {
		name    string
		disc    discoveryConfig
		tool    string
		visible bool
	}{
		{"off shows everything", off, "trello_search", true},
		{"off shows mcp tool", off, "browser_browser_navigate", true},
		{"on hides non-listed namespace", on, "trello_search", false},
		{"on hides mcp namespace", on, "browser_browser_navigate", false},
		{"on shows cogmem by rule (not in list)", on, "cogmem_memory_create", true},
		{"on shows always-shown file", on, "file_read_lines", true},
		{"on always shows search_tools meta", on, "search_tools", true},
		{"on always shows get_tool_details meta", on, "get_tool_details", true},
		{"on: namespace match is case-insensitive", on, "Cogmem_x", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.disc.baseVisible(c.tool); got != c.visible {
				t.Errorf("baseVisible(%q) = %v, want %v", c.tool, got, c.visible)
			}
		})
	}
}
