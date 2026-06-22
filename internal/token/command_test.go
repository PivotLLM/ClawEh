// ClawEh
// License: MIT

package token

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/servicetoken"
)

// setupHome points CLAW_HOME at a temp dir with a minimal two-agent config and
// returns the data dir.
func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	cfg := `{"agents":{"list":[{"id":"amber","name":"amber","default":true},{"id":"dawn","name":"dawn"}]}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return home
}

func TestIssueListRevoke(t *testing.T) {
	home := setupHome(t)
	path := servicetoken.Path(home)

	if err := issue("amber"); err != nil {
		t.Fatalf("issue amber: %v", err)
	}
	toks, _ := servicetoken.Load(path)
	first := toks["amber"]
	if first == "" {
		t.Fatal("issue did not persist a token for amber")
	}

	// Issuing again replaces (one token per agent).
	if err := issue("amber"); err != nil {
		t.Fatalf("re-issue amber: %v", err)
	}
	toks, _ = servicetoken.Load(path)
	if toks["amber"] == "" || toks["amber"] == first {
		t.Errorf("re-issue should replace the token, got %q (was %q)", toks["amber"], first)
	}
	if len(servicetoken.Agents(toks)) != 1 {
		t.Errorf("expected exactly one agent with a token, got %v", servicetoken.Agents(toks))
	}

	// Revoke removes it.
	if err := revoke("amber"); err != nil {
		t.Fatalf("revoke amber: %v", err)
	}
	toks, _ = servicetoken.Load(path)
	if _, ok := toks["amber"]; ok {
		t.Error("revoke did not remove the token")
	}
}

func TestIssue_UnknownAgentErrors(t *testing.T) {
	setupHome(t)
	if err := issue("ghost"); err == nil {
		t.Error("issue for an unconfigured agent should error")
	}
}
