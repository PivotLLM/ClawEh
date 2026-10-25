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

func TestDetectTranscriber(t *testing.T) {
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
			// A model provider alone does not enable STT; only voice.stt does.
			name:    "provider alone does not enable STT",
			cfg:     &config.Config{Providers: []config.Provider{groqProvider}},
			wantNil: true,
		},
		{
			name: "first enabled entry is used, disabled ones are not",
			cfg: &config.Config{
				Providers: []config.Provider{openAIProvider, openRouterProvider, groqProvider},
				Voice: config.VoiceConfig{STT: []config.STTProvider{
					{Provider: "openai", Enabled: false},
					{Provider: "my-openrouter", Enabled: true},
					{Provider: "groq", Enabled: false},
				}},
			},
			wantName: "my-openrouter",
		},
		{
			name: "multiple enabled entries form an ordered fallback chain",
			cfg: &config.Config{
				Providers: []config.Provider{groqProvider, openRouterProvider},
				Voice: config.VoiceConfig{STT: []config.STTProvider{
					{Provider: "my-openrouter", Enabled: true},
					{Provider: "groq", Enabled: true},
				}},
			},
			// fallbackTranscriber.Name() joins the chain in order.
			wantName: "my-openrouter,groq",
		},
		{
			name: "an unknown host with a model is used",
			cfg: &config.Config{
				Providers: []config.Provider{customProvider},
				Voice:     config.VoiceConfig{STT: []config.STTProvider{{Provider: "local-whisper", Enabled: true, Model: "whisper-1"}}},
			},
			wantName: "local-whisper",
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

// An entry that cannot be used is skipped, with its reason, and the next
// usable entry is chosen alone rather than starting a chain with a broken one.
func TestDetectTranscriber_SkipsUnusableEntries(t *testing.T) {
	providers := []config.Provider{
		groqProvider,
		{Name: "no-key", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1"},
		{Name: "no-url", Protocol: "openai-chat", APIKey: "sk-x"},
		{Name: "cli", Protocol: "claude-cli"},
		{Name: "anthropic-api", Protocol: "anthropic", BaseURL: "https://api.anthropic.com/v1", APIKey: "sk-a"},
		{Name: "azure-api", Protocol: "azure", BaseURL: "https://example.openai.azure.com", APIKey: "sk-z"},
		customProvider,
	}
	for _, tc := range []struct {
		name     string
		entry    config.STTProvider
		wantSkip string
	}{
		{"provider not configured", config.STTProvider{Provider: "missing", Enabled: true}, "provider not configured"},
		{"provider name is case-sensitive", config.STTProvider{Provider: "Groq", Enabled: true}, "provider not configured"},
		{"provider without a key", config.STTProvider{Provider: "no-key", Enabled: true}, "provider has no API key"},
		{"provider without a base URL", config.STTProvider{Provider: "no-url", Enabled: true}, "provider has no base URL"},
		{"CLI provider", config.STTProvider{Provider: "cli", Enabled: true}, "provider protocol is not OpenAI-compatible"},
		{"anthropic provider", config.STTProvider{Provider: "anthropic-api", Enabled: true}, "provider protocol is not OpenAI-compatible"},
		{"azure provider", config.STTProvider{Provider: "azure-api", Enabled: true}, "provider protocol is not OpenAI-compatible"},
		{"unknown host without a model", config.STTProvider{Provider: "local-whisper", Enabled: true}, "no model set and no default for this endpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Providers: providers}
			if _, skip := resolveSTT(cfg, &tc.entry); skip != tc.wantSkip {
				t.Fatalf("resolveSTT skip = %q, want %q", skip, tc.wantSkip)
			}

			cfg.Voice.STT = []config.STTProvider{tc.entry, {Provider: "groq", Enabled: true}}
			w, ok := DetectTranscriber(cfg).(*whisperTranscriber)
			if !ok || w.name != "groq" {
				t.Fatalf("DetectTranscriber() = %v, want the groq transcriber alone", w)
			}
		})
	}
}

// The endpoint and key come from the named provider; the model is the entry's,
// or the default for the provider's host.
func TestDetectTranscriber_UsesTheProvider(t *testing.T) {
	for _, tc := range []struct {
		name      string
		entry     config.STTProvider
		wantModel string
	}{
		{"blank model takes the host default", config.STTProvider{Provider: "groq", Enabled: true}, "whisper-large-v3"},
		{"set model wins", config.STTProvider{Provider: "groq", Enabled: true, Model: "whisper-large-v3-turbo"}, "whisper-large-v3-turbo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Providers: []config.Provider{groqProvider},
				Voice:     config.VoiceConfig{STT: []config.STTProvider{tc.entry}},
			}
			w, ok := DetectTranscriber(cfg).(*whisperTranscriber)
			if !ok {
				t.Fatalf("DetectTranscriber() = %T, want *whisperTranscriber", DetectTranscriber(cfg))
			}
			if w.name != "groq" || w.apiKey != "sk-groq" || w.apiBase != "https://api.groq.com/openai/v1" || w.model != tc.wantModel {
				t.Fatalf("transcriber = %s %s %s, want groq at the provider endpoint with model %s", w.name, w.apiBase, w.model, tc.wantModel)
			}
		})
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

func TestDefaultSTTModel(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.groq.com/openai/v1": "whisper-large-v3",
		"https://api.openai.com/v1":      "whisper-1",
		"https://OpenRouter.ai/api/v1":   "openai/whisper-large-v3",
		"https://stt.example.com/v1":     "",
		"":                               "",
	} {
		if got := DefaultSTTModel(base); got != want {
			t.Errorf("DefaultSTTModel(%q) = %q, want %q", base, got, want)
		}
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
