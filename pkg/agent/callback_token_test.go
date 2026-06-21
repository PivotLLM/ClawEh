// ClawEh
// License: MIT

package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/callback"
)

// TestValidateCallbackToken guards the security boundary of the callback
// endpoint: a token validates only against the agent that issued it, and bogus
// or empty tokens are rejected.
func TestValidateCallbackToken(t *testing.T) {
	mgrAmber, err := callback.NewManager("amber", filepath.Join(t.TempDir(), "amber.json"), 5, 3)
	if err != nil {
		t.Fatalf("NewManager amber: %v", err)
	}
	defer mgrAmber.Stop()
	mgrDawn, err := callback.NewManager("dawn", filepath.Join(t.TempDir(), "dawn.json"), 5, 3)
	if err != nil {
		t.Fatalf("NewManager dawn: %v", err)
	}
	defer mgrDawn.Stop()

	al := &AgentLoop{callbackManagers: map[string]*callback.Manager{
		"amber": mgrAmber,
		"dawn":  mgrDawn,
	}}

	// A valid token resolves to exactly the issuing agent.
	if id, ok := al.ValidateCallbackToken(mgrAmber.CurrentToken()); !ok || id != "amber" {
		t.Errorf("amber token -> (%q,%v), want (amber,true)", id, ok)
	}
	if id, ok := al.ValidateCallbackToken(mgrDawn.CurrentToken()); !ok || id != "dawn" {
		t.Errorf("dawn token -> (%q,%v), want (dawn,true)", id, ok)
	}

	// Forged and empty tokens are rejected — no agent leaks out.
	if id, ok := al.ValidateCallbackToken("forged-token"); ok {
		t.Errorf("forged token validated as %q", id)
	}
	if _, ok := al.ValidateCallbackToken(""); ok {
		t.Error("empty token must not validate")
	}
}

// TestHandleCallbackMessage_NoStateManager covers the guard that a callback for
// an agent with no state manager is rejected rather than panicking.
func TestHandleCallbackMessage_NoStateManager(t *testing.T) {
	al := &AgentLoop{} // no agentStates, no default state
	if err := al.HandleCallbackMessage(context.Background(), "ghost", "hello"); err == nil {
		t.Error("expected an error delivering a callback with no state manager")
	}
}
