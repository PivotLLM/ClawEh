// ClawEh - Personal AI Assistant
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigFile writes a JSON config to a temp file and returns its path.
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestOpenAICompatProtocols_EmptyIsNoOp(t *testing.T) {
	// Hardcoded protocols (e.g. xai) must keep working when the map is empty.
	path := writeConfigFile(t, `{
		"agents": {"defaults": {"model": "grok"}},
		"model_list": [
			{"model_name": "grok", "model": "xai/grok-4", "api_key": "k", "enabled": true}
		]
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.OpenAICompatProtocols) != 0 {
		t.Errorf("OpenAICompatProtocols = %v, want empty", cfg.OpenAICompatProtocols)
	}
	mc := cfg.ModelList[0]
	if mc.IsOpenAICompatExtra() {
		t.Errorf("hardcoded xai protocol incorrectly tagged as extra")
	}
	if mc.OpenAICompatBase() != "" {
		t.Errorf("OpenAICompatBase = %q, want empty for hardcoded protocol", mc.OpenAICompatBase())
	}
}

func TestOpenAICompatProtocols_DefaultAPIBaseResolved(t *testing.T) {
	const wantBase = "https://api.myvendor.example/v1"
	path := writeConfigFile(t, `{
		"agents": {"defaults": {"model": "mv"}},
		"openai_compat_protocols": {
			"myvendor": "`+wantBase+`"
		},
		"model_list": [
			{"model_name": "mv", "model": "myvendor/foo-1", "api_key": "k", "enabled": true}
		]
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	mc := cfg.ModelList[0]
	if !mc.IsOpenAICompatExtra() {
		t.Fatal("expected ModelConfig to be tagged as openai-compat extra")
	}
	if mc.OpenAICompatBase() != wantBase {
		t.Errorf("OpenAICompatBase = %q, want %q", mc.OpenAICompatBase(), wantBase)
	}
	// Per-model api_base is empty here, so the factory will fall back to the
	// registered default. Mutation guard: if resolveOpenAICompatProtocols
	// stops copying the base, this assertion fails.
	if mc.APIBase != "" {
		t.Errorf("APIBase = %q, want empty (resolver must not write to APIBase)", mc.APIBase)
	}
}

func TestOpenAICompatProtocols_PerModelOverrideWinsOverDefault(t *testing.T) {
	const defaultBase = "https://default.myvendor.example/v1"
	const override = "https://override.myvendor.example/v2"
	path := writeConfigFile(t, `{
		"agents": {"defaults": {"model": "mv"}},
		"openai_compat_protocols": {
			"myvendor": "`+defaultBase+`"
		},
		"model_list": [
			{"model_name": "mv", "model": "myvendor/foo-1", "api_base": "`+override+`", "api_key": "k", "enabled": true}
		]
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	mc := cfg.ModelList[0]
	if !mc.IsOpenAICompatExtra() {
		t.Fatal("expected ModelConfig to be tagged as openai-compat extra")
	}
	if mc.OpenAICompatBase() != defaultBase {
		t.Errorf("OpenAICompatBase = %q, want %q (registered default still recorded)", mc.OpenAICompatBase(), defaultBase)
	}
	if mc.APIBase != override {
		t.Errorf("APIBase = %q, want %q (per-model override preserved)", mc.APIBase, override)
	}
}

func TestOpenAICompatProtocols_EmptyDefaultRequiresPerModelAPIBase(t *testing.T) {
	// An operator can register a protocol without a default api_base; each
	// model must then provide its own.
	path := writeConfigFile(t, `{
		"agents": {"defaults": {"model": "mv"}},
		"openai_compat_protocols": {"myvendor": ""},
		"model_list": [
			{"model_name": "mv", "model": "myvendor/foo-1", "api_base": "https://m.example/v1", "api_key": "k", "enabled": true}
		]
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	mc := cfg.ModelList[0]
	if !mc.IsOpenAICompatExtra() {
		t.Fatal("expected ModelConfig to be tagged as openai-compat extra")
	}
	if mc.OpenAICompatBase() != "" {
		t.Errorf("OpenAICompatBase = %q, want empty", mc.OpenAICompatBase())
	}
}

func TestOpenAICompatProtocols_ReservedNamesRejected(t *testing.T) {
	cases := []string{
		"openai", "anthropic", "anthropic-messages", "azure", "azure-openai",
		"bedrock", "claude-cli", "claudecli", "codex-cli", "codexcli",
		"gemini-cli", "geminicli",
		// also includes hardcoded openai-compat entries; allowing duplicates
		// would mask drift between the config-driven default and the
		// hardcoded one.
		"litellm", "openrouter", "groq", "gemini", "nvidia", "ollama",
		"moonshot", "deepseek", "cerebras", "vllm", "qwen", "mistral",
		"avian", "xai",
	}
	for _, proto := range cases {
		t.Run(proto, func(t *testing.T) {
			cfg := &Config{
				OpenAICompatProtocols: map[string]string{
					proto: "https://example.com/v1",
				},
			}
			err := cfg.ValidateOpenAICompatProtocols()
			if err == nil {
				t.Fatalf("expected validation error for reserved protocol %q", proto)
			}
			if !strings.Contains(err.Error(), proto) {
				t.Errorf("error should name the offending protocol: %v", err)
			}
			if !strings.Contains(err.Error(), "reserved") {
				t.Errorf("error should mention 'reserved': %v", err)
			}
		})
	}
}

func TestOpenAICompatProtocols_ReservedCaseInsensitive(t *testing.T) {
	cfg := &Config{
		OpenAICompatProtocols: map[string]string{"XAI": "https://example.com/v1"},
	}
	if err := cfg.ValidateOpenAICompatProtocols(); err == nil {
		t.Fatal("expected validation error for case-variant reserved protocol")
	}
}

func TestOpenAICompatProtocols_MalformedNamesRejected(t *testing.T) {
	for _, name := range []string{"", " ", "a/b", "has space"} {
		cfg := &Config{
			OpenAICompatProtocols: map[string]string{name: "https://example.com/v1"},
		}
		if err := cfg.ValidateOpenAICompatProtocols(); err == nil {
			t.Errorf("expected validation error for malformed protocol %q", name)
		}
	}
}

func TestOpenAICompatProtocols_AcceptedThroughLoadConfig(t *testing.T) {
	path := writeConfigFile(t, `{
		"agents": {"defaults": {"model": "mv"}},
		"openai_compat_protocols": {
			"myvendor": "https://api.myvendor.example/v1",
			"someorg":  "https://api.someorg.example/openai/v1"
		},
		"model_list": [
			{"model_name": "mv", "model": "myvendor/foo-1", "api_key": "k", "enabled": true},
			{"model_name": "so", "model": "someorg/bar-2",  "api_key": "k", "enabled": true}
		]
	}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.OpenAICompatProtocols["myvendor"]; got != "https://api.myvendor.example/v1" {
		t.Errorf("myvendor base = %q", got)
	}
	if !cfg.ModelList[0].IsOpenAICompatExtra() || !cfg.ModelList[1].IsOpenAICompatExtra() {
		t.Errorf("expected both entries tagged as openai-compat extras: %+v", cfg.ModelList)
	}
}

func TestOpenAICompatProtocols_ReservedConflictFailsLoadConfig(t *testing.T) {
	path := writeConfigFile(t, `{
		"agents": {"defaults": {"model": "mv"}},
		"openai_compat_protocols": {"anthropic": "https://api.evil.example/v1"},
		"model_list": [
			{"model_name": "mv", "model": "openai/gpt-4o", "api_key": "k", "enabled": true}
		]
	}`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig should reject openai_compat_protocols entry that collides with a reserved protocol")
	}
	if !strings.Contains(err.Error(), "anthropic") {
		t.Errorf("error should name the offending protocol: %v", err)
	}
}

func TestIsReservedProtocol(t *testing.T) {
	for _, name := range []string{"openai", "anthropic", "xai", "claude-cli"} {
		if !IsReservedProtocol(name) {
			t.Errorf("IsReservedProtocol(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"myvendor", "someorg", ""} {
		if IsReservedProtocol(name) {
			t.Errorf("IsReservedProtocol(%q) = true, want false", name)
		}
	}
}
