package routing

import "testing"

func TestBuildAgentMainSessionKey(t *testing.T) {
	got := BuildAgentMainSessionKey("sales")
	want := "agent:sales:main"
	if got != want {
		t.Errorf("BuildAgentMainSessionKey('sales') = %q, want %q", got, want)
	}
}

func TestBuildAgentMainSessionKey_Normalizes(t *testing.T) {
	got := BuildAgentMainSessionKey("Sales Bot")
	want := "agent:sales-bot:main"
	if got != want {
		t.Errorf("BuildAgentMainSessionKey('Sales Bot') = %q, want %q", got, want)
	}
}

func TestParseAgentSessionKey_Valid(t *testing.T) {
	parsed := ParseAgentSessionKey("agent:sales:telegram:direct:user123")
	if parsed == nil {
		t.Fatal("expected non-nil result")
	}
	if parsed.AgentID != "sales" {
		t.Errorf("AgentID = %q, want 'sales'", parsed.AgentID)
	}
	if parsed.Rest != "telegram:direct:user123" {
		t.Errorf("Rest = %q, want 'telegram:direct:user123'", parsed.Rest)
	}
}

func TestParseAgentSessionKey_Invalid(t *testing.T) {
	tests := []string{
		"",
		"foo:bar",
		"notprefix:sales:main",
		"agent::main",
		"agent:sales:",
	}
	for _, input := range tests {
		if got := ParseAgentSessionKey(input); got != nil {
			t.Errorf("ParseAgentSessionKey(%q) = %+v, want nil", input, got)
		}
	}
}

func TestIsSubagentSessionKey(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"subagent:task-1", true},
		{"agent:main:subagent:task-1", true},
		{"agent:main:main", false},
		{"agent:main:telegram:direct:user123", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsSubagentSessionKey(tt.input); got != tt.want {
			t.Errorf("IsSubagentSessionKey(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
