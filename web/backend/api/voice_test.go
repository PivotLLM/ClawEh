// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// setupVoiceEnv writes a config with a mix of providers: OpenAI-compatible
// providers with endpoints (one without a key), a CLI provider, and an
// anthropic and an azure provider, which cannot transcribe.
func setupVoiceEnv(t *testing.T, stt []config.STTProvider) (string, *http.ServeMux, *int) {
	t.Helper()
	configPath := setupTestEnv(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers = append(cfg.Providers,
		config.Provider{Name: "groq", Protocol: "openai-chat", BaseURL: "https://api.groq.com/openai/v1"},
		config.Provider{Name: "my-openrouter", Protocol: "openai-chat", BaseURL: "https://openrouter.ai/api/v1", APIKey: "sk-or-secret"},
		config.Provider{Name: "Claude CLI", Protocol: "claude-cli"},
		config.Provider{Name: "anthropic-api", Protocol: "anthropic", BaseURL: "https://api.anthropic.com/v1", APIKey: "sk-ant"},
		config.Provider{Name: "azure-api", Protocol: "azure", BaseURL: "https://example.openai.azure.com", APIKey: "sk-az"},
	)
	cfg.Voice.STT = stt
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	reloads := 0
	h := NewHandler(configPath)
	h.SetReloadTrigger(func() error { reloads++; return nil })
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return configPath, mux, &reloads
}

func serveVoice(mux *http.ServeMux, method, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/voice/stt", strings.NewReader(body)))
	return rec
}

// GET lists the entries and every provider that can transcribe (an
// OpenAI-compatible one with an endpoint), in config order, with its default
// model and whether it has a key, and never a key. CLI, anthropic and azure
// providers are left out.
func TestHandleGetVoiceSTT(t *testing.T) {
	_, mux, _ := setupVoiceEnv(t, []config.STTProvider{{Provider: "my-openrouter", Enabled: true, Model: "openai/whisper-1"}})

	rec := serveVoice(mux, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "sk-") || strings.Contains(body, "presets") {
		t.Fatalf("response carries a key or presets: %s", body)
	}
	var got struct {
		STT       []config.STTProvider `json:"stt"`
		Providers []sttProviderOption  `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	wantSTT := []config.STTProvider{{Provider: "my-openrouter", Enabled: true, Model: "openai/whisper-1"}}
	if !reflect.DeepEqual(got.STT, wantSTT) {
		t.Errorf("stt = %+v, want %+v", got.STT, wantSTT)
	}
	wantProviders := []sttProviderOption{
		{Name: "openai", DefaultModel: "whisper-1", Ready: true},
		{Name: "groq", DefaultModel: "whisper-large-v3", Ready: false},
		{Name: "my-openrouter", DefaultModel: "openai/whisper-large-v3", Ready: true},
	}
	if !reflect.DeepEqual(got.Providers, wantProviders) {
		t.Errorf("providers = %+v, want %+v", got.Providers, wantProviders)
	}
}

// An empty list is served as [], not null.
func TestHandleGetVoiceSTT_EmptyList(t *testing.T) {
	_, mux, _ := setupVoiceEnv(t, nil)
	rec := serveVoice(mux, http.MethodGet, "")
	if !strings.Contains(rec.Body.String(), `"stt":[]`) {
		t.Errorf("body = %s, want an empty stt list", rec.Body.String())
	}
}

func TestHandleUpdateVoiceSTT(t *testing.T) {
	t.Run("saves trimmed entries and reloads", func(t *testing.T) {
		configPath, mux, reloads := setupVoiceEnv(t, nil)
		rec := serveVoice(mux, http.MethodPut, `{"stt":[{"provider":" groq ","enabled":true,"model":" whisper-large-v3-turbo "},{"provider":"my-openrouter","enabled":false}]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatal(err)
		}
		want := []config.STTProvider{
			{Provider: "groq", Enabled: true, Model: "whisper-large-v3-turbo"},
			{Provider: "my-openrouter", Enabled: false},
		}
		if !reflect.DeepEqual(cfg.Voice.STT, want) {
			t.Errorf("saved stt = %+v, want %+v", cfg.Voice.STT, want)
		}
		if *reloads != 1 {
			t.Errorf("reloads = %d, want 1", *reloads)
		}
	})

	for _, tc := range []struct {
		name, body, wantMsg string
	}{
		{"blank provider", `{"stt":[{"provider":"  ","enabled":true}]}`, "each STT entry needs a provider"},
		{"unknown provider", `{"stt":[{"provider":"groq","enabled":true},{"provider":"nobody","enabled":true}]}`, `unknown provider \"nobody\"`},
		{"provider name is case-sensitive", `{"stt":[{"provider":"Groq","enabled":true}]}`, `unknown provider \"Groq\"`},
		{"anthropic provider", `{"stt":[{"provider":"anthropic-api","enabled":true}]}`, `provider \"anthropic-api\" cannot transcribe`},
		{"azure provider", `{"stt":[{"provider":"azure-api","enabled":true}]}`, `provider \"azure-api\" cannot transcribe`},
		{"CLI provider", `{"stt":[{"provider":"Claude CLI","enabled":true}]}`, `provider \"Claude CLI\" cannot transcribe`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath, mux, reloads := setupVoiceEnv(t, []config.STTProvider{{Provider: "groq", Enabled: true}})
			rec := serveVoice(mux, http.MethodPut, tc.body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Fatalf("status = %d, body=%s; want 400 with %s", rec.Code, rec.Body.String(), tc.wantMsg)
			}
			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if want := []config.STTProvider{{Provider: "groq", Enabled: true}}; !reflect.DeepEqual(cfg.Voice.STT, want) {
				t.Errorf("stt changed to %+v on a refused update", cfg.Voice.STT)
			}
			if *reloads != 0 {
				t.Errorf("reloads = %d, want 0", *reloads)
			}
		})
	}
}

// An entry in the old shape (its own api_key and base_url) loads with those
// keys ignored, and the next save drops them from the file.
func TestVoiceSTT_OldShapeKeysDroppedOnSave(t *testing.T) {
	configPath, mux, _ := setupVoiceEnv(t, nil)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["voice"] = map[string]any{"stt": []any{map[string]any{
		"provider": "groq", "enabled": true, "api_key": "sk-old-entry-key", "base_url": "https://api.groq.com/openai/v1",
	}}}
	if raw, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	rec := serveVoice(mux, http.MethodGet, "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "sk-old-entry-key") || !strings.Contains(rec.Body.String(), `"provider":"groq"`) {
		t.Fatalf("GET status = %d, body=%s", rec.Code, rec.Body.String())
	}

	if rec = serveVoice(mux, http.MethodPut, `{"stt":[{"provider":"groq","enabled":true}]}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if raw, err = os.ReadFile(configPath); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-old-entry-key") {
		t.Error("the old entry's api_key is still in the file after a save")
	}
}

func serveProviderAt(mux *http.ServeMux, method string, index int, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/providers/"+strconv.Itoa(index), strings.NewReader(body)))
	return rec
}

// providerIndex returns the position of the named provider in the saved config.
func providerIndex(t *testing.T, configPath, name string) int {
	t.Helper()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == name {
			return i
		}
	}
	t.Fatalf("provider %q not found", name)
	return -1
}

// Renaming a provider carries every speech backend that named it to the new
// name and leaves the others alone.
func TestUpdateProvider_RenameCarriesSpeechBackends(t *testing.T) {
	configPath, mux, _ := setupVoiceEnv(t, []config.STTProvider{
		{Provider: "groq", Enabled: true, Model: "whisper-large-v3-turbo"},
		{Provider: "my-openrouter", Enabled: true},
		{Provider: "groq", Enabled: false},
	})
	idx := providerIndex(t, configPath, "groq")
	if rec := serveProviderAt(mux, http.MethodPut, idx, `{"name":"groq-renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []config.STTProvider{
		{Provider: "groq-renamed", Enabled: true, Model: "whisper-large-v3-turbo"},
		{Provider: "my-openrouter", Enabled: true},
		{Provider: "groq-renamed", Enabled: false},
	}
	if !reflect.DeepEqual(cfg.Voice.STT, want) {
		t.Errorf("stt = %+v, want %+v", cfg.Voice.STT, want)
	}
}

// A provider named by a speech backend, enabled or not, cannot be deleted;
// one no backend names can.
func TestDeleteProvider_RefusedWhileSpeechBackendUsesIt(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		configPath, mux, _ := setupVoiceEnv(t, []config.STTProvider{{Provider: "groq", Enabled: enabled}})
		rec := serveProviderAt(mux, http.MethodDelete, providerIndex(t, configPath, "groq"), "")
		if want := `provider \"groq\" is in use by a speech backend`; rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("enabled=%v: DELETE status = %d, body=%s; want 409 with %s", enabled, rec.Code, rec.Body.String(), want)
		}
		providerIndex(t, configPath, "groq") // still configured

		if rec = serveProviderAt(mux, http.MethodDelete, providerIndex(t, configPath, "my-openrouter"), ""); rec.Code != http.StatusOK {
			t.Fatalf("enabled=%v: DELETE of an unused provider status = %d, body=%s", enabled, rec.Code, rec.Body.String())
		}
	}
}
