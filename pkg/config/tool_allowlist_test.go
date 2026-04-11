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
