// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeSecretConfig writes body as config.json in a private temp data dir and
// returns its path.
func writeSecretConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

func readConfigDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("config.json is not JSON: %v", err)
	}
	return doc
}

func providerAPIKeyOnDisk(t *testing.T, path string, idx int) string {
	t.Helper()
	doc := readConfigDoc(t, path)
	providers, ok := doc["providers"].([]any)
	if !ok || idx >= len(providers) {
		t.Fatalf("providers[%d] missing on disk", idx)
	}
	p, ok := providers[idx].(map[string]any)
	if !ok {
		t.Fatalf("providers[%d] on disk is %T, want an object", idx, providers[idx])
	}
	s, ok := p["api_key"].(string)
	if !ok {
		t.Fatalf("providers[%d].api_key on disk is %T, want a string", idx, p["api_key"])
	}
	return s
}

func TestLoadConfig_ResolvesEnvAndFileReferences(t *testing.T) {
	t.Setenv("CLAW_TEST_OPENAI_KEY", "sk-from-env-0123456789")
	keyFile := filepath.Join(t.TempDir(), "brave.key")
	if err := os.WriteFile(keyFile, []byte("  BSA-from-file-0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := writeSecretConfig(t, `{
		"providers": [{"name": "openai", "protocol": "openai-chat", "base_url": "https://api.openai.com/v1", "api_key": "env:CLAW_TEST_OPENAI_KEY"}],
		"models": [{"model_name": "m", "model": "gpt-4o", "provider": "openai", "enabled": true}],
		"agents": {"list": [{"id": "main", "name": "Main", "default": true}]},
		"tools": {"web": {"brave": {"api_key": "file:`+keyFile+`"}}}
	}`)

	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Providers[0].APIKey; got != "sk-from-env-0123456789" {
		t.Fatalf("providers[0].api_key = %q, want the env value", got)
	}
	if got := cfg.Tools.Web.Brave.APIKey; got != "BSA-from-file-0123456789" {
		t.Fatalf("tools.web.brave.api_key = %q, want the trimmed file contents", got)
	}
	paths := cfg.SecretRefPaths()
	if len(paths) != 2 || paths[0] != "providers[0].api_key" && paths[1] != "providers[0].api_key" {
		t.Fatalf("SecretRefPaths = %v, want providers[0].api_key and tools.web.brave.api_key", paths)
	}

	// A save writes the references back, not the resolved values.
	if err = SaveConfig(p, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-from-env") || strings.Contains(string(raw), "BSA-from-file") {
		t.Fatalf("resolved secret written to disk:\n%s", raw)
	}
	if got := providerAPIKeyOnDisk(t, p, 0); got != "env:CLAW_TEST_OPENAI_KEY" {
		t.Fatalf("api_key on disk = %q, want the reference", got)
	}

	// And the file still loads to the same values.
	again, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig after save: %v", err)
	}
	if again.Providers[0].APIKey != "sk-from-env-0123456789" || again.Tools.Web.Brave.APIKey != "BSA-from-file-0123456789" {
		t.Fatal("reloaded config lost a resolved reference")
	}
}

func TestSaveConfig_NewLiteralReplacesReference(t *testing.T) {
	t.Setenv("CLAW_TEST_KEY", "old-secret-value-0123456789")
	p := writeSecretConfig(t, `{"providers": [{"name": "openai", "protocol": "openai-chat", "api_key": "env:CLAW_TEST_KEY"}]}`)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.Providers[0].APIKey = "sk-new-literal-0123456789"
	if err := SaveConfig(p, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if got := providerAPIKeyOnDisk(t, p, 0); got != "sk-new-literal-0123456789" {
		t.Fatalf("api_key on disk = %q, want the new literal (operator replaced the reference)", got)
	}
}

func TestSaveConfig_ReferenceFollowsValueWhenIndexShifts(t *testing.T) {
	t.Setenv("CLAW_TEST_KEY_B", "second-secret-value-0123456789")
	p := writeSecretConfig(t, `{"providers": [
		{"name": "a", "protocol": "openai-chat", "api_key": "literal-a-0123456789"},
		{"name": "b", "protocol": "openai-chat", "api_key": "env:CLAW_TEST_KEY_B"}]}`)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.Providers = cfg.Providers[1:] // delete "a": "b" moves to index 0
	if err := SaveConfig(p, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if got := providerAPIKeyOnDisk(t, p, 0); got != "env:CLAW_TEST_KEY_B" {
		t.Fatalf("api_key on disk = %q, want the reference to follow its value to the new index", got)
	}
}

func TestLoadConfig_MissingEnvReferenceNamesKey(t *testing.T) {
	if err := os.Unsetenv("CLAW_TEST_MISSING_KEY"); err != nil {
		t.Fatal(err)
	}
	p := writeSecretConfig(t, `{"channels": {"discord": {"token": "env:CLAW_TEST_MISSING_KEY"}}}`)
	_, err := LoadConfig(p)
	if err == nil {
		t.Fatal("LoadConfig succeeded with an unset env reference")
	}
	for _, want := range []string{"channels.discord.token", "CLAW_TEST_MISSING_KEY", "not set"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadConfig_LooseSecretFileIsRefusedWithChmodFix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not checked on windows")
	}
	keyFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(keyFile, []byte("tok"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := writeSecretConfig(t, `{"channels": {"telegram": [{"id": "b1", "token": "file:`+keyFile+`"}]}}`)
	_, err := LoadConfig(p)
	if err == nil {
		t.Fatal("LoadConfig accepted a group/other-readable secret file")
	}
	for _, want := range []string{"channels.telegram[0].token", "chmod 600 " + keyFile} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadConfig_FileReferenceMustBeAbsolute(t *testing.T) {
	p := writeSecretConfig(t, `{"channels": {"discord": {"token": "file:relative/token"}}}`)
	if _, err := LoadConfig(p); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("LoadConfig error = %v, want an absolute-path complaint", err)
	}
}

func TestLoadConfig_ReferencesInListsMapsAndProxies(t *testing.T) {
	t.Setenv("K1", "rotation-key-one-0123456789")
	t.Setenv("K2", "rotation-key-two-0123456789")
	t.Setenv("MCP_TOKEN", "mcp-env-token-0123456789")
	t.Setenv("PROXY_URL", "http://user:pw@proxy.local:3128")
	p := writeSecretConfig(t, `{
		"tools": {
			"web": {"brave": {"api_keys": ["env:K1", "env:K2"]}, "proxy": "env:PROXY_URL"},
			"mcp": {"servers": {"fusion": {"command": "npx", "env": {"TOKEN": "env:MCP_TOKEN", "LOG": "debug"}}}}
		}
	}`)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Tools.Web.Brave.APIKeys; len(got) != 2 || got[0] != "rotation-key-one-0123456789" || got[1] != "rotation-key-two-0123456789" {
		t.Fatalf("api_keys = %v", got)
	}
	if got := cfg.Tools.Web.Proxy; got != "http://user:pw@proxy.local:3128" {
		t.Fatalf("proxy = %q", got)
	}
	if got := cfg.Tools.MCP.Servers["fusion"].Env["TOKEN"]; got != "mcp-env-token-0123456789" {
		t.Fatalf("mcp env TOKEN = %q", got)
	}
	if got := cfg.Tools.MCP.Servers["fusion"].Env["LOG"]; got != "debug" {
		t.Fatalf("mcp env LOG = %q, a literal must pass through", got)
	}
	if err = SaveConfig(p, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{`"env:K1"`, `"env:K2"`, `"env:MCP_TOKEN"`, `"env:PROXY_URL"`} {
		if !strings.Contains(string(raw), ref) {
			t.Fatalf("saved config lost %s:\n%s", ref, raw)
		}
	}
	if strings.Contains(string(raw), "rotation-key") || strings.Contains(string(raw), "mcp-env-token") {
		t.Fatalf("saved config carries a resolved value:\n%s", raw)
	}
}

func TestLoadConfig_ReferenceInNonSecretFieldIsLiteral(t *testing.T) {
	p := writeSecretConfig(t, `{"providers": [{"name": "env:NOT_A_SECRET", "protocol": "openai-chat", "base_url": "env:ALSO_NOT"}]}`)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Providers[0].Name != "env:NOT_A_SECRET" || cfg.Providers[0].BaseURL != "env:ALSO_NOT" {
		t.Fatal("a reference-looking string in a non-secret field must be left alone")
	}
}

func TestProbePass_PreservesLargeIntegers(t *testing.T) {
	// The probe pass re-encodes the document when it resolves a reference; a
	// 64-bit id must survive that round trip byte for byte.
	t.Setenv("TG", "tg-token-0123456789")
	doc, err := decodeDocument([]byte(`{"channels": {"telegram": [{"id": "b1", "token": "env:TG", "allow_from": [9007199254740993]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resolveSecretRefs(doc); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("re-encoded document lost integer precision: %s", out)
	}
	if !strings.Contains(string(out), `"token":"tg-token-0123456789"`) {
		t.Fatalf("reference not resolved in document: %s", out)
	}
}

func TestMarshalWithSecretRefs_ShowsReferences(t *testing.T) {
	t.Setenv("CLAW_TEST_KEY", "secret-value-0123456789")
	p := writeSecretConfig(t, `{"providers": [{"name": "openai", "protocol": "openai-chat", "api_key": "env:CLAW_TEST_KEY"}]}`)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalWithSecretRefs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"api_key":"env:CLAW_TEST_KEY"`) || strings.Contains(string(out), "secret-value") {
		t.Fatalf("MarshalWithSecretRefs = %s", out)
	}
}
