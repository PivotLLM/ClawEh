package config

import "testing"

func TestAgentConfig_IsToolAllowed(t *testing.T) {
	tests := []struct {
		name      string
		tools     []string
		toolName  string
		wantAllow bool
	}{
		// Nil / empty list denies everything
		{
			name:      "nil agent denies all",
			tools:     nil,
			toolName:  "read_file",
			wantAllow: false,
		},
		{
			name:      "empty tools list denies all",
			tools:     []string{},
			toolName:  "read_file",
			wantAllow: false,
		},

		// Wildcard "*" allows everything
		{
			name:      "star allows any tool",
			tools:     []string{"*"},
			toolName:  "read_file",
			wantAllow: true,
		},
		{
			name:      "star allows autotools",
			tools:     []string{"*"},
			toolName:  "autotools",
			wantAllow: true,
		},
		{
			name:      "star allows send",
			tools:     []string{"*"},
			toolName:  "send",
			wantAllow: true,
		},

		// Prefix wildcard "auto*" — case-insensitive match
		{
			name:      "auto* matches autotools (exact case)",
			tools:     []string{"auto*"},
			toolName:  "autotools",
			wantAllow: true,
		},
		{
			name:      "auto* matches AutoTools (case-insensitive)",
			tools:     []string{"auto*"},
			toolName:  "AutoTools",
			wantAllow: true,
		},
		{
			name:      "AUTO* matches autotools (uppercase prefix, case-insensitive)",
			tools:     []string{"AUTO*"},
			toolName:  "autotools",
			wantAllow: true,
		},
		{
			name:      "auto* does not match send",
			tools:     []string{"auto*"},
			toolName:  "send",
			wantAllow: false,
		},
		{
			name:      "auto* does not match empty string",
			tools:     []string{"auto*"},
			toolName:  "",
			wantAllow: false,
		},

		// Exact match
		{
			name:      "exact match allowed",
			tools:     []string{"read_file"},
			toolName:  "read_file",
			wantAllow: true,
		},
		{
			name:      "exact match is case-insensitive (match different case)",
			tools:     []string{"read_file"},
			toolName:  "Read_File",
			wantAllow: true,
		},
		{
			name:      "exact match does not match substring",
			tools:     []string{"read_file"},
			toolName:  "read_file_extra",
			wantAllow: false,
		},

		// No match
		{
			name:      "no matching entry returns false",
			tools:     []string{"write_file", "exec"},
			toolName:  "read_file",
			wantAllow: false,
		},

		// Multiple entries — first match wins
		{
			name:      "multiple entries, one matches",
			tools:     []string{"write_file", "read_file", "exec"},
			toolName:  "read_file",
			wantAllow: true,
		},
		{
			name:      "prefix entry among multiple entries matches",
			tools:     []string{"exec", "auto*"},
			toolName:  "autotools",
			wantAllow: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var a *AgentConfig
			if tc.tools != nil {
				a = &AgentConfig{Tools: tc.tools}
			}
			got := a.IsToolAllowed(tc.toolName)
			if got != tc.wantAllow {
				t.Errorf("IsToolAllowed(%q) with tools=%v = %v, want %v",
					tc.toolName, tc.tools, got, tc.wantAllow)
			}
		})
	}
}

// IsToolAllowed must route mcp_* names through mcp_tools (never the generic
// Tools list), so the registration gate and the execution-time check agree.
func TestIsToolAllowed_RoutesMCPThroughMCPTools(t *testing.T) {
	mcpName := "mcp_fusion_wxca_city_get"

	// mcp_tools grants it even when Tools is empty.
	a := &AgentConfig{Tools: []string{}, MCPTools: []string{"fusion"}}
	if !a.IsToolAllowed(mcpName) {
		t.Errorf("mcp_tools=[fusion] should permit %s", mcpName)
	}

	// A generic "*" must NOT grant mcp tools — only mcp_tools does.
	a = &AgentConfig{Tools: []string{"*"}}
	if a.IsToolAllowed(mcpName) {
		t.Errorf(`Tools=["*"] must not grant %s (mcp_tools is empty)`, mcpName)
	}

	// A stale mcp_* entry in the generic list must NOT grant it either.
	a = &AgentConfig{Tools: []string{"mcp_fusion_*"}}
	if a.IsToolAllowed(mcpName) {
		t.Errorf("stale Tools=[mcp_fusion_*] must not grant %s", mcpName)
	}

	// Non-mcp tools are unaffected by mcp_tools.
	a = &AgentConfig{Tools: []string{"file_read_lines"}, MCPTools: []string{"fusion"}}
	if !a.IsToolAllowed("file_read_lines") {
		t.Error("non-mcp tool should still be governed by the Tools list")
	}
	if a.IsToolAllowed("mcp_other_tool") {
		t.Error("mcp_other_tool not matched by mcp_tools=[fusion] should be denied")
	}
}

// MatchVisibility is the coarse per-endpoint MCP-host filter: prefix/equality for
// local tools, mcp_-stripped prefix for upstream, "*" = all, empty = nothing.
func TestMatchVisibility(t *testing.T) {
	cases := []struct {
		name      string
		patterns  []string
		tool      string
		wantMatch bool
	}{
		{"empty exposes nothing", nil, "file_read_lines", false},
		{"star exposes all local", []string{"*"}, "file_read_lines", true},
		{"star exposes all upstream", []string{"*"}, "mcp_fusion_wxca_city_get", true},

		{"local exact", []string{"session_info"}, "session_info", true},
		{"local prefix", []string{"file"}, "file_read_lines", true},
		{"local prefix underscore", []string{"file_"}, "file_write", true},
		{"local non-match", []string{"file"}, "session_info", false},

		{"upstream server prefix, no mcp_", []string{"fusion"}, "mcp_fusion_wxca_city_get", true},
		{"upstream group prefix", []string{"fusion_wxca"}, "mcp_fusion_wxca_city_get", true},
		{"upstream group excludes other group", []string{"fusion_wxca"}, "mcp_fusion_trello_search", false},
		{"upstream collapses doubled underscore", []string{"fusion_tool"}, "mcp_fusion__tool", true},

		{"trailing glob tolerated", []string{"fusion_*"}, "mcp_fusion_wxca_city_get", true},
		{"local trailing glob tolerated", []string{"read_*"}, "read_file", true},

		{"case-insensitive", []string{"FUSION"}, "mcp_Fusion_Trello_Search", true},
		{"blank entry ignored", []string{"  "}, "file_read_lines", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MatchVisibility(c.patterns, c.tool); got != c.wantMatch {
				t.Errorf("MatchVisibility(%v, %q) = %v, want %v", c.patterns, c.tool, got, c.wantMatch)
			}
		})
	}
}

func TestAgentConfig_MCPToolAllowed(t *testing.T) {
	tests := []struct {
		name      string
		mcpTools  []string
		toolName  string // published mcp_<server>_<tool>
		wantAllow bool
	}{
		// Empty / nil admits nothing.
		{"nil list admits nothing", nil, "mcp_fusion_wxca_city_get", false},
		{"empty list admits nothing", []string{}, "mcp_fusion_wxca_city_get", false},

		// Server prefix admits the whole server.
		{"server name admits all its tools", []string{"fusion"}, "mcp_fusion_wxca_city_get", true},
		{"server name admits another tool", []string{"fusion"}, "mcp_fusion_trello_search", true},
		{"server_ guard still admits", []string{"fusion_"}, "mcp_fusion_trello_search", true},

		// Tool-group prefix scopes within a server.
		{"group prefix admits its tools", []string{"fusion_wxca"}, "mcp_fusion_wxca_city_get", true},
		{"group prefix excludes other groups", []string{"fusion_wxca"}, "mcp_fusion_trello_search", false},

		// Exact full name.
		{"exact full name", []string{"fusion_wxca_city_get"}, "mcp_fusion_wxca_city_get", true},

		// Case-insensitive both ways.
		{"entry uppercase matches", []string{"FUSION"}, "mcp_fusion_trello_search", true},
		{"name uppercase matches", []string{"fusion"}, "mcp_Fusion_Trello_Search", true},

		// A prefix of the server name also matches (fusion matches fusionhub).
		{"shorter prefix spans servers", []string{"fusion"}, "mcp_fusionhub_x", true},

		// Wrong server is denied.
		{"different server denied", []string{"fusion"}, "mcp_other_tool", false},

		// Blank entries are ignored (don't admit everything).
		{"blank entry ignored", []string{"  "}, "mcp_fusion_trello_search", false},

		// Multiple entries, any can match.
		{"one of several matches", []string{"weather", "fusion_trello"}, "mcp_fusion_trello_search", true},

		// Underscore runs collapse for comparison: a clean entry matches a doubled name.
		{"entry matches doubled tool name", []string{"fusion_tool"}, "mcp_fusion__tool", true},
		{"server prefix matches doubled name", []string{"fusion"}, "mcp_fusion__tool", true},
		{"doubled mcp prefix collapses", []string{"fusion"}, "mcp__fusion_tool", true},
		{"doubled entry matches clean name", []string{"fusion__tool"}, "mcp_fusion_tool", true},
		{"triple underscores collapse", []string{"fusion_wxca"}, "mcp_fusion___wxca_city_get", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var a *AgentConfig
			if tc.mcpTools != nil {
				a = &AgentConfig{MCPTools: tc.mcpTools}
			}
			if got := a.MCPToolAllowed(tc.toolName); got != tc.wantAllow {
				t.Errorf("MCPToolAllowed(%q) with mcp_tools=%v = %v, want %v",
					tc.toolName, tc.mcpTools, got, tc.wantAllow)
			}
		})
	}
}
