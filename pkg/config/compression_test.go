// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"testing"
)

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }

// TestEffectiveCompression_PerAgentOverridesDefaults covers the field-by-field
// overlay: an agent setting one value must not discard the rest of the defaults.
func TestEffectiveCompression_PerAgentOverridesDefaults(t *testing.T) {
	defaults := &CompressionConfig{
		Trigger: &CompressionTriggerConfig{MinPercent: intPtr(20), Days: intPtr(7)},
		Retain:  &CompressionRetainConfig{TokenPercent: intPtr(10), MaxAgeDays: intPtr(5)},
	}
	agent := &AgentConfig{Compression: &CompressionConfig{
		Trigger: &CompressionTriggerConfig{Days: intPtr(3)},
	}}

	got := agent.EffectiveCompression(defaults)

	if got.Trigger.Days == nil || *got.Trigger.Days != 3 {
		t.Errorf("per-agent Days should win, got %v", got.Trigger.Days)
	}
	if got.Trigger.MinPercent == nil || *got.Trigger.MinPercent != 20 {
		t.Errorf("unset per-agent fields must keep the default, got %v", got.Trigger.MinPercent)
	}
	if got.Retain == nil || got.Retain.MaxAgeDays == nil || *got.Retain.MaxAgeDays != 5 {
		t.Errorf("an untouched group must survive the overlay, got %+v", got.Retain)
	}
}

// TestEffectiveCompression_ZeroIsNotUnset is the reason every field is a
// pointer: the old plain-int form could not express "disable this trigger",
// because 0 meant "fall back to the built-in default".
func TestEffectiveCompression_ZeroIsNotUnset(t *testing.T) {
	defaults := &CompressionConfig{Trigger: &CompressionTriggerConfig{Days: intPtr(7)}}
	agent := &AgentConfig{Compression: &CompressionConfig{
		Trigger: &CompressionTriggerConfig{Days: intPtr(0)},
	}}

	got := agent.EffectiveCompression(defaults)
	if got.Trigger.Days == nil || *got.Trigger.Days != 0 {
		t.Errorf("an explicit 0 must override a non-zero default, got %v", got.Trigger.Days)
	}
}

// TestEffectiveCompression_NilInputs verifies the common case — nothing
// configured anywhere — yields an empty policy rather than a panic.
func TestEffectiveCompression_NilInputs(t *testing.T) {
	got := (*AgentConfig)(nil).EffectiveCompression(nil)
	if got == nil {
		t.Fatal("expected a non-nil policy")
	}
	if got.Trigger != nil || got.Retain != nil || got.Estimate != nil {
		t.Errorf("expected no overrides, got %+v", got)
	}
}

// TestMigrateCompression_FoldsLegacyKeys protects a deployed config: without
// this, splitting the flat compress_* keys would silently revert a running
// instance to built-in defaults on its next restart.
func TestMigrateCompression_FoldsLegacyKeys(t *testing.T) {
	cfg := &Config{}
	cfg.Agents.Defaults.CompressMinPercent = intPtr(25)
	cfg.Agents.Defaults.CompressRetainTokenPercent = intPtr(8)
	cfg.Agents.Defaults.CompressCharsPerToken = floatPtr(3.5)
	cfg.Agents.List = []AgentConfig{{ID: "alice", CompressMessageThreshold: intPtr(50)}}

	cfg.migrateCompressionConfigs()

	d := cfg.Agents.Defaults.Compression
	if d == nil || d.Trigger == nil || d.Trigger.MinPercent == nil || *d.Trigger.MinPercent != 25 {
		t.Fatalf("compress_min_percent not migrated: %+v", d)
	}
	if d.Retain == nil || d.Retain.TokenPercent == nil || *d.Retain.TokenPercent != 8 {
		t.Errorf("compress_retain_token_percent not migrated: %+v", d.Retain)
	}
	if d.Estimate == nil || d.Estimate.CharsPerToken == nil || *d.Estimate.CharsPerToken != 3.5 {
		t.Errorf("compress_chars_per_token not migrated: %+v", d.Estimate)
	}

	a := cfg.Agents.List[0].Compression
	if a == nil || a.Trigger == nil || a.Trigger.MessageCount == nil || *a.Trigger.MessageCount != 50 {
		t.Fatalf("per-agent compress_message_threshold not migrated: %+v", a)
	}

	// Legacy fields are cleared so nothing downstream can read a stale copy.
	if cfg.Agents.Defaults.CompressMinPercent != nil || cfg.Agents.List[0].CompressMessageThreshold != nil {
		t.Error("legacy fields must be cleared after migration")
	}
}

// TestMigrateCompression_NestedValueWins verifies migration only fills gaps: an
// operator who has already written the new form must not have it overwritten by
// a stale legacy key left in the file.
func TestMigrateCompression_NestedValueWins(t *testing.T) {
	cfg := &Config{}
	cfg.Agents.Defaults.CompressMinPercent = intPtr(25)
	cfg.Agents.Defaults.Compression = &CompressionConfig{
		Trigger: &CompressionTriggerConfig{MinPercent: intPtr(35)},
	}

	cfg.migrateCompressionConfigs()

	if got := *cfg.Agents.Defaults.Compression.Trigger.MinPercent; got != 35 {
		t.Errorf("explicit nested value = %d, want 35 (migration must not overwrite it)", got)
	}
}

// TestMigrateCompression_NoLegacyKeysIsNoOp confirms a clean config is left
// entirely alone rather than gaining empty sub-structs.
func TestMigrateCompression_NoLegacyKeysIsNoOp(t *testing.T) {
	cfg := &Config{}
	cfg.Agents.List = []AgentConfig{{ID: "bob"}}

	cfg.migrateCompressionConfigs()

	if cfg.Agents.Defaults.Compression != nil {
		t.Errorf("defaults gained a compression block from nothing: %+v", cfg.Agents.Defaults.Compression)
	}
	if cfg.Agents.List[0].Compression != nil {
		t.Errorf("agent gained a compression block from nothing: %+v", cfg.Agents.List[0].Compression)
	}
}

// TestCompressionConfig_JSONRoundTrip pins the wire names operators type.
func TestCompressionConfig_JSONRoundTrip(t *testing.T) {
	raw := `{
	  "trigger": {"min_percent": 20, "normal_percent": 50, "safety_percent": 80,
	              "message_count": 100, "days": 7},
	  "retain":  {"token_percent": 10, "max_tokens": 60000, "max_age_days": 5, "min_messages": 2},
	  "estimate":{"chars_per_token": 4.0, "token_safety_margin": 1.1},
	  "target_percent": 25
	}`
	var got CompressionConfig
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Trigger == nil || got.Trigger.Days == nil || *got.Trigger.Days != 7 {
		t.Errorf("trigger.days did not decode: %+v", got.Trigger)
	}
	if got.Retain == nil || got.Retain.MaxAgeDays == nil || *got.Retain.MaxAgeDays != 5 {
		t.Errorf("retain.max_age_days did not decode: %+v", got.Retain)
	}
	if got.Retain.MaxTokens == nil || *got.Retain.MaxTokens != 60000 {
		t.Errorf("retain.max_tokens did not decode: %+v", got.Retain)
	}
	if got.TargetPercent == nil || *got.TargetPercent != 25 {
		t.Errorf("target_percent did not decode: %+v", got.TargetPercent)
	}
}
