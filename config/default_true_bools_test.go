// ClawEh
// License: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A Telegram bot's typing indicator and Matrix's mention-only rule, both on by
// default, stay off through a save and a load once switched off.
func TestDefaultOnChannelBools_FalseSurvivesSaveAndLoad(t *testing.T) {
	s := newTestStore(t)
	err := s.Update(func(c *Config) error {
		c.Channels.Telegram = []TelegramBotConfig{{ID: "default", Typing: TypingConfig{Enabled: false}}}
		c.Channels.Matrix.GroupTrigger.MentionOnly = false
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	cfg, err := LoadConfig(s.Path())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Channels.Telegram[0].Typing.Enabled {
		t.Error("telegram typing is on after saving it off")
	}
	if cfg.Channels.Matrix.GroupTrigger.MentionOnly {
		t.Error("matrix group_trigger.mention_only is on after saving it off")
	}
}

// A config that does not mention them gets the defaults: on for both.
func TestDefaultOnChannelBools_AbsentUsesDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	p := filepath.Join(dir, "config.json")
	body := `{"channels":{"telegram":[{"id":"default"}],"matrix":{}}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Channels.Telegram) != 1 || !cfg.Channels.Telegram[0].Typing.Enabled {
		t.Errorf("telegram = %+v, want one bot with typing on", cfg.Channels.Telegram)
	}
	if !cfg.Channels.Matrix.GroupTrigger.MentionOnly {
		t.Error("matrix group_trigger.mention_only off by default")
	}
}
