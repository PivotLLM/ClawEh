package config

import (
	"slices"
	"testing"
)

func TestChannelKind(t *testing.T) {
	cfg := &Config{}
	cfg.Channels.SecMsg = []SecMsgConfig{
		{Name: "signal", Accounts: []SecMsgAccountConfig{{Account: "bob", Name: "family"}}},
	}
	tests := []struct {
		channel string
		cfg     *Config
		want    string
	}{
		{"device", nil, ChannelKindDevice},
		{"discord", nil, ChannelKindDiscord},
		{"slack", nil, ChannelKindSlack},
		{"matrix", nil, ChannelKindMatrix},
		{"webui", nil, ChannelKindWebUI},
		{"telegram", nil, ChannelKindTelegram},
		{"telegram-main", nil, ChannelKindTelegram},
		{"secmsg", nil, ChannelKindSecMsg},
		{"secmsg-alice", nil, ChannelKindSecMsg},
		{"signal-alice", nil, ""},
		{"signal-alice", cfg, ChannelKindSecMsg},
		{"signal", cfg, ChannelKindSecMsg},
		{"family", cfg, ChannelKindSecMsg},
		{"signalx", cfg, ""},
		{"agent_message", cfg, ""},
		{"system", cfg, ""},
		{"", cfg, ""},
		{"Device", nil, ""},
		{"telegram_main", nil, ""},
	}
	for _, tt := range tests {
		if got := tt.cfg.ChannelKind(tt.channel); got != tt.want {
			t.Errorf("ChannelKind(%q) (config: %v) = %q, want %q", tt.channel, tt.cfg != nil, got, tt.want)
		}
	}
}

func TestChannelKindsInUse(t *testing.T) {
	cfg := &Config{}
	cfg.Channels.Device.Enabled = true
	cfg.Channels.Telegram = []TelegramBotConfig{{ID: "main", Enabled: true}, {ID: "spare"}}
	cfg.Channels.SecMsg = []SecMsgConfig{{Name: "signal"}}

	want := []string{ChannelKindDevice, ChannelKindTelegram}
	if got := cfg.ChannelKindsInUse(); !slices.Equal(got, want) {
		t.Errorf("ChannelKindsInUse() = %v, want %v", got, want)
	}
	if got := (*Config)(nil).ChannelKindsInUse(); got != nil {
		t.Errorf("nil config: ChannelKindsInUse() = %v, want nil", got)
	}
}

func TestChannelPromptFile(t *testing.T) {
	for _, kind := range ChannelKinds() {
		if got := ChannelPromptFile(kind); got == "" {
			t.Errorf("ChannelPromptFile(%q) is empty", kind)
		}
	}
	if got := ChannelPromptFile("telegram"); got != "TELEGRAM.md" {
		t.Errorf("ChannelPromptFile(telegram) = %q, want TELEGRAM.md", got)
	}
	if got := ChannelPromptFile("agent_message"); got != "" {
		t.Errorf("ChannelPromptFile(agent_message) = %q, want empty", got)
	}
}
