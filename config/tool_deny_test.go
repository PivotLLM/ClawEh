package config

import "testing"

// deny_tools is evaluated after the generic Tools allow list, under
// MatchToolPattern's rule, and deny wins.
func TestAgentConfig_IsToolAllowed_DenyTools(t *testing.T) {
	tests := []struct {
		name      string
		tools     []string
		deny      []string
		toolName  string
		wantAllow bool
	}{
		// Deny wins over allow.
		{"wildcard allow, exact deny", []string{"*"}, []string{"shell_exec"}, "shell_exec", false},
		{"wildcard allow, other tool still allowed", []string{"*"}, []string{"shell_exec"}, "file_read_lines", true},
		{"exact allow, exact deny", []string{"shell_exec"}, []string{"shell_exec"}, "shell_exec", false},
		{"prefix allow, exact deny", []string{"file_*"}, []string{"file_delete"}, "file_delete", false},
		{"prefix allow, sibling still allowed", []string{"file_*"}, []string{"file_delete"}, "file_write", true},
		{"prefix deny", []string{"*"}, []string{"file_*"}, "file_delete", false},
		{"star deny denies everything", []string{"*"}, []string{"*"}, "file_read_lines", false},

		// Same case-insensitivity as the allow list.
		{"deny entry uppercase", []string{"*"}, []string{"SHELL_EXEC"}, "shell_exec", false},
		{"tool name uppercase", []string{"*"}, []string{"shell_exec"}, "Shell_Exec", false},
		{"deny prefix uppercase", []string{"*"}, []string{"FILE_*"}, "file_delete", false},

		// Exact deny is exact: no implicit prefix for generic tools.
		{"exact deny does not match longer name", []string{"*"}, []string{"file"}, "file_delete", true},

		// Deny never grants.
		{"deny of an unallowed tool stays denied", []string{"file_read_lines"}, []string{"shell_exec"}, "shell_exec", false},
		{"unrelated deny leaves allow intact", []string{"file_read_lines"}, []string{"shell_exec"}, "file_read_lines", true},

		// nil / empty deny ⇒ unchanged behaviour.
		{"nil deny, wildcard allow", []string{"*"}, nil, "shell_exec", true},
		{"empty deny, wildcard allow", []string{"*"}, []string{}, "shell_exec", true},
		{"nil deny, empty allow", []string{}, nil, "shell_exec", false},

		// nil Tools falls back to the install defaults, still subject to deny.
		{"default tools minus deny", nil, []string{"shell_exec"}, "shell_exec", false},
		{"default tools, other tool allowed", nil, []string{"shell_exec"}, "file_read_lines", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := &AgentConfig{Tools: tc.tools, DenyTools: tc.deny}
			if got := a.IsToolAllowed(tc.toolName); got != tc.wantAllow {
				t.Errorf("IsToolAllowed(%q) with tools=%v deny=%v = %v, want %v",
					tc.toolName, tc.tools, tc.deny, got, tc.wantAllow)
			}
		})
	}
}

// deny_tools entries for MCP tools use MCPToolAllowed's rule: the mcp_ prefix
// is stripped from the tool name, underscore runs collapse, and an entry
// denies when it equals or is a prefix of <server>_<tool>.
func TestAgentConfig_MCPToolAllowed_DenyTools(t *testing.T) {
	tests := []struct {
		name      string
		mcpTools  []string
		deny      []string
		toolName  string
		wantAllow bool
	}{
		// Server-wide allow, one tool denied; siblings unaffected.
		{"server allow, exact tool deny", []string{"google"}, []string{"google_drive_file_share"}, "mcp_google_drive_file_share", false},
		{"server allow, sibling still allowed", []string{"google"}, []string{"google_drive_file_share"}, "mcp_google_drive_files_list", true},
		{"server allow, second deny entry", []string{"google"}, []string{"google_drive_file_share", "google_calendar_event_delete"}, "mcp_google_calendar_event_delete", false},

		// Group prefix deny.
		{"group prefix deny", []string{"google"}, []string{"google_calendar"}, "mcp_google_calendar_event_delete", false},
		{"group prefix deny leaves other groups", []string{"google"}, []string{"google_calendar"}, "mcp_google_gmail_message_get", true},

		// Whole-server deny under a broader allow.
		{"server deny under wide allow", []string{"fusion"}, []string{"fusion_trello"}, "mcp_fusion_trello_search", false},
		{"server deny leaves other server", []string{"fusion"}, []string{"fusion_trello"}, "mcp_fusion_wxca_city_get", true},

		// Case-insensitive both ways.
		{"deny entry uppercase", []string{"google"}, []string{"GOOGLE_DRIVE_FILE_SHARE"}, "mcp_google_drive_file_share", false},
		{"tool name uppercase", []string{"google"}, []string{"google_drive_file_share"}, "mcp_Google_Drive_File_Share", false},

		// Underscore runs collapse on both sides.
		{"doubled name collapses", []string{"google"}, []string{"google_drive_file_share"}, "mcp_google__drive_file_share", false},
		{"doubled entry collapses", []string{"google"}, []string{"google__drive_file_share"}, "mcp_google_drive_file_share", false},
		{"doubled mcp prefix collapses", []string{"google"}, []string{"google_drive_file_share"}, "mcp__google_drive_file_share", false},

		// Blank deny entries are ignored (do not deny everything).
		{"blank deny ignored", []string{"google"}, []string{"  "}, "mcp_google_drive_files_list", true},

		// Deny never grants.
		{"deny of an unallowed tool stays denied", []string{"fusion"}, []string{"google_drive_file_share"}, "mcp_google_drive_file_share", false},

		// nil / empty deny ⇒ unchanged behaviour.
		{"nil deny", []string{"google"}, nil, "mcp_google_drive_file_share", true},
		{"empty deny", []string{"google"}, []string{}, "mcp_google_drive_file_share", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := &AgentConfig{MCPTools: tc.mcpTools, DenyTools: tc.deny}
			if got := a.MCPToolAllowed(tc.toolName); got != tc.wantAllow {
				t.Errorf("MCPToolAllowed(%q) with mcp_tools=%v deny=%v = %v, want %v",
					tc.toolName, tc.mcpTools, tc.deny, got, tc.wantAllow)
			}
		})
	}
}

// IsToolDenied is the deny-only check applied to suite tools (Fusion, Maestro,
// cogmem, discovery meta tools), which are exempt from the allow lists.
func TestAgentConfig_IsToolDenied(t *testing.T) {
	tests := []struct {
		name     string
		deny     []string
		toolName string
		want     bool
	}{
		{"nil deny", nil, "google_calendar_event_delete", false},
		{"empty deny", []string{}, "google_calendar_event_delete", false},
		{"exact suite tool", []string{"google_calendar_event_delete"}, "google_calendar_event_delete", true},
		{"exact is not a prefix for suite tools", []string{"google_calendar"}, "google_calendar_event_delete", false},
		{"prefix pattern", []string{"google_drive_*"}, "google_drive_file_share", true},
		{"prefix pattern leaves siblings", []string{"google_drive_*"}, "google_calendar_events_list", false},
		{"case-insensitive", []string{"GOOGLE_CALENDAR_EVENT_DELETE"}, "google_calendar_event_delete", true},
		{"maestro tool", []string{"maestro_task_dispatch"}, "maestro_task_dispatch", true},
		{"discovery meta tool", []string{"search_tools"}, "search_tools", true},
		{"mcp name uses the MCP rule", []string{"google_calendar"}, "mcp_google_calendar_event_delete", true},
		{"mcp name, no match", []string{"google_calendar"}, "mcp_google_gmail_message_get", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := &AgentConfig{Tools: []string{}, DenyTools: tc.deny}
			if got := a.IsToolDenied(tc.toolName); got != tc.want {
				t.Errorf("IsToolDenied(%q) with deny=%v = %v, want %v", tc.toolName, tc.deny, got, tc.want)
			}
		})
	}
	var nilAgent *AgentConfig
	if nilAgent.IsToolDenied("shell_exec") {
		t.Error("nil agent must deny nothing")
	}
}

// The registration gates (agent/loop_tools.go, agent/loop_mcp.go) call
// IsToolAllowed / MCPToolAllowed / IsToolDenied directly while the
// execution-time check (tools/registry.go) goes through IsToolAllowed for
// ordinary tools and IsToolDenied for suite-exempt ones; each pair must give
// the same answer for every name, deny included.
func TestDenyTools_RegistrationAndExecutionGatesAgree(t *testing.T) {
	a := &AgentConfig{
		Tools:     []string{"*"},
		MCPTools:  []string{"google", "fusion"},
		DenyTools: []string{"google_drive_file_share", "google_calendar", "fusion_trello_delete_card", "shell_exec", "maestro_task_*"},
	}
	// Suite tools: registration skips a tool when IsToolDenied; execution refuses
	// a SuiteExempt tool under the same call. Both sides use one function, so the
	// assertion here is that the deny reaches the motivating built-in names.
	suite := map[string]bool{
		"google_drive_file_share":      true,
		"google_drive_files_list":      false,
		"google_calendar_event_delete": false, // exact entry "google_calendar" is not a prefix for suite tools
		"maestro_task_dispatch":        true,
		"maestro_health":               false,
		"cogmem_memory_search":         false,
		"search_tools":                 false,
	}
	for n, wantDenied := range suite {
		if got := a.IsToolDenied(n); got != wantDenied {
			t.Errorf("suite %s: IsToolDenied = %v, want %v", n, got, wantDenied)
		}
	}
	names := []string{
		"mcp_google_drive_file_share",
		"mcp_google_drive_files_list",
		"mcp_google_calendar_event_delete",
		"mcp_google_gmail_message_get",
		"mcp_fusion_trello_delete_card",
		"mcp_fusion_trello_search",
		"mcp__google_drive_file_share",
		"mcp_other_tool",
	}
	for _, n := range names {
		if reg, exec := a.MCPToolAllowed(n), a.IsToolAllowed(n); reg != exec {
			t.Errorf("%s: registration gate = %v, execution gate = %v", n, reg, exec)
		}
	}
	// A generic deny entry does not leak into the MCP rule as a prefix grant or
	// denial of unrelated names, and the local tool it names is denied.
	if a.IsToolAllowed("shell_exec") {
		t.Error("shell_exec should be denied by deny_tools")
	}
	if !a.IsToolAllowed("file_read_lines") {
		t.Error("file_read_lines should stay allowed")
	}
}
