// ClawEh
// License: MIT

package audit

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/msgtoken"
	"github.com/PivotLLM/ClawEh/servicetoken"
)

// serviceTokenRows reports which agents hold an MCP-host service token,
// never the tokens themselves.
func serviceTokenRows(dd string) [][]string {
	tokens, err := servicetoken.Load(servicetoken.Path(dd))
	if err != nil {
		return [][]string{row("Service tokens (MCP host)", "unavailable: "+err.Error())}
	}
	agents := servicetoken.Agents(tokens)
	if len(agents) == 0 {
		return [][]string{row("Service tokens (MCP host)", "none issued")}
	}
	sort.Strings(agents)
	rows := make([][]string, 0, len(agents))
	for _, id := range agents {
		rows = append(rows, row("Service token: agent "+id, "issued (1)"))
	}
	return rows
}

// integrationTokenRows reports the named long-lived message-API tokens per
// agent: their count and names, never the values.
func integrationTokenRows(cfg *config.Config, dd string) [][]string {
	store, err := msgtoken.NewNamedStore(msgtoken.NamedTokenPath(dd))
	if err != nil {
		return [][]string{row("Integration tokens (message API)", "unavailable: "+err.Error())}
	}
	var rows [][]string
	for i := range cfg.Agents.List {
		id := cfg.Agents.List[i].ID
		list := store.List(id)
		if len(list) == 0 {
			continue
		}
		names := make([]string, 0, len(list))
		for _, t := range list {
			names = append(names, t.Name)
		}
		rows = append(rows, row("Integration tokens: agent "+id, itoa(len(list))+": "+strings.Join(names, ", ")))
	}
	if len(rows) == 0 {
		rows = append(rows, row("Integration tokens (message API)", "none issued"))
	}
	return rows
}

// channelCredentialRows names, per enabled channel, the credential fields
// that are set.
func channelCredentialRows(cfg *config.Config) [][]string {
	var rows [][]string
	ch := cfg.Channels
	for _, b := range ch.Telegram {
		if b.Enabled {
			rows = append(rows, row("Telegram "+b.ChannelName(), "token "+setOrNot(b.Token)))
		}
	}
	for _, s := range ch.SecMsg {
		if s.Enabled {
			rows = append(rows, row("SecMsg "+orValue(s.Name, "secmsg"), "no credential in config (daemon at "+s.Address+")"))
		}
	}
	if ch.Discord.Enabled {
		rows = append(rows, row("Discord", "token "+setOrNot(ch.Discord.Token)))
	}
	if ch.Slack.Enabled {
		rows = append(rows, row("Slack", "bot_token "+setOrNot(ch.Slack.BotToken)+", app_token "+setOrNot(ch.Slack.AppToken)))
	}
	if ch.Matrix.Enabled {
		rows = append(rows, row("Matrix", "access_token "+setOrNot(ch.Matrix.AccessToken)))
	}
	if ch.LINE.Enabled {
		rows = append(rows, row("LINE", "channel_secret "+setOrNot(ch.LINE.ChannelSecret)+
			", channel_access_token "+setOrNot(ch.LINE.ChannelAccessToken)))
	}
	if ch.WebUI.Enabled {
		rows = append(rows, row("WebUI", "token "+setOrNot(ch.WebUI.Token)))
	}
	if ch.Device.Enabled {
		rows = append(rows, row("Device gateway", "token "+setOrNot(ch.Device.Token)+", word_token "+setOrNot(ch.Device.WordToken)))
	}
	if len(rows) == 0 {
		rows = append(rows, row("(no channel enabled)", ""))
	}
	return rows
}

func collectCredentials(cfg *config.Config, env Environment) Section {
	dd := dataDir(cfg, env)
	t := Table{Caption: "Tokens", Columns: []string{"Credential", "Status"}}
	t.Rows = append(t.Rows, serviceTokenRows(dd)...)
	t.Rows = append(t.Rows, integrationTokenRows(cfg, dd)...)
	for i := range cfg.Agents.List {
		a := &cfg.Agents.List[i]
		if a.Message != nil && a.Message.WindowMinutes > 0 {
			t.Rows = append(t.Rows, row("Rotating message tokens: agent "+a.ID,
				"window "+itoa(a.Message.WindowMinutes)+" min, "+itoa(a.Message.WindowCount)+" per window"))
		}
	}
	t.Rows = append(t.Rows,
		row("WebUI token", setOrNot(cfg.Channels.WebUI.Token)),
		row("WebUI token accepted in the query string", yesNo(cfg.Channels.WebUI.AllowTokenQuery)),
		row("Device gateway shared token", setOrNot(cfg.Channels.Device.Token)),
		row("Device gateway word token", setOrNot(cfg.Channels.Device.WordToken)),
	)

	return Section{
		Title: "Credentials and tokens",
		Notes: []string{
			"Values are never shown: a credential is reported as set or not set, by name, or by count. " +
				"Token stores live under " + filepath.Join(dd, "state") + ".",
		},
		Tables: []Table{
			t,
			{Caption: "Channel credentials", Columns: []string{"Channel", "Credentials"}, Rows: channelCredentialRows(cfg)},
		},
	}
}
