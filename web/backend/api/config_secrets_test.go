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
	secrets := map[string]string{
		"provider_api_key": "sk-provider-key-abcdefghijkl",
		"telegram_token":   "1234567:telegram-bot-token-xyz",
		"discord_token":    "discord-token-abcdefghijkl",
		"webui_token":      "webui-channel-token-abcdef",
		"device_token":     "device-shared-token-abcdef",
		"device_word":      "anchor-velvet-puzzle-ranger-cobalt",
		"brave_key":        "BSA-brave-search-key-abcdef",
	}
	cfg := config.DefaultConfig()
	cfg.Models = []config.ModelConfig{{ModelName: "m", Model: "gpt-4o", Provider: "OpenAI", Enabled: true}}
	cfg.Providers = []config.Provider{{Name: "OpenAI", Protocol: "openai-chat", APIKey: secrets["provider_api_key"]}}
	cfg.Channels.Telegram = []config.TelegramBotConfig{{ID: "b1", Enabled: true, Token: secrets["telegram_token"]}}
	cfg.Channels.Discord.Enabled = true
	cfg.Channels.Discord.Token = secrets["discord_token"]
	cfg.Channels.WebUI.Enabled = true
	cfg.Channels.WebUI.Token = secrets["webui_token"]
	cfg.Channels.Device.Token = secrets["device_token"]
	cfg.Channels.Device.WordToken = secrets["device_word"]
	cfg.Tools.Web.Brave.APIKey = secrets["brave_key"]

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
		if strings.Contains(body, secret) {
			t.Errorf("GET /api/config leaked %s verbatim (%q)", name, secret)
		}
	}
	if !strings.Contains(body, "****") {
		t.Fatal("response contains no masked values at all — masking did not run")
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
		if !isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = false, want true", k)
		}
	}
	for _, k := range []string{
		"name", "model", "enabled", "session_key", "endpoint_path",
		"max_tokens", "agent_id", "deliver_to", "protocol",
	} {
		if isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = true, want false", k)
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
	return got["channels"].(map[string]any)["telegram"].([]any)
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

	list[0].(map[string]any)["enabled"] = false
	patchTelegram(t, mux, list)

	if got := tokenFor(t, p, "alpha"); got != "111:ALPHA-TOKEN-SECRET" {
		t.Fatalf("alpha token = %q, want it preserved across an unrelated edit", got)
	}
}
