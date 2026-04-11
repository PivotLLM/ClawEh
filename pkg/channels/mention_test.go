package channels

import (
	"testing"
)

func TestExtractAgentMention(t *testing.T) {
	knownAgents := []string{"alice", "bob"}
	defaultTriggers := []string{"@", "/", "."}

	tests := []struct {
		name           string
		text           string
		triggers       []string
		knownAgents    []string
		wantAgent      string
		wantStripped   string
	}{
		{
			name:         "at-mention basic",
			text:         "@alice do something",
			triggers:     defaultTriggers,
			knownAgents:  knownAgents,
			wantAgent:    "alice",
			wantStripped: "do something",
		},
		{
			name:         "dot-trigger basic",
			text:         ".alice do something",
			triggers:     defaultTriggers,
			knownAgents:  knownAgents,
			wantAgent:    "alice",
			wantStripped: "do something",
		},
		{
			name:         "slash-trigger basic",
			text:         "/alice do something",
			triggers:     defaultTriggers,
			knownAgents:  knownAgents,
			wantAgent:    "alice",
			wantStripped: "do something",
		},
		{
			name:         "case insensitive mention",
			text:         "@Alice do something",
			triggers:     defaultTriggers,
			knownAgents:  knownAgents,
			wantAgent:    "alice",
			wantStripped: "do something",
		},
		{
			name:         "unknown agent returns no match",
			text:         "@carol do something",
			triggers:     defaultTriggers,
			knownAgents:  knownAgents,
			wantAgent:    "",
			wantStripped: "@carol do something",
		},
		{
			name:         "bare mention with no space returns no match",
			text:         "@alice",
			triggers:     defaultTriggers,
			knownAgents:  knownAgents,
			wantAgent:    "",
			wantStripped: "@alice",
		},
		{
			name:         "non-trigger prefix returns no match",
			text:         "hello alice do something",
			triggers:     defaultTriggers,
			knownAgents:  knownAgents,
			wantAgent:    "",
			wantStripped: "hello alice do something",
		},
		{
			name:         "empty triggers returns no match",
			text:         "@alice do something",
			triggers:     []string{},
			knownAgents:  knownAgents,
			wantAgent:    "",
			wantStripped: "@alice do something",
		},
		{
			name:         "bob mention works",
			text:         "@bob help me",
			triggers:     defaultTriggers,
			knownAgents:  knownAgents,
			wantAgent:    "bob",
			wantStripped: "help me",
		},
		{
			name:         "trigger with empty candidate (just trigger and space)",
			text:         "@ alice do something",
			triggers:     defaultTriggers,
			knownAgents:  knownAgents,
			wantAgent:    "",
			wantStripped: "@ alice do something",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAgent, gotStripped := ExtractAgentMention(tt.text, tt.triggers, tt.knownAgents)
			if gotAgent != tt.wantAgent {
				t.Errorf("agentName = %q, want %q", gotAgent, tt.wantAgent)
			}
			if gotStripped != tt.wantStripped {
				t.Errorf("stripped = %q, want %q", gotStripped, tt.wantStripped)
			}
		})
	}
}
