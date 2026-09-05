// ClawEh
// License: MIT

package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/llmcontext"
)

func ip(v int) *int         { return &v }
func fp(v float64) *float64 { return &v }

// applyOpts is the only way to observe the mapper's effect: options are opaque
// closures, so build a config from them and read the result back.
func applyOpts(opts []llmcontext.Option) llmcontext.CompressionSettings {
	return llmcontext.SettingsFromOptions(opts...)
}

// TestCompressionOptions_MapsEveryField guards against a field being added to
// CompressionConfig and silently never reaching llmcontext.
func TestCompressionOptions_MapsEveryField(t *testing.T) {
	got := applyOpts(compressionOptions(&config.CompressionConfig{
		TargetPercent: ip(25),
		Trigger: &config.CompressionTriggerConfig{
			MinPercent: ip(21), NormalPercent: ip(51), SafetyPercent: ip(81),
			MessageCount: ip(101), Days: ip(7),
		},
		Retain: &config.CompressionRetainConfig{
			TokenPercent: ip(9), MaxTokens: ip(60_000), MaxAgeDays: ip(5), MinMessages: ip(3),
		},
		Estimate: &config.CompressionEstimateConfig{
			CharsPerToken: fp(3.5), TokenSafetyMargin: fp(1.2),
		},
	}))

	for _, tc := range []struct {
		name      string
		got, want int
	}{
		{"target_percent", got.TargetPercent, 25},
		{"trigger.min_percent", got.MinPercent, 21},
		{"trigger.normal_percent", got.NormalPercent, 51},
		{"trigger.safety_percent", got.SafetyPercent, 81},
		{"trigger.message_count", got.MessageThreshold, 101},
		{"trigger.days", got.TriggerDays, 7},
		{"retain.token_percent", got.RetainTokenPercent, 9},
		{"retain.max_tokens", got.RetainMaxTokens, 60_000},
		{"retain.max_age_days", got.RetainMaxAgeDays, 5},
		{"retain.min_messages", got.RetainMinMessages, 3},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
	if got.CharsPerToken != 3.5 {
		t.Errorf("estimate.chars_per_token = %v, want 3.5", got.CharsPerToken)
	}
	if got.TokenSafetyMargin != 1.2 {
		t.Errorf("estimate.token_safety_margin = %v, want 1.2", got.TokenSafetyMargin)
	}
}

// TestCompressionOptions_UnsetFieldsProduceNoOptions verifies an empty config
// leaves llmcontext's own defaults in place rather than overwriting them with
// zeroes.
func TestCompressionOptions_UnsetFieldsProduceNoOptions(t *testing.T) {
	if got := compressionOptions(&config.CompressionConfig{}); len(got) != 0 {
		t.Errorf("an empty config produced %d options, want 0", len(got))
	}
	if got := compressionOptions(nil); got != nil {
		t.Errorf("a nil config produced %d options, want none", len(got))
	}
}

// TestCompressionOptions_ExplicitZeroIsPassedThrough is how a trigger gets
// disabled: 0 must reach llmcontext rather than being treated as unset.
func TestCompressionOptions_ExplicitZeroIsPassedThrough(t *testing.T) {
	opts := compressionOptions(&config.CompressionConfig{
		Trigger: &config.CompressionTriggerConfig{Days: ip(0), MessageCount: ip(0)},
	})
	if len(opts) != 2 {
		t.Fatalf("expected 2 options for two explicit zeroes, got %d", len(opts))
	}
	got := applyOpts(opts)
	if got.TriggerDays != 0 || got.MessageThreshold != 0 {
		t.Errorf("explicit zeroes did not reach llmcontext: days=%d count=%d",
			got.TriggerDays, got.MessageThreshold)
	}
}
