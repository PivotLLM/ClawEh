// ClawEh
// License: MIT

package audit

import (
	"slices"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
)

// channelInfo is one enabled inbound channel as the report describes it.
type channelInfo struct {
	Name     string // routing name bindings refer to (telegram-<id>, discord, ...)
	Identity string // non-secret identity: bot id, homeserver, address, webhook path
	Senders  string // who may talk to it
	Trigger  string // when it answers in groups
	AnyOpen  bool   // accepts any sender
}

func groupTrigger(g config.GroupTriggerConfig) string {
	if !g.MentionOnly && len(g.Prefixes) == 0 {
		return "any message"
	}
	parts := []string{}
	if g.MentionOnly {
		parts = append(parts, "mention only")
	}
	if len(g.Prefixes) > 0 {
		parts = append(parts, "prefixes "+strings.Join(g.Prefixes, " "))
	}
	return strings.Join(parts, ", ")
}

// senders renders an allow_from list the way channels enforce it: empty
// refuses everyone, * admits anyone. The device gateway treats empty as *.
func senders(allow []string, emptyIsAny bool) (string, bool) {
	if len(allow) == 0 {
		if emptyIsAny {
			return "any paired device (allow_from empty)", true
		}
		return "none: allow_from is empty, every sender is refused", false
	}
	if slices.Contains(allow, "*") {
		return "any sender (allow_from contains *)", true
	}
	return strings.Join(allow, ", "), false
}

// enabledChannels lists every channel the runtime would start.
func enabledChannels(cfg *config.Config) []channelInfo {
	var out []channelInfo
	add := func(name, identity string, allow []string, emptyIsAny bool, trigger string) {
		s, open := senders(allow, emptyIsAny)
		out = append(out, channelInfo{Name: name, Identity: identity, Senders: s, Trigger: trigger, AnyOpen: open})
	}
	ch := cfg.Channels
	for _, b := range ch.Telegram {
		if !b.Enabled {
			continue
		}
		id := "bot id " + orValue(b.ID, "default")
		if b.BaseURL != "" {
			id += ", api " + b.BaseURL
		}
		add(b.ChannelName(), id, b.AllowFrom, false, groupTrigger(b.GroupTrigger))
	}
	for _, s := range ch.SecMsg {
		if !s.Enabled {
			continue
		}
		if len(s.Accounts) == 0 {
			add(orValue(s.Name, "secmsg"), "daemon "+s.Address+" (accounts discovered at start)", s.AllowFrom, false, groupTrigger(s.GroupTrigger))
			continue
		}
		for _, a := range s.Accounts {
			eff := s.WithDefaults(a)
			add(a.ChannelName(s), "daemon "+s.Address+", account "+orValue(a.Account, "(sole account)"), eff.AllowFrom, false, groupTrigger(eff.GroupTrigger))
		}
	}
	if ch.Discord.Enabled {
		add("discord", "bot (identified by its token)", ch.Discord.AllowFrom, false, groupTrigger(ch.Discord.GroupTrigger))
	}
	if ch.Slack.Enabled {
		add("slack", "app (socket mode)", ch.Slack.AllowFrom, false, groupTrigger(ch.Slack.GroupTrigger))
	}
	if ch.Matrix.Enabled {
		id := ch.Matrix.Homeserver + " as " + orValue(ch.Matrix.UserID, unknown)
		if ch.Matrix.JoinOnInvite {
			id += ", joins rooms on invite"
		}
		add("matrix", id, ch.Matrix.AllowFrom, false, groupTrigger(ch.Matrix.GroupTrigger))
	}
	if ch.LINE.Enabled {
		add("line", "webhook "+ch.LINE.WebhookPath, ch.LINE.AllowFrom, false, groupTrigger(ch.LINE.GroupTrigger))
	}
	if ch.WebUI.Enabled {
		add("webui", "browser sessions on the gateway listener", ch.WebUI.AllowFrom, false, "n/a")
	}
	if ch.Device.Enabled {
		add("device", "paired hardware devices on the device gateway", ch.Device.AllowFrom, true, "n/a")
	}
	return out
}

// bindingsFor lists the agents bound to a channel, with what each binding
// matches on, and the agent that takes anything left over.
func bindingsFor(cfg *config.Config, channel string) string {
	var parts []string
	for _, b := range cfg.Bindings {
		if !strings.EqualFold(b.Match.Channel, channel) {
			continue
		}
		var on []string
		if b.Match.AccountID != "" {
			on = append(on, "account "+b.Match.AccountID)
		}
		if b.Match.Peer != nil && b.Match.Peer.ID != "" {
			on = append(on, b.Match.Peer.Kind+" "+b.Match.Peer.ID)
		}
		if b.Match.GuildID != "" {
			on = append(on, "guild "+b.Match.GuildID)
		}
		if b.Match.TeamID != "" {
			on = append(on, "team "+b.Match.TeamID)
		}
		s := b.AgentID
		if len(on) > 0 {
			s += " (" + strings.Join(on, ", ") + ")"
		} else {
			s += " (whole channel)"
		}
		if b.Default {
			s += " [default delivery]"
		}
		parts = append(parts, s)
	}
	parts = append(parts, "otherwise "+defaultAgentID(cfg))
	return strings.Join(parts, "; ")
}

func collectChannels(cfg *config.Config, _ Environment) Section {
	t := Table{Caption: "Enabled channels", Columns: []string{"Channel", "Identity", "Allowed senders", "Group trigger", "Agents"}}
	for i, c := range enabledChannels(cfg) {
		t.Rows = append(t.Rows, row(c.Name, c.Identity, c.Senders, c.Trigger, bindingsFor(cfg, c.Name)))
		if c.AnyOpen {
			t.Highlight = append(t.Highlight, i)
		}
	}
	if len(t.Rows) == 0 {
		t.Rows = append(t.Rows, row("(no channel enabled)", "", "", "", ""))
	}
	return Section{
		Title: "Channels",
		Notes: []string{
			"allow_from decides who may talk to an agent through a channel: an empty list refuses every sender, " +
				"an entry of * accepts any sender (highlighted). Bindings route a channel, or part of one, to an agent; " +
				"anything no binding claims goes to the default agent.",
		},
		Tables: []Table{t},
	}
}
