package voice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// Ensure whisperTranscriber satisfies the Transcriber interface at compile time.
var _ Transcriber = (*whisperTranscriber)(nil)

func TestWhisperTranscriberName(t *testing.T) {
	tr := NewWhisperTranscriber("groq", "sk-test", "", "", nil)
	if got := tr.Name(); got != "groq" {
		t.Errorf("Name() = %q, want %q", got, "groq")
	}
}

// Providers shared by the detection tests.
var (
	groqProvider       = config.Provider{Name: "groq", Protocol: "openai-chat", BaseURL: "https://api.groq.com/openai/v1", APIKey: "sk-groq"}
	openAIProvider     = config.Provider{Name: "openai", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-openai"}
	openRouterProvider = config.Provider{Name: "my-openrouter", Protocol: "openai-chat", BaseURL: "https://openrouter.ai/api/v1", APIKey: "sk-or"}
	customProvider     = config.Provider{Name: "local-whisper", Protocol: "openai-chat", BaseURL: "https://stt.example.com/v1", APIKey: "sk-local"}
)

// sttModel is a model_list entry named alias on provider with model id model.
func sttModel(alias, provider, model string) config.ModelConfig {
	return config.ModelConfig{ModelName: alias, Provider: provider, Model: model, Enabled: true}
}

// sttConfig is a config with the providers and models given and the named
// models ticked Transcription (STT), in that order.
func sttConfig(providers []config.Provider, models []config.ModelConfig, stt ...string) *config.Config {
	cfg := &config.Config{Providers: providers, Models: models}
	for _, name := range stt {
		cfg.TaskModels.Models = append(cfg.TaskModels.Models, config.TaskModel{Model: name, STT: true})
	}
	return cfg
}

func TestDetectTranscriber(t *testing.T) {
	allModels := []config.ModelConfig{
		sttModel("whisper-openai", "openai", "whisper-1"),
		sttModel("whisper-or", "my-openrouter", "openai/whisper-large-v3"),
		sttModel("whisper-groq", "groq", "whisper-large-v3"),
		sttModel("whisper-local", "local-whisper", "whisper-1"),
	}
	allProviders := []config.Provider{openAIProvider, openRouterProvider, groqProvider, customProvider}
	tests := []struct {
		name     string
		cfg      *config.Config
		wantNil  bool
		wantName string
	}{
		{
			name:    "no config",
			cfg:     &config.Config{},
			wantNil: true,
		},
		{
			// A model alone does not enable STT; only the Transcription tick does.
			name:    "model not ticked does not enable STT",
			cfg:     sttConfig(allProviders, allModels),
			wantNil: true,
		},
		{
			name: "a model ticked for other uses only is not used",
			cfg: func() *config.Config {
				c := sttConfig(allProviders, allModels)
				c.TaskModels.Models = []config.TaskModel{{Model: "whisper-groq", Text: true, Vision: true}}
				return c
			}(),
			wantNil: true,
		},
		{
			name:     "the one ticked model is used",
			cfg:      sttConfig(allProviders, allModels, "whisper-or"),
			wantName: "whisper-or",
		},
		{
			name: "ticked models form a fallback chain in page order",
			cfg:  sttConfig(allProviders, allModels, "whisper-or", "whisper-groq"),
			// fallbackTranscriber.Name() joins the chain in order.
			wantName: "whisper-or,whisper-groq",
		},
		{
			name:     "page order wins over model_list order",
			cfg:      sttConfig(allProviders, allModels, "whisper-groq", "whisper-openai"),
			wantName: "whisper-groq,whisper-openai",
		},
		{
			name:     "a model on an unknown host is used",
			cfg:      sttConfig(allProviders, allModels, "whisper-local"),
			wantName: "whisper-local",
		},
		{
			name: "every enabled entry of a load-balanced model joins the chain",
			cfg: sttConfig(allProviders, []config.ModelConfig{
				sttModel("whisper", "groq", "whisper-large-v3"),
				sttModel("whisper", "openai", "whisper-1"),
			}, "whisper"),
			wantName: "whisper,whisper",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := DetectTranscriber(tc.cfg)
			if tc.wantNil {
				if tr != nil {
					t.Errorf("DetectTranscriber() = %v, want nil", tr)
				}
				return
			}
			if tr == nil {
				t.Fatal("DetectTranscriber() = nil, want non-nil")
			}
			if got := tr.Name(); got != tc.wantName {
				t.Errorf("Name() = %q, want %q", got, tc.wantName)
			}
		})
	}
}

// A model that cannot be used is skipped, with its reason, and the next
// usable model is chosen alone rather than starting a chain with a broken one.
func TestDetectTranscriber_SkipsUnusableModels(t *testing.T) {
	providers := []config.Provider{
		groqProvider,
		{Name: "no-key", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1"},
		{Name: "no-url", Protocol: "openai-chat", APIKey: "sk-x"},
		{Name: "cli", Protocol: "claude-cli"},
		{Name: "anthropic-api", Protocol: "anthropic", BaseURL: "https://api.anthropic.com/v1", APIKey: "sk-a"},
		{Name: "azure-api", Protocol: "azure", BaseURL: "https://example.openai.azure.com", APIKey: "sk-z"},
	}
	for _, tc := range []struct {
		name     string
		provider string
		wantSkip string
	}{
		{"provider not configured", "missing", "provider not configured"},
		{"provider name is case-sensitive", "Groq", "provider not configured"},
		{"provider without a key", "no-key", "provider has no API key"},
		{"provider without a base URL", "no-url", "provider has no base URL"},
		{"CLI provider", "cli", "provider protocol is not OpenAI-compatible"},
		{"anthropic provider", "anthropic-api", "provider protocol is not OpenAI-compatible"},
		{"azure provider", "azure-api", "provider protocol is not OpenAI-compatible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := sttModel("bad", tc.provider, "whisper-1")
			cfg := sttConfig(providers, []config.ModelConfig{bad, sttModel("whisper-groq", "groq", "whisper-large-v3")}, "bad", "whisper-groq")
			if _, skip := resolveSTT(cfg, "bad", &bad); skip != tc.wantSkip {
				t.Fatalf("resolveSTT skip = %q, want %q", skip, tc.wantSkip)
			}
			w, ok := DetectTranscriber(cfg).(*whisperTranscriber)
			if !ok || w.name != "whisper-groq" {
				t.Fatalf("DetectTranscriber() = %v, want the groq transcriber alone", w)
			}
		})
	}
}

// A ticked model that is missing from the model list, or disabled there, is
// skipped and the next ticked model serves.
func TestDetectTranscriber_SkipsMissingModel(t *testing.T) {
	disabled := sttModel("off", "groq", "whisper-large-v3")
	disabled.Enabled = false
	cfg := sttConfig([]config.Provider{groqProvider},
		[]config.ModelConfig{disabled, sttModel("whisper-groq", "groq", "whisper-large-v3")},
		"gone", "off", "whisper-groq")
	rec := &alertRecorder{}
	w, ok := DetectTranscriberWithAlerter(cfg, rec).(*whisperTranscriber)
	if !ok || w.name != "whisper-groq" {
		t.Fatalf("DetectTranscriber() = %v, want the groq transcriber alone", w)
	}
	if len(rec.alerts) != 2 || rec.alerts[0].EventID != "voice:config:gone" || rec.alerts[1].EventID != "voice:config:off" {
		t.Fatalf("alerts = %+v, want one each for gone and off", rec.alerts)
	}
}

// The endpoint and key come from the model's provider, the model id from the
// model_list entry.
func TestDetectTranscriber_UsesTheProvider(t *testing.T) {
	cfg := sttConfig([]config.Provider{groqProvider},
		[]config.ModelConfig{sttModel("fast-stt", "groq", "whisper-large-v3-turbo")}, "fast-stt")
	w, ok := DetectTranscriber(cfg).(*whisperTranscriber)
	if !ok {
		t.Fatalf("DetectTranscriber() = %T, want *whisperTranscriber", DetectTranscriber(cfg))
	}
	if w.name != "fast-stt" || w.apiKey != "sk-groq" || w.apiBase != "https://api.groq.com/openai/v1" || w.model != "whisper-large-v3-turbo" {
		t.Fatalf("transcriber = %s %s %s, want fast-stt at the provider endpoint with its model id", w.name, w.apiBase, w.model)
	}
}

// The wire protocol follows the endpoint's host, not the provider's name.
func TestNewTranscriber_ChoosesProtocolByHost(t *testing.T) {
	if _, ok := NewTranscriber("my-openrouter", "sk", "https://openrouter.ai/api/v1", "m", nil).(*openRouterTranscriber); !ok {
		t.Error("openrouter.ai must use the OpenRouter protocol")
	}
	if _, ok := NewTranscriber("openrouter", "sk", "https://api.groq.com/openai/v1", "m", nil).(*whisperTranscriber); !ok {
		t.Error("a provider named openrouter on another host must use the multipart protocol")
	}
	if got := NewTranscriber("my-openrouter", "sk", "https://openrouter.ai/api/v1", "m", nil).Name(); got != "my-openrouter" {
		t.Errorf("Name() = %q, want the configured provider name", got)
	}
}

// stubTranscriber is a Transcriber whose result is fixed for tests.
type stubTranscriber struct {
	name string
	text string
	err  error
}

func (s stubTranscriber) Name() string { return s.name }
func (s stubTranscriber) Transcribe(context.Context, string) (*TranscriptionResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &TranscriptionResponse{Text: s.text}, nil
}

func TestFallbackTranscriber(t *testing.T) {
	t.Run("uses next when first errors", func(t *testing.T) {
		f := &fallbackTranscriber{transcribers: []Transcriber{
			stubTranscriber{name: "a", err: errors.New("boom")},
			stubTranscriber{name: "b", text: "from b"},
		}}
		got, err := f.Transcribe(context.Background(), "x")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Text != "from b" {
			t.Errorf("Text = %q, want %q", got.Text, "from b")
		}
	})

	t.Run("empty transcript is a success, not a fallback trigger", func(t *testing.T) {
		f := &fallbackTranscriber{transcribers: []Transcriber{
			stubTranscriber{name: "a", text: ""}, // silence
			stubTranscriber{name: "b", text: "should not reach"},
		}}
		got, err := f.Transcribe(context.Background(), "x")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Text != "" {
			t.Errorf("Text = %q, want empty (first transcriber wins)", got.Text)
		}
	})

	t.Run("returns last error when all fail", func(t *testing.T) {
		f := &fallbackTranscriber{transcribers: []Transcriber{
			stubTranscriber{name: "a", err: errors.New("first")},
			stubTranscriber{name: "b", err: errors.New("last")},
		}}
		_, err := f.Transcribe(context.Background(), "x")
		if err == nil || err.Error() != "last" {
			t.Errorf("err = %v, want %q", err, "last")
		}
	})
}

func TestTranscribe(t *testing.T) {
	// Write a minimal fake audio file so the transcriber can open and send it.
	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "clip.ogg")
	if err := os.WriteFile(audioPath, []byte("fake-audio-data"), 0o644); err != nil {
		t.Fatalf("failed to write fake audio file: %v", err)
	}

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/audio/transcriptions" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "Bearer sk-test" {
				t.Errorf("unexpected Authorization header: %s", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(TranscriptionResponse{
				Text:     "hello world",
				Language: "en",
				Duration: 1.5,
			}); err != nil {
				t.Errorf("encode response: %v", err)
			}
		}))
		defer srv.Close()

		tr := NewWhisperTranscriber("groq", "sk-test", "", "", nil)
		tr.apiBase = srv.URL

		resp, err := tr.Transcribe(context.Background(), audioPath)
		if err != nil {
			t.Fatalf("Transcribe() error: %v", err)
		}
		if resp.Text != "hello world" {
			t.Errorf("Text = %q, want %q", resp.Text, "hello world")
		}
		if resp.Language != "en" {
			t.Errorf("Language = %q, want %q", resp.Language, "en")
		}
	})

	t.Run("api error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"invalid_api_key"}`, http.StatusUnauthorized)
		}))
		defer srv.Close()

		tr := NewWhisperTranscriber("groq", "sk-bad", "", "", nil)
		tr.apiBase = srv.URL

		_, err := tr.Transcribe(context.Background(), audioPath)
		if err == nil {
			t.Fatal("expected error for non-200 response, got nil")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		tr := NewWhisperTranscriber("groq", "sk-test", "", "", nil)
		_, err := tr.Transcribe(context.Background(), filepath.Join(tmpDir, "nonexistent.ogg"))
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})
}
