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

// Every Telegram bot gets the typing and placeholder defaults, not only the
// first.
func TestTelegramBotDefaults_EveryBot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	p := filepath.Join(dir, "config.json")
	body := `{"channels":{"telegram":[{"id":"alice"},{"id":"bob"},{"id":"carol"}]}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Channels.Telegram) != 3 {
		t.Fatalf("telegram = %+v, want three bots", cfg.Channels.Telegram)
	}
	for _, b := range cfg.Channels.Telegram {
		if !b.Typing.Enabled || !b.Placeholder.Enabled {
			t.Errorf("bot %q: typing=%v placeholder=%v, want both on",
				b.ID, b.Typing.Enabled, b.Placeholder.Enabled)
		}
	}
}

// An explicit false on any bot survives a save and a load; the others keep
// the defaults.
func TestTelegramBotDefaults_FalseSurvivesOnEveryBot(t *testing.T) {
	tests := []struct {
		name string
		idx  int
	}{
		{"first", 0},
		{"second", 1},
		{"third", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			err := s.Update(func(c *Config) error {
				on := func(id string) TelegramBotConfig {
					return TelegramBotConfig{
						ID:          id,
						Typing:      TypingConfig{Enabled: true},
						Placeholder: PlaceholderConfig{Enabled: true},
					}
				}
				bots := []TelegramBotConfig{on("alice"), on("bob"), on("carol")}
				bots[tt.idx].Typing.Enabled = false
				bots[tt.idx].Placeholder.Enabled = false
				c.Channels.Telegram = bots
				return nil
			})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			cfg, err := LoadConfig(s.Path())
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if len(cfg.Channels.Telegram) != 3 {
				t.Fatalf("telegram = %+v, want three bots", cfg.Channels.Telegram)
			}
			for i, b := range cfg.Channels.Telegram {
				want := i != tt.idx
				if b.Typing.Enabled != want || b.Placeholder.Enabled != want {
					t.Errorf("bot %q: typing=%v placeholder=%v, want %v",
						b.ID, b.Typing.Enabled, b.Placeholder.Enabled, want)
				}
			}
		})
	}
}
