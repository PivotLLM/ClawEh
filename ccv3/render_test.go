// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import (
	"encoding/json"
	"testing"
)

func card(data string) *Card {
	return &Card{Spec: SpecV3, SpecVersion: SpecVersionV3, Data: json.RawMessage(data)}
}

// The full card renders every section in order; the fields that are never
// rendered (greetings, post-history instructions, keyed and disabled entries,
// creator notes, tags, assets, dates) do not appear.
func TestRenderFullCard(t *testing.T) {
	c := card(`{
  "name": "Sherlock Holmes",
  "nickname": "Holmes",
  "description": "{{char}} is a consulting detective.{{// private note}}",
  "personality": "Cold and precise with {{User}}.",
  "scenario": "  221B Baker Street.  ",
  "first_mes": "FIRST_MES",
  "mes_example": "<START>\n{{user}}: Hello.\n{{char}}: You have been in Afghanistan.\n<start>\n{{char}}: Uses ` + "```" + ` fences.\n<START>",
  "system_prompt": "Stay in character as {{char}}. {{random:a,b}}",
  "post_history_instructions": "POST_HISTORY",
  "alternate_greetings": ["ALT"],
  "group_only_greetings": ["GROUP"],
  "tags": ["TAG"],
  "creator_notes": "CREATOR_NOTES",
  "assets": [{"type": "icon", "uri": "ASSET", "name": "main", "ext": "png"}],
  "creation_date": 1700000000,
  "character_book": {"entries": [
    {"keys": [], "content": "Second constant.", "enabled": true, "constant": true, "insertion_order": 5},
    {"keys": ["k"], "content": "KEYED", "enabled": true, "insertion_order": 0},
    {"keys": [], "content": "DISABLED", "enabled": false, "constant": true, "insertion_order": 0},
    {"keys": [], "content": "First constant, about {{char}}.", "enabled": true, "constant": true, "insertion_order": 1}
  ]}
}`)
	want := "# Character\n\n" +
		"Stay in character as Holmes. {{random:a,b}}\n\n" +
		"You are Holmes.\n\n" +
		"## Description\nHolmes is a consulting detective.\n\n" +
		"## Personality\nCold and precise with the user.\n\n" +
		"## Scenario\n221B Baker Street.\n\n" +
		"## How Sherlock Holmes speaks\n" +
		"```text\nthe user: Hello.\nHolmes: You have been in Afghanistan.\n```\n\n" +
		"````text\nHolmes: Uses ``` fences.\n````\n\n" +
		"## Background\nFirst constant, about Holmes.\n\nSecond constant."
	if got := Render(c); got != want {
		t.Errorf("Render =\n%s\n--- want ---\n%s", got, want)
	}
}

// A card with only a name renders the heading and the identity sentence.
func TestRenderMinimalCard(t *testing.T) {
	want := "# Character\n\nYou are Watson."
	if got := Render(card(`{"name":"Watson","description":"   ","tags":[]}`)); got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

func TestRenderUnreadableCard(t *testing.T) {
	if got := Render(card(`[]`)); got != "" {
		t.Errorf("Render(unreadable) = %q, want empty", got)
	}
}

func TestExpandMacros(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"{{char}} meets {{user}}.", "Bob meets the user."},
		{"{{CHAR}} and {{User}}", "Bob and the user"},
		{"a{{// comment}}b", "ab"},
		{"a{{//}}b", "ab"},
		{"{{random:x,y}} {{roll:d6}} {{original}}", "{{random:x,y}} {{roll:d6}} {{original}}"},
		{"{{ char }}", "{{ char }}"},
		{"unclosed {{char", "unclosed {{char"},
		{"{{char}}{{char}}", "BobBob"},
		{"no macros", "no macros"},
		{"", ""},
	} {
		if got := ExpandMacros(tc.in, "Bob"); got != tc.want {
			t.Errorf("ExpandMacros(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
