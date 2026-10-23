// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import (
	"encoding/json"
	"os"
	"testing"
)

// Card ids follow the agent id rule: the shared table the config package and
// the WebUI are checked against gives the same validity here, and a valid id
// normalizes to the same lower-case identity.
func TestIDRuleMatchesAgentIDCases(t *testing.T) {
	b, err := os.ReadFile("../config/testdata/agent_id_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Input      string `json:"input"`
		Normalized string `json:"normalized"`
		Valid      bool   `json:"valid"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, tc := range cases {
		if got := ValidID(tc.Input); got != tc.Valid {
			t.Errorf("ValidID(%q) = %v, want %v", tc.Input, got, tc.Valid)
		}
		if tc.Valid {
			if got := NormalizeID(tc.Input); got != tc.Normalized {
				t.Errorf("NormalizeID(%q) = %q, want %q", tc.Input, got, tc.Normalized)
			}
		}
	}
}

func TestSameID(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"Sherlock", "sherlock", true},
		{"sherlock", "watson", false},
		{"", "", false},
		{"bad id", "bad id", false},
	} {
		if got := sameID(tc.a, tc.b); got != tc.want {
			t.Errorf("sameID(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
