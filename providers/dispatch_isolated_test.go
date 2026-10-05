// ClawEh
// License: MIT

package providers

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// GetIsolated: an HTTP model is the shared cached provider; a CLI model is a
// provider of its own in the given workspace, without the declined-tools
// guard (bypass is forced off, so there is no setting to point at), and the
// configuration is left untouched.
func TestGetIsolated(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "Claude CLI", Protocol: "claude-cli", BypassRestrictions: true},
			{Name: "p", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:0/v1", APIKey: "k"},
		},
		Models: []config.ModelConfig{
			{ModelName: "cc", Model: "claude-cli", Provider: "Claude CLI", Enabled: true},
			{ModelName: "http", Model: "gpt", Provider: "p", Enabled: true},
		},
	}
	d := NewProviderDispatcher(cfg)

	shared, err := d.Get("http")
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.GetIsolated("http", "/tmp/ws")
	if err != nil || got != shared {
		t.Fatalf("HTTP model: GetIsolated = %v, %v; want the shared provider", got, err)
	}

	p, err := d.GetIsolated("cc", "/tmp/ws")
	if err != nil {
		t.Fatal(err)
	}
	cli, ok := p.(*ClaudeCliProvider)
	if !ok {
		t.Fatalf("CLI model: got %T, want an unguarded *ClaudeCliProvider", p)
	}
	if cli.Workspace() != "/tmp/ws" {
		t.Fatalf("CLI workspace = %q, want /tmp/ws", cli.Workspace())
	}
	if again, againErr := d.GetIsolated("cc", "/tmp/ws"); againErr != nil || again == p {
		t.Fatal("an isolated CLI provider was cached")
	}
	if !cfg.Providers[0].BypassRestrictions || cfg.Models[0].Workspace != "" {
		t.Fatal("GetIsolated changed the configuration")
	}

	if _, err := d.GetIsolated("cc", " "); err == nil {
		t.Fatal("GetIsolated without a workspace succeeded")
	}
	if _, err := d.GetIsolated("nope", "/tmp/ws"); err == nil {
		t.Fatal("GetIsolated of an unknown model succeeded")
	}
}
