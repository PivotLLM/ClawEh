package config

import "testing"

// TestDefaultConfig_CallbacksOffByDefault locks in that newly-created agents do
// not get the external callback endpoint enabled by default — the prompt-injected
// callback token is opt-in and must only appear when an agent explicitly sets a
// window > 0. (The runtime side — nil/0 window yields no manager — is covered by
// TestBuildCallbackManagers_TracksConfig.)
func TestDefaultConfig_CallbacksOffByDefault(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Agents.List) == 0 {
		t.Fatal("expected at least one default agent")
	}
	for _, a := range cfg.Agents.List {
		if a.Callback != nil && a.Callback.WindowMinutes > 0 {
			t.Errorf("default agent %q must have callbacks off, got window_minutes=%d", a.ID, a.Callback.WindowMinutes)
		}
	}
}
