package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

func TestLoadChannelPrompt(t *testing.T) {
	ws := t.TempDir()
	for name, content := range map[string]string{
		"TELEGRAM.md":              "  Telegram text.\n",
		"DEVICE.md":                "Device text.",
		"SECMSG.md":                "Secure text.",
		"channel-slack.md":         "old Slack text",
		"AGENT_MESSAGE.md":         "never read",
		"channel-agent_message.md": "never read",
	} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.Channels.SecMsg = []config.SecMsgConfig{{Name: "signal"}}

	tests := []struct {
		channel string
		kindOf  func(string) string
		want    string
	}{
		{"telegram-main", nil, "Telegram text."},
		{"telegram", nil, "Telegram text."},
		{"device", nil, "Device text."},
		{"agent_message", nil, ""},
		{"slack", nil, ""}, // kind known, file missing; the old name is not read
		{"", nil, ""},
		{"signal-alice", nil, ""},
		{"signal-alice", cfg.ChannelKind, "Secure text."},
	}
	for _, tt := range tests {
		cb := NewContextBuilder(ws)
		if tt.kindOf != nil {
			cb = cb.WithChannelKind(tt.kindOf)
		}
		if got := cb.loadChannelPrompt(tt.channel); got != tt.want {
			t.Errorf("loadChannelPrompt(%q) = %q, want %q", tt.channel, got, tt.want)
		}
	}
}
