// ClawEh
// License: MIT

package providers

import (
	"bytes"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
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

// A bypass flag in a CLI model's extra_args is never passed to a fresh
// agent's CLI (TestFreshAgent_CLIModelIsolated runs it). Building its provider (several times a turn) logs it once per
// configuration when the provider's own setting ignores it, and not at all
// when the setting is on (the flag is dropped only for the fresh agent).
func TestGetIsolated_BypassFlagLoggedOnce(t *testing.T) {
	for _, bypass := range []bool{false, true} {
		var buf bytes.Buffer
		restore := logger.RedirectForTest(&buf)
		cfg := &config.Config{
			Providers: []config.Provider{{Name: "Claude CLI", Protocol: "claude-cli", BypassRestrictions: bypass}},
			Models: []config.ModelConfig{{
				ModelName: "cc", Model: "claude-cli", Provider: "Claude CLI", Enabled: true,
				ExtraArgs: []string{"--dangerously-skip-permissions"},
			}},
		}
		d := NewProviderDispatcher(cfg)
		for range 3 {
			if _, err := d.GetIsolated("cc", "/tmp/ws"); err != nil {
				t.Fatal(err)
			}
		}
		want := 1
		if bypass {
			want = 0
		}
		if n := strings.Count(buf.String(), "ignoring permission-bypass flag"); n != want {
			t.Errorf("bypass %v: logged %d time(s), want %d:\n%s", bypass, n, want, buf.String())
		}
		d.Flush(cfg)
		buf.Reset()
		if _, err := d.GetIsolated("cc", "/tmp/ws"); err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(buf.String(), "ignoring permission-bypass flag"); n != want {
			t.Errorf("bypass %v: after a reload logged %d time(s), want %d", bypass, n, want)
		}
		restore()
		if cfg.Models[0].ExtraArgs[0] != "--dangerously-skip-permissions" {
			t.Fatal("GetIsolated changed the configuration")
		}
	}
}
