// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// UnknownPatchKeys reports what the load warning reports plus the silently
// removed keys; the old top-level permission keys are plain unknown keys;
// free-form maps take any key.
func TestUnknownPatchKeys(t *testing.T) {
	doc := map[string]any{
		"nonsense": 1,
		"tools": map[string]any{
			"discovery": map[string]any{"ttl": 5},
			"mcp": map[string]any{"servers": map[string]any{"bob": map[string]any{
				"env": map[string]any{"ANY": "1"}, "headers": map[string]any{"X-Any": "1"},
			}}},
		},
		"security": map[string]any{"permissions": map[string]any{"dir_mode": "0750", "logs": map[string]any{"file_mode": "0640"}}},
	}
	want := []string{"nonsense", "security.permissions.dir_mode", "tools.discovery.ttl"}
	if got := UnknownPatchKeys(doc); !slices.Equal(got, want) {
		t.Errorf("UnknownPatchKeys = %v, want %v", got, want)
	}
	if got := unknownConfigKeys(any(doc)); !slices.Equal(got, []string{"nonsense", "security.permissions.dir_mode"}) {
		t.Errorf("unknownConfigKeys = %v, want nonsense and security.permissions.dir_mode", got)
	}
	if got := unknownPermissionKeys(any(doc)); !slices.Equal(got, []string{"security.permissions.dir_mode"}) {
		t.Errorf("unknownPermissionKeys = %v", got)
	}
}

// Everything GET /api/config returns may be sent back in a PATCH. A fully
// populated config refuses only the keys kept just so a load can migrate
// them, and a load clears those, so a loaded config refuses nothing.
func TestUnknownPatchKeys_FullConfigAccepted(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "marshal_full.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, 18)
	for _, scope := range []string{"agents.defaults.", "agents.list[0]."} {
		for _, k := range []string{
			"compress_chars_per_token", "compress_message_threshold", "compress_min_percent",
			"compress_normal_percent", "compress_retain_min_messages", "compress_retain_token_percent",
			"compress_safety_percent", "compress_token_safety_margin",
		} {
			want = append(want, scope+k)
		}
	}
	want = append(want, "agents.list[0].mounts", "channels.discord.mention_only")
	slices.Sort(want)
	if got := UnknownPatchKeys(doc); !slices.Equal(got, want) {
		t.Errorf("UnknownPatchKeys on a full config = %v, want exactly the migration-only keys %v", got, want)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"agents":{"defaults":{"compress_min_percent":40},"list":[{"id":"alice","compress_retain_min_messages":4}]},` +
		`"channels":{"discord":{"mention_only":true}}}`
	if err = os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Channels.Discord.GroupTrigger.MentionOnly || cfg.Agents.Defaults.Compression == nil ||
		cfg.Agents.Defaults.Compression.Trigger.MinPercent == nil || *cfg.Agents.Defaults.Compression.Trigger.MinPercent != 40 {
		t.Fatal("the migration-only keys were not migrated at load")
	}
	out, err := MarshalWithSecretRefs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var loaded map[string]any
	if err = json.Unmarshal(out, &loaded); err != nil {
		t.Fatal(err)
	}
	if got := UnknownPatchKeys(loaded); len(got) != 0 {
		t.Errorf("UnknownPatchKeys on a loaded config = %v, want none", got)
	}
}
