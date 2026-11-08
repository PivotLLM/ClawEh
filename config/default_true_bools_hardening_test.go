// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A hand-written config with the default-on switches set false loads them
// false, and saving it writes each key out so the next load agrees.
func TestDefaultOnSwitches_ExplicitFalseInFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	p := filepath.Join(dir, "config.json")
	body := `{"channels":{
		"slack":{"placeholder":{"enabled":false}},
		"matrix":{"placeholder":{"enabled":false},"group_trigger":{"mention_only":false}},
		"telegram":[{"id":"default","typing":{"enabled":false},"placeholder":{"enabled":false}}]
	}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	check := func(stage string, cfg *Config) {
		t.Helper()
		ch := cfg.Channels
		if ch.Slack.Placeholder.Enabled || ch.Matrix.Placeholder.Enabled || ch.Matrix.GroupTrigger.MentionOnly ||
			ch.Telegram[0].Typing.Enabled || ch.Telegram[0].Placeholder.Enabled {
			t.Errorf("%s: a switch set false reads as on: slack %+v, matrix %+v %+v, telegram %+v %+v", stage,
				ch.Slack.Placeholder, ch.Matrix.Placeholder, ch.Matrix.GroupTrigger,
				ch.Telegram[0].Typing, ch.Telegram[0].Placeholder)
		}
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	check("load", cfg)
	if err = SaveConfig(p, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	// The saved file names every one of them explicitly.
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Channels struct {
			Slack struct {
				Placeholder map[string]any `json:"placeholder"`
			} `json:"slack"`
			Matrix struct {
				Placeholder  map[string]any `json:"placeholder"`
				GroupTrigger map[string]any `json:"group_trigger"`
			} `json:"matrix"`
			Telegram []struct {
				Typing      map[string]any `json:"typing"`
				Placeholder map[string]any `json:"placeholder"`
			} `json:"telegram"`
		} `json:"channels"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]any{
		"slack.placeholder.enabled":         doc.Channels.Slack.Placeholder["enabled"],
		"matrix.placeholder.enabled":        doc.Channels.Matrix.Placeholder["enabled"],
		"matrix.group_trigger.mention_only": doc.Channels.Matrix.GroupTrigger["mention_only"],
		"telegram[0].typing.enabled":        doc.Channels.Telegram[0].Typing["enabled"],
		"telegram[0].placeholder.enabled":   doc.Channels.Telegram[0].Placeholder["enabled"],
	} {
		if got != false {
			t.Errorf("saved %s = %v, want false written out", name, got)
		}
	}

	cfg, err = LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig after save: %v", err)
	}
	check("reload", cfg)
}

// Switching a default-on switch back on after it was saved off also
// survives a save and a load.
func TestDefaultOnSwitches_BackOnSurvives(t *testing.T) {
	s := newTestStore(t)
	off := func(c *Config) error {
		c.Channels.Slack.Placeholder.Enabled = false
		c.Channels.Matrix.GroupTrigger.MentionOnly = false
		return nil
	}
	on := func(c *Config) error {
		c.Channels.Slack.Placeholder.Enabled = true
		c.Channels.Matrix.GroupTrigger.MentionOnly = true
		return nil
	}
	for i, fn := range []func(*Config) error{off, on} {
		if err := s.Update(fn); err != nil {
			t.Fatalf("Update %d: %v", i, err)
		}
	}
	cfg, err := LoadConfig(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Channels.Slack.Placeholder.Enabled || !cfg.Channels.Matrix.GroupTrigger.MentionOnly {
		t.Errorf("slack placeholder %v, matrix mention_only %v, want both on again",
			cfg.Channels.Slack.Placeholder.Enabled, cfg.Channels.Matrix.GroupTrigger.MentionOnly)
	}
}
