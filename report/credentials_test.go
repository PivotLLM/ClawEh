// ClawEh
// License: MIT

package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/msgtoken"
	"github.com/PivotLLM/ClawEh/servicetoken"
)

func TestCollectCredentials_FreshInstall(t *testing.T) {
	cfg, env := fixtureConfig(t)
	tb := findTable(t, collectCredentials(t.Context(), cfg, env), "Tokens")
	_, svc := findRow(t, tb, "Service tokens")
	if svc[1] != "none issued" {
		t.Errorf("service tokens = %q", svc[1])
	}
	_, integ := findRow(t, tb, "Integration tokens")
	if integ[1] != "none issued" {
		t.Errorf("integration tokens = %q", integ[1])
	}
	_, dev := findRow(t, tb, "Device gateway shared token")
	if dev[1] != "set" {
		t.Errorf("device token = %q", dev[1])
	}
	_, word := findRow(t, tb, "Device gateway word token")
	if word[1] != "set" {
		t.Errorf("word token = %q", word[1])
	}
}

func TestCollectCredentials_StoresCountedNotShown(t *testing.T) {
	cfg, env := fixtureConfig(t)
	svcPath := servicetoken.Path(env.DataDir)
	if err := servicetoken.Save(svcPath, map[string]string{"alice": "SST" + strings.Repeat("ab", 32)}); err != nil {
		t.Fatal(err)
	}
	store, err := msgtoken.NewNamedStore(msgtoken.NamedTokenPath(env.DataDir))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.Create("bob", "gps-tracker")
	if err != nil {
		t.Fatal(err)
	}

	s := collectCredentials(t.Context(), cfg, env)
	tb := findTable(t, s, "Tokens")
	_, svc := findRow(t, tb, "Service token: agent alice")
	if svc[1] != "issued (1)" {
		t.Errorf("service token row = %q", svc[1])
	}
	_, integ := findRow(t, tb, "Integration tokens: agent bob")
	if integ[1] != "1: gps-tracker" {
		t.Errorf("integration row = %q", integ[1])
	}
	txt := RenderText(&Report{Sections: []Section{s}})
	if strings.Contains(txt, tok.Token) || strings.Contains(txt, "SST"+strings.Repeat("ab", 32)) {
		t.Error("a token value leaked into the credentials section")
	}
}

func TestCollectCredentials_UnreadableStoreIsARow(t *testing.T) {
	cfg, env := fixtureConfig(t)
	svcPath := servicetoken.Path(env.DataDir)
	if err := os.MkdirAll(filepath.Dir(svcPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svcPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	tb := findTable(t, collectCredentials(t.Context(), cfg, env), "Tokens")
	_, svc := findRow(t, tb, "Service tokens")
	contains(t, svc[1], "unavailable:", "corrupt service-token store")
}

func TestChannelCredentialRows_NamesOnly(t *testing.T) {
	cfg, _ := fixtureConfig(t)
	txt := tableText(Table{Rows: channelCredentialRows(cfg)})
	contains(t, txt, "Telegram telegram-bob | token set", "telegram")
	contains(t, txt, "Slack | bot_token set, app_token set", "slack")
	contains(t, txt, "LINE | channel_secret set, channel_access_token set", "line")
	contains(t, txt, "Device gateway | token set, word_token set", "device")
	for _, s := range allSecrets {
		if strings.Contains(txt, s) {
			t.Errorf("secret %q leaked into channel credentials", s)
		}
	}
}
