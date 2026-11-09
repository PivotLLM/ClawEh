package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

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
		{Name: "gemini-nokey", Protocol: config.GeminiProtocol},
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
		{"CLI provider", "cli", "provider protocol does not serve speech"},
		{"anthropic provider", "anthropic-api", "provider protocol does not serve speech"},
		{"azure provider", "azure-api", "provider protocol does not serve speech"},
		{"gemini without a key", "gemini-nokey", "provider has no API key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := sttConfig(providers, []config.ModelConfig{
				sttModel("bad", tc.provider, "whisper-1"),
				sttModel("whisper-groq", "groq", "whisper-large-v3"),
			}, "bad", "whisper-groq")
			rec := &alertRecorder{}
			tr := DetectTranscriberWithAlerter(cfg, rec)
			if tr == nil || tr.Name() != "whisper-groq" {
				t.Fatalf("DetectTranscriber() = %v, want the groq transcriber alone", tr)
			}
			if len(rec.alerts) != 1 || !strings.Contains(rec.alerts[0].Description, "bad: "+tc.wantSkip+";") {
				t.Fatalf("alerts = %+v, want one naming %q", rec.alerts, tc.wantSkip)
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
	tr := DetectTranscriberWithAlerter(cfg, rec)
	if tr == nil || tr.Name() != "whisper-groq" {
		t.Fatalf("DetectTranscriber() = %v, want the groq transcriber alone", tr)
	}
	if len(rec.alerts) != 2 || rec.alerts[0].EventID != "voice:config:gone" || rec.alerts[1].EventID != "voice:config:off" {
		t.Fatalf("alerts = %+v, want one each for gone and off", rec.alerts)
	}
}

// SpeechBackend picks the spawnllm client by protocol.
func TestSpeechBackend_ByProtocol(t *testing.T) {
	for _, tc := range []struct {
		p    config.Provider
		want string
	}{
		{groqProvider, "*speech.OpenAI"},
		{openRouterProvider, "*speech.OpenAI"},
		{config.Provider{Protocol: "openai-responses", BaseURL: "https://x/v1", APIKey: "k"}, "*speech.OpenAI"},
		{config.Provider{Protocol: config.GeminiProtocol, APIKey: "k"}, "*speech.Gemini"},
		{config.Provider{Protocol: config.ElevenLabsProtocol, APIKey: "k"}, "*speech.ElevenLabs"},
	} {
		c, problem := SpeechBackend(&tc.p)
		if problem != "" || fmt.Sprintf("%T", c) != tc.want {
			t.Errorf("SpeechBackend(%s) = %T, %q; want %s", tc.p.Protocol, c, problem, tc.want)
		}
	}
}

// fakeSTT serves OpenAI-style multipart and OpenRouter-style JSON
// transcriptions, recording what it was sent.
func fakeSTT(t *testing.T, text string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization")+" "+r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"text": text, "language": "en", "duration": 1.5}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// The endpoint and key come from the model's provider, the model id from the
// model_list entry; the file goes up as a multipart upload.
func TestTranscribe_ThroughSpawnllm(t *testing.T) {
	srv, seen := fakeSTT(t, "hello world")
	audioPath := filepath.Join(t.TempDir(), "clip.ogg")
	if err := os.WriteFile(audioPath, []byte("fake-audio-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	prov := config.Provider{Name: "local", Protocol: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "sk-test"}
	cfg := sttConfig([]config.Provider{prov}, []config.ModelConfig{sttModel("fast-stt", "local", "whisper-large-v3-turbo")}, "fast-stt")
	tr := DetectTranscriber(cfg)
	if tr == nil || tr.Name() != "fast-stt" {
		t.Fatalf("DetectTranscriber() = %v", tr)
	}
	resp, err := tr.Transcribe(context.Background(), audioPath)
	if err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if resp.Text != "hello world" || resp.Language != "en" {
		t.Errorf("result = %+v", resp)
	}
	if len(*seen) != 1 || !strings.HasPrefix((*seen)[0], "/v1/audio/transcriptions Bearer sk-test multipart/form-data") {
		t.Errorf("requests = %v", *seen)
	}
	if _, err := tr.Transcribe(context.Background(), filepath.Join(t.TempDir(), "nonexistent.ogg")); err == nil {
		t.Error("a missing file transcribed")
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
