// ClawEh
// License: MIT

package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/msgtoken"
)

// TestValidateMessageToken guards the security boundary of the external-message
// endpoint: a token validates only against the agent that issued it, and bogus
// or empty tokens are rejected.
func TestValidateMessageToken(t *testing.T) {
	mgrAmber, err := msgtoken.NewManager("amber", filepath.Join(t.TempDir(), "amber.json"), 5, 3)
	if err != nil {
		t.Fatalf("NewManager amber: %v", err)
	}
	defer mgrAmber.Stop()
	mgrDawn, err := msgtoken.NewManager("dawn", filepath.Join(t.TempDir(), "dawn.json"), 5, 3)
	if err != nil {
		t.Fatalf("NewManager dawn: %v", err)
	}
	defer mgrDawn.Stop()

	al := &AgentLoop{messageManagers: map[string]*msgtoken.Manager{
		"amber": mgrAmber,
		"dawn":  mgrDawn,
	}}

	// A valid token resolves to exactly the issuing agent.
	if id, ok := al.ValidateMessageToken(mgrAmber.CurrentToken()); !ok || id != "amber" {
		t.Errorf("amber token -> (%q,%v), want (amber,true)", id, ok)
	}
	if id, ok := al.ValidateMessageToken(mgrDawn.CurrentToken()); !ok || id != "dawn" {
		t.Errorf("dawn token -> (%q,%v), want (dawn,true)", id, ok)
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
	tok, err := named.Create("amber", "gps")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	named.Update("amber", tok.ID, 2, 15) // limit 2/min

	mgr, err := msgtoken.NewManager("dawn", filepath.Join(t.TempDir(), "dawn.json"), 5, 3)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Stop()

	al := &AgentLoop{
		namedTokens:     named,
		messageManagers: map[string]*msgtoken.Manager{"dawn": mgr},
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
	if agentID != "amber" || retry <= 0 {
		t.Fatalf("rate-limited check = (%q,%v), want (amber, >0)", agentID, retry)
	}

	// Rotating tokens are never rate-limited.
	if id, _, d := al.CheckMessageToken(mgr.CurrentToken()); d != MsgTokenValid || id != "dawn" {
		t.Fatalf("rotating check = (%q,%v), want (dawn,Valid)", id, d)
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
