package config

import "testing"

// TestDefaultConfig_MessageEndpointOffByDefault locks in that newly-created agents do
// not get the external-message endpoint enabled by default — it is opt-in (the prompt-injected
// token framing is gone). It must only stay off unless an agent explicitly sets a
// window > 0. (The runtime side — nil/0 window yields no manager — is covered by
// TestBuildMessageManagers_TracksConfig.)
func TestDefaultConfig_MessageEndpointOffByDefault(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Agents.List) == 0 {
		t.Fatal("expected at least one default agent")
	}
	for _, a := range cfg.Agents.List {
		if a.Message != nil && a.Message.WindowMinutes > 0 {
			t.Errorf("default agent %q must have the message endpoint off, got window_minutes=%d", a.ID, a.Message.WindowMinutes)
		}
	}
}
