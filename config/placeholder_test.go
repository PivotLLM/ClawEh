// ClawEh
// License: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A placeholder switched off stays off through a save and a load, on every
// channel, including those whose default is on.
func TestPlaceholderDisabled_SurvivesSaveAndLoad(t *testing.T) {
	s := newTestStore(t)
	err := s.Update(func(c *Config) error {
		c.Channels.Slack.Placeholder.Enabled = false
		c.Channels.Discord.Placeholder.Enabled = false
		c.Channels.Matrix.Placeholder.Enabled = false
		c.Channels.WebUI.Placeholder.Enabled = false
		c.Channels.Telegram = []TelegramBotConfig{{ID: "default", Placeholder: PlaceholderConfig{Enabled: false}}}
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	cfg, err := LoadConfig(s.Path())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	for name, on := range map[string]bool{
		"slack":    cfg.Channels.Slack.Placeholder.Enabled,
		"discord":  cfg.Channels.Discord.Placeholder.Enabled,
		"matrix":   cfg.Channels.Matrix.Placeholder.Enabled,
		"webui":    cfg.Channels.WebUI.Placeholder.Enabled,
		"telegram": cfg.Channels.Telegram[0].Placeholder.Enabled,
	} {
		if on {
			t.Errorf("%s placeholder is on after saving it off", name)
		}
	}
}

// A config that does not mention the placeholder gets each channel's default:
// on for Slack and Matrix, off for Discord and the WebUI.
func TestPlaceholderAbsent_UsesDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"channels":{"slack":{"enabled":false},"matrix":{},"discord":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Channels.Slack.Placeholder.Enabled || cfg.Channels.Slack.Placeholder.Text == "" {
		t.Errorf("slack placeholder = %+v, want on with its text", cfg.Channels.Slack.Placeholder)
	}
	if !cfg.Channels.Matrix.Placeholder.Enabled {
		t.Error("matrix placeholder off by default")
	}
	if cfg.Channels.Discord.Placeholder.Enabled || cfg.Channels.WebUI.Placeholder.Enabled {
		t.Error("discord or webui placeholder on by default")
	}
}
