package config

import "strings"

// Channel kinds: one per channel implementation. Every channel of a kind (each
// Telegram bot, each secmsg account) shares the kind's workspace prompt file,
// ChannelPromptFile(kind).
const (
	ChannelKindDevice   = "device"
	ChannelKindDiscord  = "discord"
	ChannelKindMatrix   = "matrix"
	ChannelKindSecMsg   = "secmsg"
	ChannelKindSlack    = "slack"
	ChannelKindTelegram = "telegram"
	ChannelKindWebUI    = "webui"
)

// channelKinds is every channel kind, in a fixed order.
var channelKinds = []string{
	ChannelKindDevice,
	ChannelKindDiscord,
	ChannelKindMatrix,
	ChannelKindSecMsg,
	ChannelKindSlack,
	ChannelKindTelegram,
	ChannelKindWebUI,
}

// ChannelKinds returns every channel kind.
func ChannelKinds() []string {
	return append([]string(nil), channelKinds...)
}

// ChannelPromptFile returns the workspace file name holding a kind's
// channel-specific prompt ("TELEGRAM.md" for "telegram"), or "" for a name
// that is not a channel kind.
func ChannelPromptFile(kind string) string {
	for _, k := range channelKinds {
		if k == kind {
			return strings.ToUpper(k) + ".md"
		}
	}
	return ""
}

// ChannelKind returns the kind of the registered channel named channel, or ""
// for a name that is no channel's (internal channels such as "system" or
// "agent_message").
//
// The names of device, Discord, Slack, Matrix and WebUI are fixed, and a
// Telegram bot's is "telegram" or "telegram-<id>" (TelegramBotConfig.ChannelName).
// A secmsg account's name derives from its daemon's configured name or an
// explicit account name (SecMsgAccountConfig.ChannelName), so it is resolved
// against c's secmsg daemons; "secmsg" and "secmsg-<account>" (the names of a
// daemon with no name) are recognised without a config. c may be nil.
func (c *Config) ChannelKind(channel string) string {
	switch channel {
	case ChannelKindDevice, ChannelKindDiscord, ChannelKindMatrix, ChannelKindSlack,
		ChannelKindWebUI, ChannelKindTelegram, ChannelKindSecMsg:
		return channel
	}
	if strings.HasPrefix(channel, ChannelKindTelegram+"-") {
		return ChannelKindTelegram
	}
	if strings.HasPrefix(channel, ChannelKindSecMsg+"-") {
		return ChannelKindSecMsg
	}
	if c == nil || channel == "" {
		return ""
	}
	for _, daemon := range c.Channels.SecMsg {
		for _, account := range daemon.Accounts {
			if account.Name == channel {
				return ChannelKindSecMsg
			}
		}
		// Accounts found by discovery are named "<base>-<account>".
		base := SecMsgAccountConfig{}.ChannelName(daemon)
		if channel == base || strings.HasPrefix(channel, base+"-") {
			return ChannelKindSecMsg
		}
	}
	return ""
}

// ChannelKindsInUse returns the kinds with at least one enabled channel, in
// ChannelKinds order. c may be nil.
func (c *Config) ChannelKindsInUse() []string {
	if c == nil {
		return nil
	}
	ch := c.Channels
	inUse := map[string]bool{
		ChannelKindDevice:  ch.Device.Enabled,
		ChannelKindDiscord: ch.Discord.Enabled,
		ChannelKindMatrix:  ch.Matrix.Enabled,
		ChannelKindSlack:   ch.Slack.Enabled,
		ChannelKindWebUI:   ch.WebUI.Enabled,
	}
	for _, bot := range ch.Telegram {
		if bot.Enabled {
			inUse[ChannelKindTelegram] = true
		}
	}
	for _, daemon := range ch.SecMsg {
		if daemon.Enabled {
			inUse[ChannelKindSecMsg] = true
		}
	}
	var kinds []string
	for _, k := range channelKinds {
		if inUse[k] {
			kinds = append(kinds, k)
		}
	}
	return kinds
}
