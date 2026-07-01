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

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// Ensure whisperTranscriber satisfies the Transcriber interface at compile time.
var _ Transcriber = (*whisperTranscriber)(nil)

func TestWhisperTranscriberName(t *testing.T) {
	tr := NewWhisperTranscriber("groq", "sk-test", "", "")
	if got := tr.Name(); got != "groq" {
		t.Errorf("Name() = %q, want %q", got, "groq")
	}
}

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
			// A model provider alone no longer enables STT — only voice.stt does
			// (its key may still be borrowed from a matching provider).
			name: "provider key alone does not enable STT",
			cfg: &config.Config{
				Providers: []config.Provider{
					{Name: "groq", Protocol: "openai-chat", BaseURL: "https://api.groq.com/openai/v1", APIKey: "sk-groq-direct"},
				},
			},
			wantNil: true,
		},
		{
			name: "stt list picks first enabled with key",
			cfg: &config.Config{
				Voice: config.VoiceConfig{
					STT: []config.STTProvider{
						{Provider: "openai", Enabled: false, APIKey: "sk-off"},
						{Provider: "openrouter", Enabled: true, APIKey: "sk-or"},
						{Provider: "groq", Enabled: false, APIKey: "sk-groq"},
					},
				},
			},
			wantName: "openrouter",
		},
		{
			name: "multiple enabled entries form an ordered fallback chain",
			cfg: &config.Config{
				Voice: config.VoiceConfig{
					STT: []config.STTProvider{
						{Provider: "openrouter", Enabled: true, APIKey: "sk-or"},
						{Provider: "groq", Enabled: true, APIKey: "sk-groq"},
					},
				},
			},
			// fallbackTranscriber.Name() joins the chain in order.
			wantName: "openrouter,groq",
		},
		{
			name: "stt list enabled without key is skipped",
			cfg: &config.Config{
				Voice: config.VoiceConfig{
					STT: []config.STTProvider{
						{Provider: "openai", Enabled: true},
					},
				},
			},
			wantNil: true,
		},
		{
			name: "stt entry borrows key from matching provider",
			cfg: &config.Config{
				Providers: []config.Provider{
					{Name: "or", Protocol: "openai-chat", BaseURL: "https://openrouter.ai/api/v1", APIKey: "sk-or-provider"},
				},
				Voice: config.VoiceConfig{
					STT: []config.STTProvider{
						{Provider: "openrouter", Enabled: true}, // no key of its own
					},
				},
			},
			wantName: "openrouter",
		},
		{
			name: "stt entry with no key and no matching provider is skipped",
			cfg: &config.Config{
				Providers: []config.Provider{
					{Name: "groq", Protocol: "openai-chat", BaseURL: "https://api.groq.com/openai/v1", APIKey: "sk-groq"},
				},
				Voice: config.VoiceConfig{
					STT: []config.STTProvider{
						{Provider: "openrouter", Enabled: true}, // openrouter.ai has no provider key
					},
				},
			},
			// The entry can't resolve a key and an unrelated groq provider does
			// not enable STT, so nothing is detected.
			wantNil: true,
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
			_ = json.NewEncoder(w).Encode(TranscriptionResponse{
				Text:     "hello world",
				Language: "en",
				Duration: 1.5,
			})
		}))
		defer srv.Close()

		tr := NewWhisperTranscriber("groq", "sk-test", "", "")
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

		tr := NewWhisperTranscriber("groq", "sk-bad", "", "")
		tr.apiBase = srv.URL

		_, err := tr.Transcribe(context.Background(), audioPath)
		if err == nil {
			t.Fatal("expected error for non-200 response, got nil")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		tr := NewWhisperTranscriber("groq", "sk-test", "", "")
		_, err := tr.Transcribe(context.Background(), filepath.Join(tmpDir, "nonexistent.ogg"))
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})
}
