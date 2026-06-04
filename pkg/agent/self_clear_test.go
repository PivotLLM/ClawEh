// ClawEh
// License: MIT

package agent

import (
	"strings"
	"testing"
	"time"
)

func TestAllowSelfClear(t *testing.T) {
	al := &AgentLoop{}
	if !al.allowSelfClear("s") {
		t.Fatal("first self-clear should be allowed")
	}
	if al.allowSelfClear("s") {
		t.Error("second immediate self-clear should be rate-limited")
	}
	if !al.allowSelfClear("other") {
		t.Error("a different session should not be rate-limited")
	}
	// Simulate the cooldown elapsing.
	al.lastSelfClear.Store("s", time.Now().Add(-selfClearCooldown-time.Second))
	if !al.allowSelfClear("s") {
		t.Error("self-clear after the cooldown should be allowed")
	}
}

func TestWrapClearNotice(t *testing.T) {
	base := wrapClearNotice("")
	if !strings.Contains(base, "cleared") || strings.Contains(base, "Handoff") {
		t.Errorf("base notice unexpected: %q", base)
	}
	withHandoff := wrapClearNotice("  next: task B  ")
	if !strings.Contains(withHandoff, "Handoff note") || !strings.Contains(withHandoff, "next: task B") {
		t.Errorf("handoff notice unexpected: %q", withHandoff)
	}
}
