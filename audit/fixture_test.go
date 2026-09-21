// ClawEh
// License: MIT

package audit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
)

// Distinctive fake secrets planted everywhere a credential can live. The
// no-secrets test asserts none of them reach the rendered report.
const (
	secretAPIKey     = "sk-FAKEKEY-9f8e7d6c"
	secretProxyPass  = "PROXYSECRET-8a7b"
	secretEnvValue   = "ENVSECRET-6c5d4e"
	secretHeader     = "Bearer HDRSECRET-3f2e1d"
	secretMCPEnv     = "MCPENVSECRET-0a9b"
	secretTelegram   = "TGSECRET-7e6f5a"
	secretDiscord    = "DISCORDSECRET-4d3c"
	secretWebUI      = "WEBUISECRET-2b1a"
	secretDevice     = "DEVSECRET-1c2d3e"
	secretWordToken  = "apple banana cherry dolphin eagle"
	secretBrave      = "BRAVESECRET-5e6f"
	secretGitHub     = "GHSECRET-7a8b"
	secretClawHub    = "HUBSECRET-9c0d"
	secretSTT        = "STTSECRET-1e2f"
	secretSlackBot   = "xoxb-SLACKSECRET-3a4b"
	secretSlackApp   = "xapp-SLACKSECRET-5c6d"
	secretMatrix     = "MATRIXSECRET-7e8f"
	secretLINE       = "LINESECRET-9a0b"
	secretLINEAccess = "LINEACCESS-1c2d"
)

var allSecrets = []string{
	secretAPIKey, secretProxyPass, secretEnvValue, secretHeader, secretMCPEnv, secretTelegram,
	secretDiscord, secretWebUI, secretDevice, secretWordToken, secretBrave, secretGitHub,
	secretClawHub, secretSTT, secretSlackBot, secretSlackApp, secretMatrix, secretLINE, secretLINEAccess,
}

// fixtureEnv is the process environment the tests describe.
func fixtureEnv(dataDir string) Environment {
	return Environment{
		ConfigPath: filepath.Join(dataDir, "config.json"),
		DataDir:    dataDir,
		Executable: "/usr/local/bin/alice",
		Hostname:   "testbox",
		User:       "eric",
		Group:      "staff",
		OS:         "linux",
		Arch:       "amd64",
		GoVersion:  "go1.27.1",
		Version:    "0.6.0+abcdef12",
		Commit:     "abcdef12",
		BuildTime:  "20260921120000",
		Now:        time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC),
	}
}

// fixtureConfig builds a small realistic config under a fresh CLAW_HOME:
// one API provider with a key, two CLI providers, two agents (one with a
// symlinked workspace, one with explicit tools and a mount), MCP servers of
// both transports, and every channel type with a fake credential.
func fixtureConfig(t *testing.T) (*config.Config, Environment) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	cfg := config.DefaultConfig()

	realWS := filepath.Join(home, "real-workspace")
	linkWS := filepath.Join(home, "link-workspace")
	if err := os.MkdirAll(filepath.Join(realWS, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realWS, linkWS); err != nil {
		t.Fatal(err)
	}
	docs := filepath.Join(home, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg.Providers = []config.Provider{
		{
			Name: "OpenAI", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", APIKey: secretAPIKey,
			Proxy: "http://proxyuser:" + secretProxyPass + "@proxy.local:3128",
		},
		{Name: "Claude CLI", Protocol: "claude-cli"},
		{Name: "Codex CLI", Protocol: "codex-cli", Command: "/opt/codex/bin/codex"},
	}
	cfg.Models = []config.ModelConfig{
		{ModelName: "GPT", Model: "gpt-5.5", Provider: "OpenAI", Enabled: true, ThinkingLevel: "high", ContextWindow: 200000},
		{ModelName: "GPT Old", Model: "gpt-4o", Provider: "OpenAI", Enabled: false, NoTools: true},
		{
			ModelName: "Claude CLI", Model: "claude-cli", Provider: "Claude CLI", Enabled: true,
			ExtraArgs: []string{"--dangerously-skip-permissions", "--no-chrome", "--verbose"},
			Env:       map[string]string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1", "MY_SECRET_ENV": secretEnvValue},
		},
		{ModelName: "Codex Fast", Model: "o4-mini", Provider: "Codex CLI", Enabled: true, Workspace: "/tmp/codex-ws"},
	}
	cfg.Agents.Defaults.Models = []string{"GPT", "Claude CLI"}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "alice", Name: "Alice", Default: true, Workspace: linkWS, MCPTools: []string{"fusion", "nothing"}},
		{
			ID: "bob", Name: "Bob", Tools: []string{"file_read", "shell_exec"}, GlobalCron: true,
			Mounts:  []config.MountConfig{{Name: "docs", Path: docs, Notify: true}},
			Maestro: &config.MaestroConfig{Enabled: true, MaxConcurrent: 3},
		},
	}
	cfg.Tools.AllowReadPaths = []string{"^/srv/shared/"}
	cfg.Tools.AllowWritePaths = []string{"^/srv/out/"}
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"fusion": {
			Enabled: true, Type: "http", URL: "https://fusion.example.com/mcp",
			Headers: map[string]string{"Authorization": secretHeader},
		},
		"local": {
			Enabled: true, Command: "npx", Args: []string{"-y", "some-mcp-server"},
			Env: map[string]string{"API_TOKEN": secretMCPEnv}, EnvFile: "/etc/mcp.env",
		},
	}
	cfg.Tools.Web.Brave = config.BraveConfig{Enabled: true, APIKey: secretBrave}
	cfg.Tools.Skills.Github.Token = secretGitHub
	cfg.Tools.Skills.Registries.ClawHub.AuthToken = secretClawHub
	cfg.Voice.STT = []config.STTProvider{{Provider: "groq", Enabled: true, APIKey: secretSTT}}

	cfg.Gateway = config.GatewayConfig{Host: "0.0.0.0", Port: 18790, AllowedCIDRs: []string{"192.168.1.0/24"}}
	cfg.Channels.Telegram = []config.TelegramBotConfig{{ID: "bob", Enabled: true, Token: secretTelegram, AllowFrom: []string{"*"}}}
	cfg.Channels.Discord = config.DiscordConfig{Enabled: true, Token: secretDiscord, AllowFrom: []string{"1234"}}
	cfg.Channels.Slack = config.SlackConfig{Enabled: true, BotToken: secretSlackBot, AppToken: secretSlackApp}
	cfg.Channels.Matrix = config.MatrixConfig{
		Enabled: true, Homeserver: "https://matrix.example.com", UserID: "@alice:example.com",
		AccessToken: secretMatrix, AllowFrom: []string{"@eric:example.com"},
	}
	cfg.Channels.LINE = config.LINEConfig{
		Enabled: true, ChannelSecret: secretLINE, ChannelAccessToken: secretLINEAccess,
		WebhookHost: "0.0.0.0", WebhookPort: 18792, WebhookPath: "/webhook/line", AllowFrom: []string{"U1"},
	}
	cfg.Channels.WebUI = config.WebUIConfig{Enabled: true, Token: secretWebUI, AllowOrigins: []string{"https://alice.example.com"}}
	cfg.Channels.Device = config.DeviceChannelConfig{Enabled: true, Token: secretDevice, WordToken: secretWordToken, Host: "0.0.0.0"}
	cfg.Bindings = []config.AgentBinding{
		{AgentID: "bob", Match: config.BindingMatch{Channel: "telegram-bob"}, Default: true, DeliverTo: "42"},
		{AgentID: "alice", Match: config.BindingMatch{Channel: "discord", GuildID: "g1", Peer: &config.PeerMatch{Kind: "channel", ID: "c9"}}},
	}
	return cfg, fixtureEnv(home)
}

// findTable returns the captioned table in a section.
func findTable(t *testing.T, s Section, caption string) Table {
	t.Helper()
	for _, tb := range s.Tables {
		if tb.Caption == caption {
			return tb
		}
	}
	t.Fatalf("table %q not found in section %q", caption, s.Title)
	return Table{}
}

// findRow returns the first row whose first cell starts with prefix.
func findRow(t *testing.T, tb Table, prefix string) (int, []string) {
	t.Helper()
	for i, r := range tb.Rows {
		if len(r) > 0 && strings.HasPrefix(r[0], prefix) {
			return i, r
		}
	}
	t.Fatalf("row starting %q not found in table %q", prefix, tb.Caption)
	return -1, nil
}

func tableText(tb Table) string {
	var b strings.Builder
	for _, r := range tb.Rows {
		b.WriteString(strings.Join(r, " | ") + "\n")
	}
	return b.String()
}

func contains(t *testing.T, haystack, needle, what string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("%s: expected to contain %q\n---\n%s", what, needle, haystack)
	}
}

func isHighlighted(tb Table, i int) bool {
	return slices.Contains(tb.Highlight, i)
}
