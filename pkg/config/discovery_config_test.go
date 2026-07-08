package config

import (
	"encoding/json"
	"testing"
)

// A config written before the rename (bare "ttl") normalizes onto TTLMax.
func TestDiscoveryConfig_LegacyTTLNormalizes(t *testing.T) {
	var d ToolDiscoveryConfig
	if err := json.Unmarshal([]byte(`{"ttl": 7}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.TTLMax != 7 {
		t.Fatalf("legacy ttl should normalize to TTLMax, got %d", d.TTLMax)
	}
}

// An explicit ttl_max always wins over a stale ttl.
func TestDiscoveryConfig_TTLMaxWinsOverLegacy(t *testing.T) {
	var d ToolDiscoveryConfig
	if err := json.Unmarshal([]byte(`{"ttl": 7, "ttl_max": 40}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.TTLMax != 40 {
		t.Fatalf("ttl_max should win over legacy ttl, got %d", d.TTLMax)
	}
}

// Unset discovery values fall back to the defaults.
func TestDiscoveryConfig_Defaults(t *testing.T) {
	c := &Config{}
	if got := c.DiscoveryTTLMax(); got != DefaultDiscoveryTTLMax {
		t.Errorf("DiscoveryTTLMax default = %d, want %d", got, DefaultDiscoveryTTLMax)
	}
	if got := c.DiscoveryVisibleBudget(); got != DefaultDiscoveryVisibleBudget {
		t.Errorf("DiscoveryVisibleBudget default = %d, want %d", got, DefaultDiscoveryVisibleBudget)
	}
}
