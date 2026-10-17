// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"os"
	"testing"
)

// toolGrantCase is one row of testdata/tool_grant_cases.json, the table the
// WebUI's toolGranted (agent-model.test.ts) is checked against. It covers
// native tools only: mcp_ tools are gated by mcp_tools, which the WebUI does
// not evaluate this way.
type toolGrantCase struct {
	Tools     []string `json:"tools"`
	DenyTools []string `json:"deny_tools"`
	Tool      string   `json:"tool"`
	Allowed   bool     `json:"allowed"`
}

func TestToolGrantSharedCases(t *testing.T) {
	b, err := os.ReadFile("testdata/tool_grant_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []toolGrantCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, tc := range cases {
		a := &AgentConfig{Tools: tc.Tools, DenyTools: tc.DenyTools}
		if got := a.IsToolAllowed(tc.Tool); got != tc.Allowed {
			t.Errorf("IsToolAllowed(%q) with tools %q, deny_tools %q = %v, want %v",
				tc.Tool, tc.Tools, tc.DenyTools, got, tc.Allowed)
		}
	}
}
