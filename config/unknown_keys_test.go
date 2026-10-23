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
			"discovery": {"ttl": 3},
			"subagent": {"enabled": true}
		},
		"gateway": {"tls": {"cert_file": "/c", "key_file": "/k", "certfile": "typo"}},
		"channels": {"line": {"enabled": true}},
		"top_level_typo": 1
	}`))
	if err != nil {
		t.Fatal(err)
	}
	got := unknownConfigKeys(doc)
	want := []string{
		"agents.defaults.worksapce",
		"agents.list[0].colour",
		"channels.line",
		"gateway.tls.certfile",
		"providers[0].apikey",
		"tools.mcp.servers.fusion.comand",
		"tools.subagent",
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

// TestUnknownConfigKeys_RemovedSettings: settings removed because they never
// had an effect are reported when an existing config.json still has them.
func TestUnknownConfigKeys_RemovedSettings(t *testing.T) {
	doc, err := decodeDocument([]byte(`{
		"agents": {"defaults": {"image_model": "m", "image_model_fallbacks": ["m"]}},
		"models": [{"model_name": "m", "model": "m", "provider": "p", "rpm": 60, "connect_mode": "stdio"}],
		"tools": {
			"web": {"enabled": true, "proxy": "http://proxy:3128"},
			"exec": {"enabled": true, "timeout_seconds": 60},
			"skills": {"local": {"enabled": true}},
			"read_file": {"enabled": true, "max_read_file_size": 1024}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	got := unknownConfigKeys(doc)
	want := []string{
		"agents.defaults.image_model",
		"agents.defaults.image_model_fallbacks",
		"models[0].connect_mode",
		"models[0].rpm",
		"tools.exec.enabled",
		"tools.read_file.enabled",
		"tools.skills.local",
		"tools.web.enabled",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unknownConfigKeys = %v, want %v", got, want)
	}
}
