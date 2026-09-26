// ClawEh
// License: MIT

package config

import (
	"reflect"
	"testing"
)

func TestUnknownConfigKeys(t *testing.T) {
	doc, err := decodeDocument([]byte(`{
		"agents": {"defaults": {"restrict_to_workspace": true, "worksapce": "typo"}, "list": [{"id": "main", "colour": "blue"}]},
		"providers": [{"name": "openai", "protocol": "openai-chat", "api_key": "x", "apikey": "wrong"}],
		"tools": {
			"mcp": {"servers": {"fusion": {"command": "npx", "comand": "typo"}}},
			"discovery": {"ttl": 3}
		},
		"gateway": {"tls": {"cert_file": "/c", "key_file": "/k", "certfile": "typo"}},
		"top_level_typo": 1
	}`))
	if err != nil {
		t.Fatal(err)
	}
	got := unknownConfigKeys(doc)
	want := []string{
		"agents.defaults.worksapce",
		"agents.list[0].colour",
		"gateway.tls.certfile",
		"providers[0].apikey",
		"tools.mcp.servers.fusion.comand",
		"top_level_typo",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unknownConfigKeys = %v, want %v", got, want)
	}
}

func TestUnknownConfigKeys_KnownKeysAreClean(t *testing.T) {
	// A config the program wrote itself must not produce a single warning.
	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	cfg := DefaultConfig()
	data, err := MarshalWithSecretRefs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodeDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := unknownConfigKeys(doc); len(got) != 0 {
		t.Fatalf("DefaultConfig round trip reports unknown keys: %v", got)
	}
}

func TestLoadConfig_UnknownKeyDoesNotFailLoad(t *testing.T) {
	p := writeSecretConfig(t, `{"gateway": {"prot": 1234}, "nonsense": true}`)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Gateway.Port != DefaultGatewayPort {
		t.Fatalf("gateway.port = %d, want the default (the typo must be ignored)", cfg.Gateway.Port)
	}
}
