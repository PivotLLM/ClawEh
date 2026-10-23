// ClawEh
// License: MIT

package channels

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

// channelSpec is one channel a configuration asks for: its name, how to build
// it, and a fingerprint of everything the build reads.
//
// The inputs of each channel (a reload rebuilds a channel only when they
// change; the tests in reload_test.go and device/reload_test.go pin them):
//
//   - telegram, telegram-<id>: its channels.telegram[] entry.
//   - secmsg accounts: the channels.secmsg[] daemon entry and the account,
//     daemon defaults applied (pinned, or found by discovery on every reload).
//   - discord, slack, matrix, webui: their channels.<name> section.
//   - device: channels.device, the data directory, logging.log_message_content,
//     gateway.external_url, gateway.lockout_exempt, gateway.trusted_proxies and
//     the names on the HTTPS certificate file (see the device package's
//     registered inputs).
//   - built-in channels: always rebuilt, since their factory may read anything.
//
// Shared by every channel, and so rebuilding all of them when it changes: the
// media store (replaced when tools.media_cleanup changes). Voice transcription
// is set on the agent loop, not on a channel, so it is no channel's input.
// Everything else the manager hands a channel (its platform name, the
// alerter, the placeholder recorder, the build hook) is the same for the life
// of the process.
type channelSpec struct {
	name     string
	display  string
	platform string
	// inputs fingerprints what build reads; "" means it cannot be compared, so
	// a reload always rebuilds the channel.
	inputs string
	build  func() (Channel, error)
	// allowFrom is warned about when empty and checkAllowFrom is set, once
	// the channel is built.
	allowFrom      []string
	checkAllowFrom bool
	// daemon fingerprints the SecMsg daemon entry of an account channel.
	daemon string
}

// fingerprint is a digest of v's JSON form, or "" when it has none (the
// channel is then always rebuilt). A digest rather than the JSON keeps the
// tokens in the inputs out of the manager's memory.
func fingerprint(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// plan lists the channels cfg asks for, built-ins last. It runs SecMsg
// account discovery, so it may block on a daemon.
func (m *Manager) plan(cfg *config.Config) []channelSpec {
	var specs []channelSpec
	taken := map[string]bool{}
	add := func(s channelSpec) {
		if taken[s.name] {
			logger.ErrorCF("channels", "Duplicate channel name — skipping", map[string]any{
				"channel": s.display,
				"name":    s.name,
			})
			return
		}
		taken[s.name] = true
		specs = append(specs, s)
	}

	for _, bot := range cfg.Channels.Telegram {
		if bot.Enabled && bot.Token != "" {
			if s, ok := telegramSpec(bot, m); ok {
				add(s)
			}
		}
	}

	for _, sm := range cfg.Channels.SecMsg {
		if sm.Enabled && sm.Address != "" {
			for _, s := range m.secmsgSpecs(sm) {
				add(s)
			}
		}
	}

	if cfg.Channels.Discord.Enabled && cfg.Channels.Discord.Token != "" {
		if s, ok := factorySpec(cfg, m, "discord", "Discord", cfg.Channels.Discord.AllowFrom); ok {
			add(s)
		}
	}

	if cfg.Channels.Slack.Enabled && cfg.Channels.Slack.BotToken != "" {
		if s, ok := factorySpec(cfg, m, "slack", "Slack", cfg.Channels.Slack.AllowFrom); ok {
			add(s)
		}
	}

	if cfg.Channels.Matrix.Enabled &&
		cfg.Channels.Matrix.Homeserver != "" &&
		cfg.Channels.Matrix.UserID != "" &&
		cfg.Channels.Matrix.AccessToken != "" {
		if s, ok := factorySpec(cfg, m, "matrix", "Matrix", cfg.Channels.Matrix.AllowFrom); ok {
			add(s)
		}
	}

	if cfg.Channels.WebUI.Enabled && cfg.Channels.WebUI.Token != "" {
		if s, ok := factorySpec(cfg, m, "webui", "WebUI", cfg.Channels.WebUI.AllowFrom); ok {
			add(s)
		}
	}

	if cfg.Channels.Device.Enabled {
		if s, ok := factorySpec(cfg, m, "device", "Device", cfg.Channels.Device.AllowFrom); ok {
			add(s)
		}
	}

	return withBuiltins(specs, cfg, m)
}

// withBuiltins appends every registered built-in channel (see RegisterBuiltin)
// to specs. A configured channel of the same name is kept and the built-in
// skipped.
func withBuiltins(specs []channelSpec, cfg *config.Config, m *Manager) []channelSpec {
	taken := map[string]bool{}
	for _, s := range specs {
		taken[s.name] = true
	}
	for _, name := range builtinNames() {
		f, ok := getBuiltin(name)
		if !ok {
			continue
		}
		if taken[name] {
			logger.WarnCF("channels", "Built-in channel skipped: a configured channel has its name",
				map[string]any{"channel": name})
			continue
		}
		specs = append(specs, channelSpec{
			name:     name,
			display:  name,
			platform: name,
			build:    func() (Channel, error) { return f(cfg, m.bus) },
		})
	}
	return specs
}

// factorySpec is the spec of a channel built by a factory registered with
// RegisterFactory, or false when none is.
func factorySpec(cfg *config.Config, m *Manager, name, display string, allowFrom []string) (channelSpec, bool) {
	f, ok := getFactory(name)
	if !ok {
		logger.WarnCF("channels", "Factory not registered", map[string]any{"channel": display})
		return channelSpec{}, false
	}
	inputs := ""
	if in, ok := getFactoryInputs(name); ok {
		inputs = fingerprint(in(cfg))
	}
	return channelSpec{
		name:     name,
		display:  display,
		platform: name,
		inputs:   inputs,
		build:    func() (Channel, error) { return f(cfg, m.bus) },

		allowFrom:      allowFrom,
		checkAllowFrom: true,
	}, true
}

// telegramSpec is the spec of one configured Telegram bot.
func telegramSpec(bot config.TelegramBotConfig, m *Manager) (channelSpec, bool) {
	display := "Telegram"
	if bot.ID != "" && bot.ID != "default" {
		display = "Telegram (" + bot.ID + ")"
	}
	f, ok := getTelegramBotFactory()
	if !ok {
		logger.WarnCF("channels", "Telegram bot factory not registered", map[string]any{"channel": display})
		return channelSpec{}, false
	}
	return channelSpec{
		name:     bot.ChannelName(),
		display:  display,
		platform: "telegram",
		inputs:   fingerprint(bot),
		build:    func() (Channel, error) { return f(bot, m.bus) },

		allowFrom:      bot.AllowFrom,
		checkAllowFrom: true,
	}, true
}

// secmsgSpecs are the specs of a secure-messaging daemon's accounts: the
// pinned ones, or the ones discovery finds, each inheriting the daemon-level
// allow_from / group_trigger defaults. When discovery fails, the accounts of
// this daemon entry that are running are kept as they are.
func (m *Manager) secmsgSpecs(cfg config.SecMsgConfig) []channelSpec {
	f, ok := getSecMsgFactory()
	if !ok {
		logger.WarnCF("channels", "SecMsg factory not registered", map[string]any{
			"channel": "SecMsg (" + cfg.Name + ")",
		})
		return nil
	}
	daemon := fingerprint(cfg)
	accounts, ok := m.resolveSecMsgAccounts(cfg)
	if !ok {
		return m.runningSpecs(daemon)
	}
	var specs []channelSpec
	for _, account := range accounts {
		name := account.ChannelName(cfg)
		specs = append(specs, channelSpec{
			name:     name,
			display:  "SecMsg (" + name + ")",
			platform: "secmsg",
			inputs: fingerprint(struct {
				Daemon  config.SecMsgConfig
				Account config.SecMsgAccountConfig
			}{cfg, account}),
			build:          func() (Channel, error) { return f(cfg, account, m.bus) },
			allowFrom:      account.AllowFrom,
			checkAllowFrom: true,
			daemon:         daemon,
		})
	}
	return specs
}

// runningSpecs are the specs of the running channels built for the SecMsg
// daemon entry daemon (a fingerprint), sorted by name.
func (m *Manager) runningSpecs(daemon string) []channelSpec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var specs []channelSpec
	for _, s := range m.specs {
		if s.daemon == daemon {
			specs = append(specs, s)
		}
	}
	slices.SortFunc(specs, func(a, b channelSpec) int { return strings.Compare(a.name, b.name) })
	return specs
}
