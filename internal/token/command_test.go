// ClawEh
// License: MIT

package token

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/servicetoken"
)

// setupHome points CLAW_HOME at a temp dir with a minimal two-agent config and
// returns the data dir.
func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	cfg := `{"agents":{"list":[{"id":"alice","name":"alice","default":true},{"id":"bob","name":"bob"}]}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return home
}

func TestIssueListRevoke(t *testing.T) {
	home := setupHome(t)
	path := servicetoken.Path(home)

	if err := issue("alice"); err != nil {
		t.Fatalf("issue alice: %v", err)
	}
	toks, loadErr := servicetoken.Load(path)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	first := toks["alice"]
	if first == "" {
		t.Fatal("issue did not persist a token for alice")
	}

	// Issuing again replaces (one token per agent).
	if err := issue("alice"); err != nil {
		t.Fatalf("re-issue alice: %v", err)
	}
	toks, loadErr = servicetoken.Load(path)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if toks["alice"] == "" || toks["alice"] == first {
		t.Errorf("re-issue should replace the token, got %q (was %q)", toks["alice"], first)
	}
	if len(servicetoken.Agents(toks)) != 1 {
		t.Errorf("expected exactly one agent with a token, got %v", servicetoken.Agents(toks))
	}

	// Revoke removes it.
	if err := revoke("alice"); err != nil {
		t.Fatalf("revoke alice: %v", err)
	}
	toks, loadErr = servicetoken.Load(path)
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if _, ok := toks["alice"]; ok {
		t.Error("revoke did not remove the token")
	}
}

func TestIssue_UnknownAgentErrors(t *testing.T) {
	setupHome(t)
	if err := issue("ghost"); err == nil {
		t.Error("issue for an unconfigured agent should error")
	}
}

// TestRevoke_AgentRemovedFromConfig: the token of an agent that is no longer
// in the config is still revoked, so it cannot come back to life with a new
// agent of the same id.
func TestRevoke_AgentRemovedFromConfig(t *testing.T) {
	home := setupHome(t)
	// Bob has been removed from the config; his token is still in the store.
	cfg := `{"agents":{"list":[{"id":"alice","name":"alice","default":true}]}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	path := servicetoken.Path(home)
	if err := servicetoken.Save(path, map[string]string{"bob": servicetoken.Hash("t")}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := revoke("Bob"); err != nil {
		t.Fatalf("revoke bob (not in config): %v", err)
	}
	toks, err := servicetoken.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := toks["bob"]; ok {
		t.Error("revoke did not remove the token of an agent missing from the config")
	}
}

func TestRevoke_UnknownAgentWithoutTokenErrors(t *testing.T) {
	setupHome(t)
	if err := revoke("ghost"); err == nil {
		t.Error("revoke for an agent neither configured nor holding a token should error")
	}
	// A configured agent without a token is not an error.
	if err := revoke("bob"); err != nil {
		t.Errorf("revoke bob (configured, no token): %v", err)
	}
}
