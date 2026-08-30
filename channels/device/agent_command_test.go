package device

import (
	"strings"
	"testing"
)

func TestParseSlashCommand(t *testing.T) {
	tests := []struct {
		in      string
		wantCmd string
		wantArg string
		wantOK  bool
	}{
		{"/agent", "agent", "", true},
		{"  /agent  ", "agent", "", true},
		{"/agent bob", "agent", "bob", true},
		{"/Agent Bob", "agent", "Bob", true},
		{"/agent  my bot ", "agent", "my bot", true},
		{"/agent\tbob", "agent", "bob", true},
		{"hello there", "", "", false},
		{"", "", "", false},
		{"/agentator", "agentator", "", true}, // not "agent"
	}
	for _, tc := range tests {
		cmd, arg, ok := parseSlashCommand(tc.in)
		if cmd != tc.wantCmd || arg != tc.wantArg || ok != tc.wantOK {
			t.Errorf("parseSlashCommand(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tc.in, cmd, arg, ok, tc.wantCmd, tc.wantArg, tc.wantOK)
		}
	}
}

func TestResolveDeviceAgent(t *testing.T) {
	agents := []DeviceAgentInfo{
		{ID: "bob", Name: "Bob"},
		{ID: "research", Name: ""}, // name-less: falls back to id
	}
	cases := []struct {
		arg      string
		wantID   string
		wantName string
	}{
		{"bob", "bob", "Bob"},                // by id
		{"Bob", "bob", "Bob"},                // by name, case-insensitive
		{"BOB", "bob", "Bob"},                // by id, case-insensitive
		{"research", "research", "research"}, // name-less → id as display
		{"nope", "", ""},                     // no match
	}
	for _, tc := range cases {
		id, name := resolveDeviceAgent(agents, tc.arg)
		if id != tc.wantID || name != tc.wantName {
			t.Errorf("resolveDeviceAgent(%q) = (%q,%q), want (%q,%q)", tc.arg, id, name, tc.wantID, tc.wantName)
		}
	}
}

func TestFormatAgentList(t *testing.T) {
	agents := []DeviceAgentInfo{{ID: "bob", Name: "Bob"}, {ID: "research", Name: ""}}
	got := formatAgentList(agents, "bob")
	for _, want := range []string{"Bob (current)", "research", "/agent <name>"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatAgentList missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "research (current)") {
		t.Errorf("non-current agent marked current:\n%s", got)
	}
}
