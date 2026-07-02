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

// TestHandleExternalMessage_NoConfig covers the guard that an external message for
// an agent loop with no config is rejected rather than panicking. Delivery now
// resolves the target via CronTarget, so no config means nowhere to deliver.
func TestHandleExternalMessage_NoConfig(t *testing.T) {
	al := &AgentLoop{} // no config
	if err := al.HandleExternalMessage(context.Background(), "ghost", "hello"); err == nil {
		t.Error("expected an error delivering an external message with no config")
	}
}
