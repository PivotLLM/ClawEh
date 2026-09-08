package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// writeConfig persists cfg to a temp file and returns a handler over it.
func writeConfig(t *testing.T, cfg *config.Config) (*Handler, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	return NewHandler(path), path
}

func setCLI(t *testing.T, h *Handler, protocol string, enabled bool) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]bool{"enabled": enabled})
	req := httptest.NewRequest(http.MethodPut, "/api/system/clis/"+protocol, bytes.NewReader(body))
	req.SetPathValue("protocol", protocol)
	rec := httptest.NewRecorder()
	h.handleSetCLIEnabled(rec, req)
	return rec
}

func reload(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The switch has to work from nothing: no provider, no model. That is the case
// that used to require two pages and four undiscoverable flags.
func TestSetCLIEnabled_CreatesProviderAndModelFromNothing(t *testing.T) {
	// Providers exist, but none is a CLI: an absent list would be seeded back
	// to the full catalogue on load, which is right for a fresh install and
	// wrong for this test.
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "OpenAI", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1"}}
	cfg.Models = nil
	h, path := writeConfig(t, cfg)

	if rec := setCLI(t, h, "antigravity-cli", true); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	got := reload(t, path)
	if len(got.Providers) != 2 || got.Providers[1].Protocol != "antigravity-cli" {
		t.Fatalf("providers = %+v", got.Providers)
	}
	// No command: an empty one resolves the binary on PATH and follows the CLI
	// across upgrades, rather than pinning a path a reinstall invalidates.
	if got.Providers[1].Command != "" {
		t.Errorf("command = %q, want empty so PATH resolves it", got.Providers[1].Command)
	}
	if len(got.Models) != 1 {
		t.Fatalf("models = %+v", got.Models)
	}
	m := got.Models[0]
	if !m.Enabled {
		t.Error("the model the switch created is disabled")
	}
	if m.Model != "antigravity-cli" {
		t.Errorf("model id = %q, want the sentinel so the CLI picks its own model", m.Model)
	}
	if m.RequestTimeout != 3600 {
		t.Errorf("request_timeout = %d, want the catalogue's 3600", m.RequestTimeout)
	}
	if m.Provider != got.Providers[1].Name {
		t.Errorf("model provider = %q, provider is named %q", m.Provider, got.Providers[1].Name)
	}
}

// Off means the CLI, not one model of it. A switch carrying the CLI's name that
// left a second model enabled would still route agents to it.
func TestSetCLIEnabled_OffDisablesEveryModelOnTheCLI(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "Claude CLI", Protocol: "claude-cli"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "Claude CLI", Model: "claude-cli", Provider: "Claude CLI", Enabled: true},
		{ModelName: "Claude CLI Opus", Model: "claude-opus-4-7", Provider: "Claude CLI", Enabled: true},
		{ModelName: "Elsewhere", Model: "gpt-5.5", Provider: "OpenAI", Enabled: true},
	}
	cfg.Providers = append(cfg.Providers, config.Provider{Name: "OpenAI", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1"})
	h, path := writeConfig(t, cfg)

	if rec := setCLI(t, h, "claude-cli", false); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	got := reload(t, path)
	for _, m := range got.Models {
		switch m.ModelName {
		case "Claude CLI", "Claude CLI Opus":
			if m.Enabled {
				t.Errorf("%s is still enabled after switching the CLI off", m.ModelName)
			}
		case "Elsewhere":
			if !m.Enabled {
				t.Error("switching a CLI off disabled an unrelated model")
			}
		}
	}
	// Nothing is deleted, so the switch is reversible.
	if len(got.Models) != 3 || len(got.Providers) != 2 {
		t.Errorf("entries were removed: %d models, %d providers", len(got.Models), len(got.Providers))
	}
}

// Turning one back on must find what it left behind rather than pile up a
// second provider and model each time.
func TestSetCLIEnabled_RoundTripsWithoutDuplicating(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "OpenAI", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1"}}
	cfg.Models = nil
	h, path := writeConfig(t, cfg)

	for range 3 {
		if rec := setCLI(t, h, "codex-cli", true); rec.Code != http.StatusOK {
			t.Fatalf("on: %d %s", rec.Code, rec.Body)
		}
		if rec := setCLI(t, h, "codex-cli", false); rec.Code != http.StatusOK {
			t.Fatalf("off: %d %s", rec.Code, rec.Body)
		}
	}
	setCLI(t, h, "codex-cli", true)

	got := reload(t, path)
	if len(got.Providers) != 2 || len(got.Models) != 1 {
		t.Fatalf("after three round trips: %d providers, %d models", len(got.Providers), len(got.Models))
	}
	if !got.Models[0].Enabled {
		t.Error("the final on left the model disabled")
	}
}

// A model someone tuned by hand is enabled in place. A switch is not a reset,
// and silently replacing a configured entry would lose the tuning.
func TestSetCLIEnabled_OnKeepsAnExistingModelsSettings(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "My Claude", Protocol: "claude-cli", Command: "/opt/claude"}}
	cfg.Models = []config.ModelConfig{{
		ModelName: "Tuned", Model: "claude-cli", Provider: "My Claude",
		ContextWindow: 250000, Workspace: "/srv/work", Enabled: false,
	}}
	h, path := writeConfig(t, cfg)

	if rec := setCLI(t, h, "claude-cli", true); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	got := reload(t, path)
	if len(got.Models) != 1 || len(got.Providers) != 1 {
		t.Fatalf("the switch added entries alongside the configured ones: %+v / %+v", got.Providers, got.Models)
	}
	m := got.Models[0]
	if !m.Enabled {
		t.Error("model not enabled")
	}
	if m.ContextWindow != 250000 || m.Workspace != "/srv/work" || m.ModelName != "Tuned" {
		t.Errorf("the switch overwrote hand-set fields: %+v", m)
	}
	if got.Providers[0].Command != "/opt/claude" {
		t.Errorf("the switch overwrote an explicit command: %q", got.Providers[0].Command)
	}
}

// An upgraded install still naming the deprecated gemini-cli protocol must be
// recognised as Antigravity, not treated as a second, unconfigured CLI.
func TestSetCLIEnabled_FindsAGeminiCLIProviderForAntigravity(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "Gemini CLI", Protocol: "gemini-cli"}}
	cfg.Models = []config.ModelConfig{{ModelName: "Gemini CLI", Model: "gemini-2.5-pro", Provider: "Gemini CLI", Enabled: false}}
	h, path := writeConfig(t, cfg)

	if rec := setCLI(t, h, "antigravity-cli", true); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	got := reload(t, path)
	if len(got.Providers) != 1 {
		t.Fatalf("a duplicate provider was created: %+v", got.Providers)
	}
	if len(got.Models) != 1 || !got.Models[0].Enabled {
		t.Fatalf("models = %+v", got.Models)
	}
}

func TestSetCLIEnabled_RejectsAnUnknownProtocol(t *testing.T) {
	h, _ := writeConfig(t, config.DefaultConfig())
	if rec := setCLI(t, h, "openai-chat", true); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a protocol that is not a CLI", rec.Code)
	}
}

// The listing is what the section renders, so it has to report the state the
// switch just wrote.
func TestListCLIs_ReportsConfiguredState(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "Claude CLI", Protocol: "claude-cli"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "a", Model: "claude-cli", Provider: "Claude CLI", Enabled: true},
		{ModelName: "b", Model: "claude-opus-4-7", Provider: "Claude CLI", Enabled: false},
	}
	h, _ := writeConfig(t, cfg)

	rec := httptest.NewRecorder()
	h.handleListCLIs(rec, httptest.NewRequest(http.MethodGet, "/api/system/clis", nil))
	var got []cliInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(config.CLIAgents) {
		t.Fatalf("got %d rows, want one per supported CLI", len(got))
	}
	for _, c := range got {
		switch c.Protocol {
		case "claude-cli":
			if !c.Enabled || !c.Configured || c.Models != 2 || c.ModelsEnabled != 1 {
				t.Errorf("claude row = %+v", c)
			}
		default:
			// Unconfigured CLIs are still listed — greyed out in the UI. A CLI
			// ClawEh supports but has not been set up is a different thing from
			// one it does not support, and hiding it looks like the latter.
			if c.Enabled || c.Configured || c.Models != 0 {
				t.Errorf("%s row = %+v, want unconfigured but present", c.Protocol, c)
			}
		}
	}
}
