// ClawEh - allowlisted environment for child processes
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package childenv

import (
	"slices"
	"strings"
	"testing"
)

// lookup returns the value of k in env and whether it is present.
func lookup(env []string, k string) (string, bool) {
	for _, kv := range env {
		if strings.HasPrefix(kv, k+"=") {
			return kv[len(k)+1:], true
		}
	}
	return "", false
}

func TestBase_DropsSecretsKeepsBasics(t *testing.T) {
	t.Setenv("CLAW_HOME", "/srv/claw")
	t.Setenv("CLAW_GATEWAY_TOKEN", "secret")
	t.Setenv("ALERTER_PUSHOVER_TOKEN", "secret")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	t.Setenv("RANDOM_OTHER", "x")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/alice")
	t.Setenv("LANG", "en_CA.UTF-8")
	t.Setenv("LC_ALL", "C")
	t.Setenv("XDG_CONFIG_HOME", "/home/alice/.config")
	t.Setenv("NVM_DIR", "/home/alice/.nvm")
	t.Setenv("NODE_OPTIONS", "--max-old-space-size=4096")
	t.Setenv("npm_config_registry", "https://registry.example")
	t.Setenv("https_proxy", "http://proxy:3128")

	env := Base()

	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAW_") || strings.HasPrefix(kv, "ALERTER_") {
			t.Errorf("Base() leaked %q", kv)
		}
	}
	for _, dropped := range []string{"ANTHROPIC_API_KEY", "RANDOM_OTHER"} {
		if _, ok := lookup(env, dropped); ok {
			t.Errorf("Base() kept %s, want it dropped", dropped)
		}
	}
	want := map[string]string{
		"PATH":                "/usr/bin:/bin",
		"HOME":                "/home/alice",
		"LANG":                "en_CA.UTF-8",
		"LC_ALL":              "C",
		"XDG_CONFIG_HOME":     "/home/alice/.config",
		"NVM_DIR":             "/home/alice/.nvm",
		"NODE_OPTIONS":        "--max-old-space-size=4096",
		"npm_config_registry": "https://registry.example",
		"https_proxy":         "http://proxy:3128",
	}
	for k, v := range want {
		got, ok := lookup(env, k)
		if !ok {
			t.Errorf("Base() dropped %s", k)
			continue
		}
		if got != v {
			t.Errorf("Base() %s = %q, want %q", k, got, v)
		}
	}
	if !slices.IsSorted(env) {
		t.Errorf("Base() is not sorted: %v", env)
	}
}

func TestCLI_AddsCLIAuthOnly(t *testing.T) {
	t.Setenv("CLAW_GATEWAY_TOKEN", "secret")
	t.Setenv("ALERTER_SMTP_PASSWORD", "secret")
	t.Setenv("ANTHROPIC_API_KEY", "k1")
	t.Setenv("OPENAI_API_KEY", "k2")
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/alice/.claude-claw")
	t.Setenv("CODEX_HOME", "/home/alice/.codex")
	t.Setenv("GEMINI_API_KEY", "k3")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "p")
	t.Setenv("CURSOR_API_KEY", "k4")
	t.Setenv("AGY_HOME", "/home/alice/.agy")
	t.Setenv("ANTIGRAVITY_HOME", "/home/alice/.antigravity")
	t.Setenv("GITHUB_TOKEN", "secret")

	env := CLI()

	for _, k := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CLAUDE_CONFIG_DIR", "CODEX_HOME",
		"GEMINI_API_KEY", "GOOGLE_CLOUD_PROJECT", "CURSOR_API_KEY", "AGY_HOME", "ANTIGRAVITY_HOME",
	} {
		if _, ok := lookup(env, k); !ok {
			t.Errorf("CLI() dropped %s", k)
		}
	}
	for _, k := range []string{"CLAW_GATEWAY_TOKEN", "ALERTER_SMTP_PASSWORD", "GITHUB_TOKEN"} {
		if _, ok := lookup(env, k); ok {
			t.Errorf("CLI() kept %s, want it dropped", k)
		}
	}

	// Base must not carry the CLI keys.
	if _, ok := lookup(Base(), "ANTHROPIC_API_KEY"); ok {
		t.Error("Base() kept ANTHROPIC_API_KEY; only CLI() may")
	}
}

func TestMerge_OverlayWins(t *testing.T) {
	base := []string{"A=base", "PATH=/bin", "Z=keep"}
	got := Merge(base, map[string]string{"PATH": "/opt/bin", "B": "new", "A": "extra"})

	want := []string{"Z=keep", "A=extra", "B=new", "PATH=/opt/bin"}
	if !slices.Equal(got, want) {
		t.Errorf("Merge() = %v, want %v", got, want)
	}
	if !slices.Equal(base, []string{"A=base", "PATH=/bin", "Z=keep"}) {
		t.Errorf("Merge() modified base: %v", base)
	}
	if got, ok := lookup(got, "PATH"); !ok || got != "/opt/bin" {
		t.Errorf("PATH = %q, want /opt/bin", got)
	}
}

func TestMerge_EmptyExtraCopiesBase(t *testing.T) {
	base := []string{"A=1"}
	got := Merge(base, nil)
	if !slices.Equal(got, base) {
		t.Errorf("Merge(base, nil) = %v, want %v", got, base)
	}
	got[0] = "A=2"
	if base[0] != "A=1" {
		t.Error("Merge() returned base's backing array")
	}
}
