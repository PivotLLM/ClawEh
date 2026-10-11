// ClawEh
// License: MIT

package report

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

func TestCollectChannels_AnySenderCalledOut(t *testing.T) {
	cfg, env := fixtureConfig(t)
	tb := findTable(t, collectChannels(t.Context(), cfg, env), "Enabled channels")

	_, tg := findRow(t, tb, "telegram-bob")
	contains(t, tg[2], "any sender (allow_from contains *)", "telegram senders")
	contains(t, tg[4], "bob (whole channel) [default delivery]", "telegram binding")
	contains(t, tg[4], "otherwise alice", "fallback agent")

	i, dc := findRow(t, tb, "discord")
	if dc[2] != "1234" {
		t.Errorf("discord senders = %q", dc[2])
	}
	if isHighlighted(tb, i) {
		t.Error("an allow-listed channel must not be highlighted")
	}
	contains(t, dc[4], "alice (channel c9, guild g1)", "discord binding match")

	_, sl := findRow(t, tb, "slack")
	contains(t, sl[2], "every sender is refused", "empty allow_from")

	_, mx := findRow(t, tb, "matrix")
	contains(t, mx[1], "https://matrix.example.com as @alice:example.com", "matrix identity")

	_, dev := findRow(t, tb, "device")
	contains(t, dev[2], "any paired device", "device empty allow_from")
}

func TestCollectChannels_NoneEnabled(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Channels.Telegram = nil
	cfg.Channels.Discord.Enabled = false
	cfg.Channels.Slack.Enabled = false
	cfg.Channels.Matrix.Enabled = false
	cfg.Channels.LINE.Enabled = false
	cfg.Channels.WebUI.Enabled = false
	cfg.Channels.Device.Enabled = false
	tb := findTable(t, collectChannels(t.Context(), cfg, env), "Enabled channels")
	if len(tb.Rows) != 1 || tb.Rows[0][0] != "(no channel enabled)" {
		t.Errorf("rows = %v", tb.Rows)
	}
}

// A peer binding with a kind and no id matches every peer of that kind, so
// the report names it that way rather than as a whole-channel binding.
func TestCollectChannels_PeerKindBinding(t *testing.T) {
	cfg, env := fixtureConfig(t)
	cfg.Bindings = append(cfg.Bindings, config.AgentBinding{
		AgentID: "bob",
		Match:   config.BindingMatch{Channel: "slack", AccountID: "*", Peer: &config.PeerMatch{Kind: "direct"}},
	})
	tb := findTable(t, collectChannels(t.Context(), cfg, env), "Enabled channels")
	_, sl := findRow(t, tb, "slack")
	contains(t, sl[4], "bob (account *, every direct message)", "slack direct-message binding")
}
