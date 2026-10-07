// ClawEh
// License: MIT

package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/msgtoken"
)

// TestValidateMessageToken guards the security boundary of the external-message
// endpoint: a token validates only against the agent that issued it, and bogus
// or empty tokens are rejected.
func TestValidateMessageToken(t *testing.T) {
	mgrAlice, err := msgtoken.NewManager("alice", filepath.Join(t.TempDir(), "alice.json"), 5, 3)
	if err != nil {
		t.Fatalf("NewManager alice: %v", err)
	}
	defer mgrAlice.Stop()
	mgrBob, err := msgtoken.NewManager("bob", filepath.Join(t.TempDir(), "bob.json"), 5, 3)
	if err != nil {
		t.Fatalf("NewManager bob: %v", err)
	}
	defer mgrBob.Stop()

	al := &AgentLoop{messageManagers: map[string]*msgtoken.Manager{
		"alice": mgrAlice,
		"bob":   mgrBob,
	}}

	// A valid token resolves to exactly the issuing agent.
	if id, ok := al.ValidateMessageToken(mgrAlice.CurrentToken()); !ok || id != "alice" {
		t.Errorf("alice token -> (%q,%v), want (alice,true)", id, ok)
	}
	if id, ok := al.ValidateMessageToken(mgrBob.CurrentToken()); !ok || id != "bob" {
		t.Errorf("bob token -> (%q,%v), want (bob,true)", id, ok)
	}

	// Forged and empty tokens are rejected — no agent leaks out.
	if id, ok := al.ValidateMessageToken("forged-token"); ok {
		t.Errorf("forged token validated as %q", id)
	}
	if _, ok := al.ValidateMessageToken(""); ok {
		t.Error("empty token must not validate")
	}
}

// TestCheckMessageToken_RateLimit exercises the decision surface used by the
// message route: a named token flooded past its limit becomes RateLimited, a
// rotating token is never rate-limited, and junk is Invalid.
func TestCheckMessageToken_RateLimit(t *testing.T) {
	named, err := msgtoken.NewNamedStore("")
	if err != nil {
		t.Fatalf("NewNamedStore: %v", err)
	}
	tok, err := named.Create("alice", "gps")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	named.Update("alice", tok.ID, 2, 15) // limit 2/min

	mgr, err := msgtoken.NewManager("bob", filepath.Join(t.TempDir(), "bob.json"), 5, 3)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Stop()

	al := &AgentLoop{
		namedTokens:     named,
		messageManagers: map[string]*msgtoken.Manager{"bob": mgr},
	}

	// Within limit → Valid.
	if _, _, d := al.CheckMessageToken(tok.Token); d != MsgTokenValid {
		t.Fatalf("1st check decision = %v, want Valid", d)
	}
	if _, _, d := al.CheckMessageToken(tok.Token); d != MsgTokenValid {
		t.Fatalf("2nd check decision = %v, want Valid", d)
	}
	// Third trips the limit → RateLimited with a positive retryAfter.
	agentID, retry, d := al.CheckMessageToken(tok.Token)
	if d != MsgTokenRateLimited {
		t.Fatalf("3rd check decision = %v, want RateLimited", d)
	}
	if agentID != "alice" || retry <= 0 {
		t.Fatalf("rate-limited check = (%q,%v), want (alice, >0)", agentID, retry)
	}

	// Rotating tokens are never rate-limited.
	if id, _, d := al.CheckMessageToken(mgr.CurrentToken()); d != MsgTokenValid || id != "bob" {
		t.Fatalf("rotating check = (%q,%v), want (bob,Valid)", id, d)
	}

	// Junk → Invalid.
	if _, _, d := al.CheckMessageToken("forged"); d != MsgTokenInvalid {
		t.Fatalf("junk check decision = %v, want Invalid", d)
	}
}

// TestHandleExternalMessage_NoConfig covers the guard that an external message for
// an agent loop with no config is rejected rather than panicking. Delivery now
// resolves the target via CronTarget, so no config means nowhere to deliver.
func TestHandleExternalMessage_NoConfig(t *testing.T) {
	al := &AgentLoop{} // no config
	if err := al.HandleExternalMessage(context.Background(), "ghost", "hello"); err == nil {
		t.Error("expected an error delivering an external message with no config")
	}
}
