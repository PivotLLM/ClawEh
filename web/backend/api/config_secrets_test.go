package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// secretFixture returns a config carrying one credential of each shape the
// masking has to cover, written to disk, plus the handler mux serving it.
func secretFixture(t *testing.T) (string, *http.ServeMux, map[string]string) {
	t.Helper()
	// One entry per secret-bearing config field (config.go fields named
	// token/key/keys/secret/password/headers/env/proxy). Every value is unique
	// and at least 12 characters, so a leak is unambiguous and the partial mask
	// applies. Proxy userinfo is covered as separate user and password values.
	secrets := map[string]string{
		// providers[].api_key / proxy, models[].env (CLI models)
		"provider_api_key":    "sk-provider-key-abcdefghijkl",
		"provider_proxy_user": "provider-proxy-username",
		"provider_proxy_pass": "provider-proxy-password-secret",
		"cli_env_api_key":     "sk-ant-cli-env-key-abcdefghijkl",
		"cli_env_plain":       "/home/operator/cli-home-dir", // env value that does not look like a secret
		// channels.telegram[].token / proxy
		"telegram_token":      "1234567:telegram-bot-token-xyz",
		"telegram_proxy_pass": "telegram-proxy-password-secret",
		// channels.discord.token / proxy
		"discord_token":      "discord-token-abcdefghijkl",
		"discord_proxy_pass": "discord-proxy-password-secret",
		// channels.slack.bot_token / app_token
		"slack_bot_token": "xoxb-slack-bot-token-abcdefghijkl",
		"slack_app_token": "xapp-slack-app-token-abcdefghijkl",
		// channels.matrix.access_token
		"matrix_access_token": "syt_matrix-access-token-abcdefghijkl",
		// channels.line.channel_secret / channel_access_token
		"line_secret":       "line-channel-secret-abcdefghijkl",
		"line_access_token": "line-channel-access-token-abcdefghijkl",
		// channels.webui.token
		"webui_token": "webui-channel-token-abcdef",
		// channels.device.token / word_token
		"device_token": "device-shared-token-abcdef",
		"device_word":  "anchor-velvet-puzzle-ranger-cobalt",
		// tools.web.{brave,tavily,perplexity}.api_key / api_keys, glm_search.api_key, proxy
		"brave_key":       "BSA-brave-search-key-abcdef",
		"brave_key2":      "BSA-brave-rotation-key-wxyz01",
		"tavily_key":      "tvly-tavily-search-key-abcdef",
		"tavily_key2":     "tvly-tavily-rotation-key-abcdef",
		"perplexity_key":  "pplx-perplexity-key-abcdefghijkl",
		"perplexity_key2": "pplx-perplexity-rotation-key-abcdef",
		"glm_key":         "glm-search-key-abcdefghijkl",
		"web_proxy_pass":  "web-proxy-password-secret",
		// tools.skills.github.token / proxy, tools.skills.registries.clawhub.auth_token
		"skills_github_token":      "ghp_skills-github-token-abcdefghijkl",
		"skills_github_proxy_pass": "skills-github-proxy-password-secret",
		"clawhub_auth_token":       "clawhub-auth-token-abcdefghijkl",
		// tools.mcp.servers[].env / headers
		"mcp_env_secret":  "mcp-env-secret-value-abcdefghijkl",
		"mcp_env_plain":   "mcp-env-plain-looking-value",
		"mcp_header_auth": "Bearer mcp-header-secret-abcdefghijkl",
	}
	cfg := config.DefaultConfig()
	cfg.Models = []config.ModelConfig{
		{ModelName: "m", Model: "gpt-4o", Provider: "OpenAI", Enabled: true},
		{
			ModelName: "cli", Model: "claude-sonnet", Provider: "ClaudeCLI", Enabled: true,
			Env: map[string]string{"ANTHROPIC_API_KEY": secrets["cli_env_api_key"], "HOME": secrets["cli_env_plain"]},
		},
	}
	cfg.Agents.Defaults.Models = []string{"m", "cli"} // the template's CLI aliases are not in Models above
	cfg.Providers = []config.Provider{
		{
			Name: "OpenAI", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", APIKey: secrets["provider_api_key"],
			Proxy: "http://" + secrets["provider_proxy_user"] + ":" + secrets["provider_proxy_pass"] + "@proxy.local:3128",
		},
		{Name: "ClaudeCLI", Protocol: "claude-cli"},
	}
	cfg.Channels.Telegram = []config.TelegramBotConfig{{
		ID: "b1", Enabled: true, Token: secrets["telegram_token"],
		Proxy: "socks5://tg:" + secrets["telegram_proxy_pass"] + "@proxy.local:1080",
	}}
	cfg.Channels.Discord.Enabled = true
	cfg.Channels.Discord.Token = secrets["discord_token"]
	cfg.Channels.Discord.Proxy = "http://dc:" + secrets["discord_proxy_pass"] + "@proxy.local:3128"
	cfg.Channels.Slack.BotToken = secrets["slack_bot_token"]
	cfg.Channels.Slack.AppToken = secrets["slack_app_token"]
	cfg.Channels.Matrix.AccessToken = secrets["matrix_access_token"]
	cfg.Channels.LINE.ChannelSecret = secrets["line_secret"]
	cfg.Channels.LINE.ChannelAccessToken = secrets["line_access_token"]
	cfg.Channels.WebUI.Enabled = true
	cfg.Channels.WebUI.Token = secrets["webui_token"]
	cfg.Channels.Device.Token = secrets["device_token"]
	cfg.Channels.Device.WordToken = secrets["device_word"]
	cfg.Tools.Web.Brave.APIKey = secrets["brave_key"]
	cfg.Tools.Web.Brave.APIKeys = []string{secrets["brave_key"], secrets["brave_key2"]}
	cfg.Tools.Web.Tavily.APIKey = secrets["tavily_key"]
	cfg.Tools.Web.Tavily.APIKeys = []string{secrets["tavily_key2"]}
	cfg.Tools.Web.Perplexity.APIKey = secrets["perplexity_key"]
	cfg.Tools.Web.Perplexity.APIKeys = []string{secrets["perplexity_key2"]}
	cfg.Tools.Web.GLMSearch.APIKey = secrets["glm_key"]
	cfg.Tools.Web.Proxy = "http://web:" + secrets["web_proxy_pass"] + "@proxy.local:3128"
	cfg.Tools.Skills.Github.Token = secrets["skills_github_token"]
	cfg.Tools.Skills.Github.Proxy = "http://gh:" + secrets["skills_github_proxy_pass"] + "@proxy.local:3128"
	cfg.Tools.Skills.Registries.ClawHub.AuthToken = secrets["clawhub_auth_token"]
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"fusion": {
			Enabled: true, Command: "npx",
			Env:     map[string]string{"FUSION_SECRET": secrets["mcp_env_secret"], "LOG_LEVEL": secrets["mcp_env_plain"]},
			Headers: map[string]string{"Authorization": secrets["mcp_header_auth"]},
		},
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(p)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return p, mux, secrets
}

func getConfigBody(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// TestGetConfig_MasksEverySecret is the regression guard for the leak: the
// endpoint has no operator auth, so its response must not carry credentials.
func TestGetConfig_MasksEverySecret(t *testing.T) {
	_, mux, secrets := secretFixture(t)
	body := getConfigBody(t, mux)

	for name, secret := range secrets {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(body, secret) {
				t.Errorf("GET /api/config leaked %s verbatim (%q)", name, secret)
			}
		})
	}
	if !strings.Contains(body, "****") {
		t.Fatal("response contains no masked values at all — masking did not run")
	}
}

// pathValue walks a decoded JSON document by keys and list indexes.
func pathValue(t *testing.T, doc any, path ...any) any {
	t.Helper()
	cur := doc
	for _, step := range path {
		switch s := step.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				t.Fatalf("at %v: %T is not an object", step, cur)
			}
			cur = m[s]
		case int:
			l, ok := cur.([]any)
			if !ok || s >= len(l) {
				t.Fatalf("at %v: %T is not a list with index %d", step, cur, s)
			}
			cur = l[s]
		}
	}
	return cur
}

// TestGetConfig_MasksWholeEnvHeadersKeyListsAndProxies pins the shapes the
// name-based rule alone missed: every env value (whatever the variable is
// called), every header value, every element of an api_keys list, and the
// userinfo of a proxy URL.
func TestGetConfig_MasksWholeEnvHeadersKeyListsAndProxies(t *testing.T) {
	_, mux, _ := secretFixture(t)
	var doc any
	if err := json.Unmarshal([]byte(getConfigBody(t, mux)), &doc); err != nil {
		t.Fatal(err)
	}

	allMasked := func(name string, v any) {
		t.Helper()
		switch vals := v.(type) {
		case map[string]any:
			if len(vals) == 0 {
				t.Errorf("%s: empty, fixture did not reach it", name)
			}
			for k, val := range vals {
				if s, ok := val.(string); !ok || !isMasked(s) {
					t.Errorf("%s[%s] = %v, want masked", name, k, val)
				}
			}
		case []any:
			if len(vals) == 0 {
				t.Errorf("%s: empty, fixture did not reach it", name)
			}
			for i, val := range vals {
				if s, ok := val.(string); !ok || !isMasked(s) {
					t.Errorf("%s[%d] = %v, want masked", name, i, val)
				}
			}
		default:
			t.Errorf("%s is %T, want map or list", name, v)
		}
	}
	allMasked("models[1].env", pathValue(t, doc, "models", 1, "env"))
	allMasked("tools.mcp.servers.fusion.env", pathValue(t, doc, "tools", "mcp", "servers", "fusion", "env"))
	allMasked("tools.mcp.servers.fusion.headers", pathValue(t, doc, "tools", "mcp", "servers", "fusion", "headers"))
	allMasked("tools.web.brave.api_keys", pathValue(t, doc, "tools", "web", "brave", "api_keys"))
	allMasked("tools.web.tavily.api_keys", pathValue(t, doc, "tools", "web", "tavily", "api_keys"))
	allMasked("tools.web.perplexity.api_keys", pathValue(t, doc, "tools", "web", "perplexity", "api_keys"))

	for name, path := range map[string][]any{
		"providers[0].proxy":         {"providers", 0, "proxy"},
		"channels.telegram[0].proxy": {"channels", "telegram", 0, "proxy"},
		"channels.discord.proxy":     {"channels", "discord", "proxy"},
		"tools.web.proxy":            {"tools", "web", "proxy"},
		"tools.skills.github.proxy":  {"tools", "skills", "github", "proxy"},
	} {
		got, ok := pathValue(t, doc, path...).(string)
		if !ok || !strings.Contains(got, "://****@proxy.local:") {
			t.Errorf("%s = %q, want scheme://****@host", name, got)
		}
	}
}

func TestMaskProxyURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"http://proxy.local:3128", "http://proxy.local:3128"},
		{"http://user:pass@proxy.local:3128", "http://****@proxy.local:3128"},
		{"socks5h://user@proxy.local:1080/", "socks5h://****@proxy.local:1080/"},
		{"https://user:p%40ss@proxy.local:3128?x=1", "https://****@proxy.local:3128?x=1"},
		{"user:pass@proxy.local:3128", "****@proxy.local:3128"},
	}
	for _, tc := range tests {
		if got := maskProxyURL(tc.in); got != tc.want {
			t.Errorf("maskProxyURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestMaskAPIKey_RevealBound: no length reveals more than 7 characters, and
// anything under 12 is hidden whole.
func TestMaskAPIKey_RevealBound(t *testing.T) {
	for n := 1; n <= 64; n++ {
		key := strings.Repeat("k", n)
		got := maskAPIKey(key)
		revealed := len(strings.ReplaceAll(got, "****", ""))
		if n < 12 && got != "****" {
			t.Errorf("len %d: %q, want fully masked", n, got)
		}
		if revealed > 7 {
			t.Errorf("len %d: %q reveals %d characters", n, got, revealed)
		}
	}
}

// TestListProviders_MasksProxyUserinfo covers GET /api/providers, which renders
// providers itself rather than through the config masker, and the PUT that
// follows: a masked proxy sent back keeps the stored URL.
func TestListProviders_MasksProxyUserinfo(t *testing.T) {
	p, mux, secrets := secretFixture(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/providers = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, name := range []string{"provider_proxy_user", "provider_proxy_pass", "provider_api_key"} {
		if strings.Contains(body, secrets[name]) {
			t.Errorf("GET /api/providers leaked %s", name)
		}
	}
	if !strings.Contains(body, `"proxy":"http://****@proxy.local:3128"`) {
		t.Errorf("masked proxy not in body: %s", body)
	}

	put := `{"name":"OpenAI","protocol":"openai-chat","base_url":"https://api.openai.com/v1","api_key":"sk-****ijkl","proxy":"http://****@proxy.local:3128"}`
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/providers/0", bytes.NewReader([]byte(put))))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/providers/0 = %d, body=%s", rec.Code, rec.Body.String())
	}
	cfg, err := config.LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	want := "http://" + secrets["provider_proxy_user"] + ":" + secrets["provider_proxy_pass"] + "@proxy.local:3128"
	if cfg.Providers[0].Proxy != want || cfg.Providers[0].APIKey != secrets["provider_api_key"] {
		t.Errorf("PUT with masked values changed the stored provider: proxy=%q api_key=%q", cfg.Providers[0].Proxy, cfg.Providers[0].APIKey)
	}
}

// TestUnmask_KeyListSurvivesReorderAndDelete: api_keys elements have no
// identity, so a masked element is matched to the stored key it masks to.
func TestUnmask_KeyListSurvivesReorderAndDelete(t *testing.T) {
	p, mux, secrets := secretFixture(t)
	var doc map[string]any
	if err := json.Unmarshal([]byte(getConfigBody(t, mux)), &doc); err != nil {
		t.Fatal(err)
	}
	brave, ok := pathValue(t, doc, "tools", "web", "brave").(map[string]any)
	if !ok {
		t.Fatal("tools.web.brave missing")
	}
	keys, ok := brave["api_keys"].([]any)
	if !ok || len(keys) != 2 {
		t.Fatalf("api_keys = %v, want 2 masked keys", brave["api_keys"])
	}
	// Drop the first key and add a new plaintext one behind the survivor.
	brave["api_keys"] = []any{keys[1], "BSA-brave-brand-new-key-abcdef"}
	patch, err := json.Marshal(map[string]any{"tools": map[string]any{"web": map[string]any{"brave": brave}}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewReader(patch)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, body=%s", rec.Code, rec.Body.String())
	}
	cfg, err := config.LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Tools.Web.Brave.APIKeys
	if len(got) != 2 || got[0] != secrets["brave_key2"] || got[1] != "BSA-brave-brand-new-key-abcdef" {
		t.Fatalf("api_keys after edit = %q, want [brave_key2, new key]", got)
	}
}

// TestConfigRoundTrip_PUT is the property that makes masking safe to ship: a
// client that reads the masked config and writes it back must not destroy the
// credentials it never saw.
func TestConfigRoundTrip_PUT(t *testing.T) {
	p, mux, secrets := secretFixture(t)
	masked := getConfigBody(t, mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader([]byte(masked)))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/config = %d, body=%s", rec.Code, rec.Body.String())
	}

	saved, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for name, secret := range secrets {
		if !strings.Contains(string(saved), secret) {
			t.Errorf("PUT round trip destroyed %s: %q is no longer on disk", name, secret)
		}
	}
	if strings.Contains(string(saved), "****") {
		t.Error("a masked placeholder was written to disk")
	}
}

// TestConfigRoundTrip_PATCH covers the same property on the merge-patch path.
func TestConfigRoundTrip_PATCH(t *testing.T) {
	p, mux, secrets := secretFixture(t)
	masked := getConfigBody(t, mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewReader([]byte(masked)))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/config = %d, body=%s", rec.Code, rec.Body.String())
	}

	saved, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for name, secret := range secrets {
		if !strings.Contains(string(saved), secret) {
			t.Errorf("PATCH round trip destroyed %s", name)
		}
	}
}

// TestConfigUpdate_AcceptsNewSecret checks the other direction: an operator
// setting a real credential must have it written through, not mistaken for a
// mask and reverted.
func TestConfigUpdate_AcceptsNewSecret(t *testing.T) {
	p, mux, _ := secretFixture(t)

	patch := `{"channels":{"discord":{"token":"brand-new-discord-token"}}}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewReader([]byte(patch))))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, body=%s", rec.Code, rec.Body.String())
	}

	cfg, err := config.LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Channels.Discord.Token != "brand-new-discord-token" {
		t.Fatalf("Discord token = %q, want the new value written through", cfg.Channels.Discord.Token)
	}
}

// TestIsSecretKey pins the name matching, including the numeric near-misses.
// chars_per_token ends in "_token" but holds a number, which is why maskSecrets
// also requires a non-empty string value.
func TestIsSecretKey(t *testing.T) {
	for _, k := range []string{
		"api_key", "brave_api_key", "token", "bot_token", "app_token",
		"access_token", "word_token", "auth_token", "channel_access_token",
		"secret", "client_secret", "channel_secret", "password", "password_hash",
	} {
		if !config.IsSecretKey(k) {
			t.Errorf("config.IsSecretKey(%q) = false, want true", k)
		}
	}
	for _, k := range []string{
		"name", "model", "enabled", "session_key", "endpoint_path",
		"max_tokens", "agent_id", "deliver_to", "protocol",
	} {
		if config.IsSecretKey(k) {
			t.Errorf("config.IsSecretKey(%q) = true, want false", k)
		}
	}
}

// TestMaskSecrets_LeavesNonStringsAlone guards the chars_per_token case: the
// key matches the suffix rule but the value is a number, so it must survive.
func TestMaskSecrets_LeavesNonStringsAlone(t *testing.T) {
	m := map[string]any{
		"chars_per_token": 3.5,
		"max_tokens":      4096.0,
		"api_key":         "",
		"token":           "real-secret-value-here",
	}
	maskSecrets(m)

	if m["chars_per_token"] != 3.5 {
		t.Errorf("chars_per_token = %v, want 3.5 untouched", m["chars_per_token"])
	}
	if m["max_tokens"] != 4096.0 {
		t.Errorf("max_tokens = %v, want 4096 untouched", m["max_tokens"])
	}
	if m["api_key"] != "" {
		t.Errorf("empty api_key = %v, want left empty", m["api_key"])
	}
	if m["token"] == "real-secret-value-here" {
		t.Error("token was not masked")
	}
}

// telegramFixture writes a config with two named Telegram bots, the shape the
// WebUI edits as a list.
func telegramFixture(t *testing.T) (string, *http.ServeMux) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Models = []config.ModelConfig{{ModelName: "m", Model: "gpt-4o", Provider: "OpenAI", Enabled: true}}
	cfg.Agents.Defaults.Models = []string{"m"}
	cfg.Channels.Telegram = []config.TelegramBotConfig{
		{ID: "alpha", Enabled: true, Token: "111:ALPHA-TOKEN-SECRET"},
		{ID: "bravo", Enabled: true, Token: "222:BRAVO-TOKEN-SECRET"},
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(p)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return p, mux
}

// maskedTelegramList returns the masked telegram array as the WebUI receives it.
func maskedTelegramList(t *testing.T, mux *http.ServeMux) []any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(getConfigBody(t, mux)), &got); err != nil {
		t.Fatal(err)
	}
	channels, ok := got["channels"].(map[string]any)
	if !ok {
		t.Fatalf("channels is %T, want map[string]any", got["channels"])
	}
	list, ok := channels["telegram"].([]any)
	if !ok {
		t.Fatalf("telegram is %T, want []any", channels["telegram"])
	}
	return list
}

func patchTelegram(t *testing.T, mux *http.ServeMux, list []any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"channels": map[string]any{"telegram": list}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func tokenFor(t *testing.T, path, id string) string {
	t.Helper()
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range cfg.Channels.Telegram {
		if b.ID == id {
			return b.Token
		}
	}
	return ""
}

// TestUnmask_DeletingListEntryKeepsTokensWithTheirOwners is the regression guard
// for a credential swap. The WebUI reads the masked config, edits a list and
// writes the whole list back; matching stored entries by position meant deleting
// the first bot restored the deleted bot's token onto whichever bot inherited
// index 0 — silently, and with a real credential rather than a visible error.
func TestUnmask_DeletingListEntryKeepsTokensWithTheirOwners(t *testing.T) {
	p, mux := telegramFixture(t)
	list := maskedTelegramList(t, mux)

	patchTelegram(t, mux, []any{list[1]}) // drop "alpha", keep "bravo"

	if got := tokenFor(t, p, "bravo"); got != "222:BRAVO-TOKEN-SECRET" {
		t.Fatalf("bravo token = %q, want its own token — a positional match would give it alpha's", got)
	}
	if got := tokenFor(t, p, "alpha"); got != "" {
		t.Fatalf("alpha still present with token %q, want it deleted", got)
	}
}

// TestUnmask_ReorderingListKeepsTokensWithTheirOwners covers the same hazard
// from the other direction.
func TestUnmask_ReorderingListKeepsTokensWithTheirOwners(t *testing.T) {
	p, mux := telegramFixture(t)
	list := maskedTelegramList(t, mux)

	patchTelegram(t, mux, []any{list[1], list[0]}) // swap the order

	if got := tokenFor(t, p, "alpha"); got != "111:ALPHA-TOKEN-SECRET" {
		t.Errorf("alpha token = %q, want its own", got)
	}
	if got := tokenFor(t, p, "bravo"); got != "222:BRAVO-TOKEN-SECRET" {
		t.Errorf("bravo token = %q, want its own", got)
	}
}

// TestUnmask_NewListEntryKeepsItsOwnToken checks that an entry with no stored
// counterpart is written through rather than inheriting a neighbour's secret.
func TestUnmask_NewListEntryKeepsItsOwnToken(t *testing.T) {
	p, mux := telegramFixture(t)
	list := maskedTelegramList(t, mux)

	added := map[string]any{"id": "charlie", "enabled": true, "token": "333:CHARLIE-NEW-TOKEN"}
	patchTelegram(t, mux, []any{added, list[0], list[1]})

	for id, want := range map[string]string{
		"charlie": "333:CHARLIE-NEW-TOKEN",
		"alpha":   "111:ALPHA-TOKEN-SECRET",
		"bravo":   "222:BRAVO-TOKEN-SECRET",
	} {
		if got := tokenFor(t, p, id); got != want {
			t.Errorf("%s token = %q, want %q", id, got, want)
		}
	}
}

// TestUnmask_EditingOneFieldKeepsTheToken is the ordinary WebUI save: change an
// unrelated setting on a bot and its credential must survive.
func TestUnmask_EditingOneFieldKeepsTheToken(t *testing.T) {
	p, mux := telegramFixture(t)
	list := maskedTelegramList(t, mux)

	bot, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("list[0] is %T, want map[string]any", list[0])
	}
	bot["enabled"] = false
	patchTelegram(t, mux, list)

	if got := tokenFor(t, p, "alpha"); got != "111:ALPHA-TOKEN-SECRET" {
		t.Fatalf("alpha token = %q, want it preserved across an unrelated edit", got)
	}
}
