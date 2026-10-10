// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"math/rand"
	"os"
	"strings"
	"testing"
)

// agentIDCase is one row of testdata/agent_id_cases.json, the table the WebUI
// (agent-id-field.test.tsx) checks its copy of the rule against.
type agentIDCase struct {
	Input      string  `json:"input"`
	Normalized string  `json:"normalized"`
	Valid      bool    `json:"valid"`
	Problem    *string `json:"problem"`
}

func loadAgentIDCases(t *testing.T) []agentIDCase {
	t.Helper()
	b, err := os.ReadFile("testdata/agent_id_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []agentIDCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	return cases
}

// The shared table: ValidAgentID, NormalizeAgentID and the refusal sentence
// give what the WebUI's copy must also give.
func TestAgentIDSharedCases(t *testing.T) {
	for _, tc := range loadAgentIDCases(t) {
		if got := ValidAgentID(tc.Input); got != tc.Valid {
			t.Errorf("ValidAgentID(%q) = %v, want %v", tc.Input, got, tc.Valid)
		}
		if got := NormalizeAgentID(tc.Input); got != tc.Normalized {
			t.Errorf("NormalizeAgentID(%q) = %q, want %q", tc.Input, got, tc.Normalized)
		}
		err := agentIDError(tc.Input, "")
		switch {
		case tc.Problem == nil && err != nil:
			t.Errorf("agentIDError(%q) = %v, want nil", tc.Input, err)
		case tc.Problem != nil && (err == nil || err.Error() != *tc.Problem):
			t.Errorf("agentIDError(%q) = %v, want %q", tc.Input, err, *tc.Problem)
		}
	}
}

// checkAgentIDRule fails t unless NormalizeAgentID(x) is a valid id in lower
// case and stable, and a valid x normalizes to its lower-case form.
func checkAgentIDRule(t *testing.T, x string) {
	t.Helper()
	n := NormalizeAgentID(x)
	if !ValidAgentID(n) {
		t.Errorf("NormalizeAgentID(%q) = %q, not a valid id", x, n)
	}
	if again := NormalizeAgentID(n); again != n {
		t.Errorf("NormalizeAgentID(%q) = %q, not idempotent (%q)", x, n, again)
	}
	if n != strings.ToLower(n) {
		t.Errorf("NormalizeAgentID(%q) = %q, not lower case", x, n)
	}
	if ValidAgentID(x) && n != strings.ToLower(x) {
		t.Errorf("valid id %q normalized to %q", x, n)
	}
	if ValidAgentID(x) != (agentIDError(x, "") == nil) {
		t.Errorf("ValidAgentID(%q) = %v disagrees with agentIDError", x, ValidAgentID(x))
	}
}

// The rule and the normalizer agree on random inputs drawn from characters
// that matter at the edges (separators, upper case, non-ASCII) and at lengths
// around the 64-character cap.
func TestAgentIDNormalizeProperties(t *testing.T) {
	for _, tc := range loadAgentIDCases(t) {
		checkAgentIDRule(t, tc.Input)
	}
	alphabet := []rune("abzAZ09-_ .!/\tÉéİK")
	rng := rand.New(rand.NewSource(1))
	for range 20000 {
		n := rng.Intn(80)
		var b strings.Builder
		for range n {
			b.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		checkAgentIDRule(t, b.String())
	}
}

func FuzzAgentIDRule(f *testing.F) {
	for _, s := range []string{"", "alice", "_alice", "Alice.Smith", strings.Repeat("a", 63) + "-b", "İ"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkAgentIDRule(t, s)
	})
}
