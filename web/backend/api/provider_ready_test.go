package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// The green dot used to mean "a command string is set", which was wrong twice
// over: it lit up for a path that no longer existed, and stayed dark for the
// blank command the seeded config ships with — the case that actually works.
func TestProviderReady_ResolvesTheBinaryRatherThanTrustingTheString(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "agy")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	tests := []struct {
		name     string
		provider config.Provider
		want     bool
	}{
		{
			name:     "blank command finds the protocol default on PATH",
			provider: config.Provider{Protocol: "antigravity-cli"},
			want:     true,
		},
		{
			name:     "a path that does not exist is not ready",
			provider: config.Provider{Protocol: "antigravity-cli", Command: filepath.Join(dir, "gone")},
			want:     false,
		},
		{
			name:     "an explicit path that exists is ready",
			provider: config.Provider{Protocol: "antigravity-cli", Command: real},
			want:     true,
		},
		{
			name:     "the deprecated gemini-cli alias resolves to agy",
			provider: config.Provider{Protocol: "gemini-cli"},
			want:     true,
		},
		{
			name:     "a CLI whose binary is absent is not ready",
			provider: config.Provider{Protocol: "codex-cli"},
			want:     false,
		},
		{
			name:     "an HTTP provider is ready when it has a key",
			provider: config.Provider{Protocol: "openai-chat", APIKey: "sk-x"},
			want:     true,
		},
		{
			name:     "an HTTP provider without a key is not",
			provider: config.Provider{Protocol: "openai-chat"},
			want:     false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerReady(&tc.provider); got != tc.want {
				t.Errorf("providerReady = %v, want %v", got, tc.want)
			}
		})
	}
}

// A blank command still has to tell the operator which binary will run, or the
// card shows a green dot next to nothing.
func TestResolvedCLICommand_NamesTheBinaryABlankCommandWillRun(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "claude")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	if got := resolvedCLICommand(&config.Provider{Protocol: "claude-cli"}); got != real {
		t.Errorf("resolved command = %q, want %q", got, real)
	}
	// HTTP providers have no binary, and an unresolvable CLI must not invent one.
	if got := resolvedCLICommand(&config.Provider{Protocol: "openai-chat", APIKey: "k"}); got != "" {
		t.Errorf("HTTP provider resolved to %q, want empty", got)
	}
	if got := resolvedCLICommand(&config.Provider{Protocol: "cursor-cli"}); got != "" {
		t.Errorf("missing binary resolved to %q, want empty", got)
	}
}

// Every CLI protocol the config accepts must resolve to a default binary,
// otherwise adding that provider and leaving the path blank silently reports it
// as unusable.
func TestDefaultCLIBinary_CoversEveryAcceptedCLIProtocol(t *testing.T) {
	for _, protocol := range []string{"claude-cli", "codex-cli", "antigravity-cli", "cursor-cli", "gemini-cli"} {
		if !config.IsCLIProtocol(protocol) {
			t.Fatalf("%s is no longer an accepted CLI protocol; update this test", protocol)
		}
		if defaultCLIBinary(protocol) == "" {
			t.Errorf("%s has no default binary", protocol)
		}
	}
}
