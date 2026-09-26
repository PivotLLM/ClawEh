package config

import (
	"encoding/json"
	"testing"
)

// TestDefaults_FloodControl: a seeded config caps concurrent turns at 8 and has
// the daily-spend alert off; a saved config that omits the keys inherits the
// same, and one that sets them wins.
func TestDefaults_FloodControl(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Agents.Defaults.MaxConcurrentTurns != 8 {
		t.Fatalf("MaxConcurrentTurns = %d, want 8", cfg.Agents.Defaults.MaxConcurrentTurns)
	}
	if cfg.Agents.Defaults.DailySpendAlertUSD != 0 {
		t.Fatalf("DailySpendAlertUSD = %v, want 0 (off)", cfg.Agents.Defaults.DailySpendAlertUSD)
	}

	cfg = DefaultConfig()
	if err := json.Unmarshal([]byte(`{"agents":{"defaults":{"max_concurrent_turns":0,"daily_spend_alert_usd":12.5}}}`), cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Agents.Defaults.MaxConcurrentTurns != 0 || cfg.Agents.Defaults.DailySpendAlertUSD != 12.5 {
		t.Fatalf("got max_concurrent_turns=%d daily_spend_alert_usd=%v, want 0 and 12.5",
			cfg.Agents.Defaults.MaxConcurrentTurns, cfg.Agents.Defaults.DailySpendAlertUSD)
	}
}
