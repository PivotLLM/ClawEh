package voice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

type Transcriber interface {
	Name() string
	Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error)
}

// whisperTranscriber talks to any OpenAI-compatible /audio/transcriptions
// endpoint (Groq, OpenAI, OpenRouter, or a custom host). name is the provider
// label surfaced to logs and the Transcriber interface.
type whisperTranscriber struct {
	name       string
	apiKey     string
	apiBase    string
	model      string
	httpClient *http.Client
}

type TranscriptionResponse struct {
	Text     string  `json:"text"`
	Language string  `json:"language,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

// STTPreset holds the default endpoint and model for a known STT provider.
type STTPreset struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
}

// sttPresets is the single source of truth for known provider defaults, ordered
// for stable UI display. The WebUI reads it via STTPresets.
var sttPresets = []STTPreset{
	{"groq", "https://api.groq.com/openai/v1", "whisper-large-v3"},
	{"openai", "https://api.openai.com/v1", "whisper-1"},
	{"openrouter", "https://openrouter.ai/api/v1", "openai/whisper-large-v3"},
}

// STTPresets returns the known provider presets in display order.
func STTPresets() []STTPreset { return sttPresets }

// presetFor returns the preset for a provider name, or a zero preset if unknown.
func presetFor(provider string) STTPreset {
	for _, p := range sttPresets {
		if p.Provider == provider {
			return p
		}
	}
	return STTPreset{}
}

// NewWhisperTranscriber builds a transcriber for an OpenAI-compatible endpoint.
// Blank baseURL/model fall back to the provider preset (else groq defaults).
func NewWhisperTranscriber(name, apiKey, baseURL, model string) *whisperTranscriber {
	preset := presetFor(name)
	if baseURL == "" {
		baseURL = preset.BaseURL
		if baseURL == "" {
			baseURL = presetFor("groq").BaseURL
		}
	}
	if model == "" {
		model = preset.Model
		if model == "" {
			model = presetFor("groq").Model
		}
	}
	logger.DebugCF("voice", "Creating transcriber", map[string]any{
		"provider": name, "base_url": baseURL, "model": model, "has_api_key": apiKey != "",
	})
	return &whisperTranscriber{
		name:    name,
		apiKey:  apiKey,
		apiBase: strings.TrimRight(baseURL, "/"),
		model:   model,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (t *whisperTranscriber) Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error) {
	logger.InfoCF("voice", "Starting transcription", map[string]any{"audio_file": audioFilePath})

	audioFile, err := os.Open(audioFilePath)
	if err != nil {
		logger.ErrorCF("voice", "Failed to open audio file", map[string]any{"path": audioFilePath, "error": err})
		return nil, fmt.Errorf("failed to open audio file: %w", err)
	}
	defer audioFile.Close()

	fileInfo, err := audioFile.Stat()
	if err != nil {
		logger.ErrorCF("voice", "Failed to get file info", map[string]any{"path": audioFilePath, "error": err})
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}

	logger.DebugCF("voice", "Audio file details", map[string]any{
		"size_bytes": fileInfo.Size(),
		"file_name":  filepath.Base(audioFilePath),
	})

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	part, err := writer.CreateFormFile("file", filepath.Base(audioFilePath))
	if err != nil {
		logger.ErrorCF("voice", "Failed to create form file", map[string]any{"error": err})
		return nil, fmt.Errorf("failed to create form file: %w", err)
	}

	copied, err := io.Copy(part, audioFile)
	if err != nil {
		logger.ErrorCF("voice", "Failed to copy file content", map[string]any{"error": err})
		return nil, fmt.Errorf("failed to copy file content: %w", err)
	}

	logger.DebugCF("voice", "File copied to request", map[string]any{"bytes_copied": copied})

	if err = writer.WriteField("model", t.model); err != nil {
		logger.ErrorCF("voice", "Failed to write model field", map[string]any{"error": err})
		return nil, fmt.Errorf("failed to write model field: %w", err)
	}

	if err = writer.WriteField("response_format", "json"); err != nil {
		logger.ErrorCF("voice", "Failed to write response_format field", map[string]any{"error": err})
		return nil, fmt.Errorf("failed to write response_format field: %w", err)
	}

	if err = writer.Close(); err != nil {
		logger.ErrorCF("voice", "Failed to close multipart writer", map[string]any{"error": err})
		return nil, fmt.Errorf("failed to close multipart writer: %w", err)
	}

	url := t.apiBase + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, &requestBody)
	if err != nil {
		logger.ErrorCF("voice", "Failed to create request", map[string]any{"error": err})
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+t.apiKey)

	logger.DebugCF("voice", "Sending transcription request", map[string]any{
		"provider":           t.name,
		"url":                url,
		"request_size_bytes": requestBody.Len(),
		"file_size_bytes":    fileInfo.Size(),
	})

	resp, err := t.httpClient.Do(req)
	if err != nil {
		logger.ErrorCF("voice", "Failed to send request", map[string]any{"error": err})
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.ErrorCF("voice", "Failed to read response", map[string]any{"error": err})
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		logger.ErrorCF("voice", "API error", map[string]any{
			"status_code": resp.StatusCode,
			"response":    string(body),
		})
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	logger.DebugCF("voice", "Received transcription response", map[string]any{
		"provider":            t.name,
		"status_code":         resp.StatusCode,
		"response_size_bytes": len(body),
	})

	var result TranscriptionResponse
	if err := json.Unmarshal(body, &result); err != nil {
		logger.ErrorCF("voice", "Failed to unmarshal response", map[string]any{"error": err})
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	logger.InfoCF("voice", "Transcription completed successfully", map[string]any{
		"text_length":      len(result.Text),
		"language":         result.Language,
		"duration_seconds": result.Duration,
	})

	return &result, nil
}

func (t *whisperTranscriber) Name() string {
	return t.name
}

// openRouterTranscriber talks to OpenRouter's /audio/transcriptions endpoint,
// which — unlike Groq/OpenAI — takes a JSON body with base64-encoded audio
// (input_audio.data + .format) rather than a multipart file upload, and returns
// {"text", "usage"}. Model ids are namespaced (e.g. "openai/whisper-large-v3").
type openRouterTranscriber struct {
	apiKey     string
	apiBase    string
	model      string
	httpClient *http.Client
}

// NewOpenRouterTranscriber builds a transcriber for OpenRouter. Blank
// baseURL/model fall back to the openrouter preset.
func NewOpenRouterTranscriber(apiKey, baseURL, model string) *openRouterTranscriber {
	preset := presetFor("openrouter")
	if baseURL == "" {
		baseURL = preset.BaseURL
	}
	if model == "" {
		model = preset.Model
	}
	logger.DebugCF("voice", "Creating transcriber", map[string]any{
		"provider": "openrouter", "base_url": baseURL, "model": model, "has_api_key": apiKey != "",
	})
	return &openRouterTranscriber{
		apiKey:     apiKey,
		apiBase:    strings.TrimRight(baseURL, "/"),
		model:      model,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func (t *openRouterTranscriber) Name() string { return "openrouter" }

func (t *openRouterTranscriber) Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error) {
	logger.InfoCF("voice", "Starting transcription", map[string]any{"audio_file": audioFilePath, "provider": "openrouter"})

	raw, err := os.ReadFile(audioFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read audio file: %w", err)
	}
	// OpenRouter needs the container format, taken from the file extension.
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(audioFilePath)), ".")
	if format == "" {
		return nil, fmt.Errorf("cannot determine audio format from %q", filepath.Base(audioFilePath))
	}

	reqBody, err := json.Marshal(map[string]any{
		"input_audio": map[string]string{
			"data":   base64.StdEncoding.EncodeToString(raw),
			"format": format,
		},
		"model": t.model,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	reqURL := t.apiBase + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.apiKey)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		logger.ErrorCF("voice", "API error", map[string]any{"provider": "openrouter", "status_code": resp.StatusCode, "response": string(body)})
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	// OpenRouter response: {"text": "...", "usage": {"seconds": 9.2, ...}}
	var parsed struct {
		Text  string `json:"text"`
		Usage struct {
			Seconds float64 `json:"seconds"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	result := &TranscriptionResponse{Text: parsed.Text, Duration: parsed.Usage.Seconds}
	logger.InfoCF("voice", "Transcription completed successfully", map[string]any{
		"provider":         "openrouter",
		"text_length":      len(result.Text),
		"duration_seconds": result.Duration,
	})
	return result, nil
}

// NewTranscriber builds the right transcriber for a provider. OpenRouter uses a
// JSON/base64 protocol; every other provider is treated as OpenAI-compatible
// multipart (groq, openai, or a custom OpenAI-style host).
func NewTranscriber(provider, apiKey, baseURL, model string) Transcriber {
	if provider == "openrouter" {
		return NewOpenRouterTranscriber(apiKey, baseURL, model)
	}
	return NewWhisperTranscriber(provider, apiKey, baseURL, model)
}

// urlHost returns the host of a URL, or "" if it can't be parsed.
func urlHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// resolveSTTCredentials fills in a Speech entry's endpoint/model from the
// provider preset, and — when the entry has no api_key — borrows the key from a
// configured model provider that targets the same host. This lets an operator
// enable "openrouter" (or groq/openai) in the Speech list without re-typing a
// key they already configured as a provider.
func resolveSTTCredentials(cfg *config.Config, s *config.STTProvider) (apiKey, baseURL, model string) {
	preset := presetFor(s.Provider)
	baseURL = s.BaseURL
	if baseURL == "" {
		baseURL = preset.BaseURL
	}
	model = s.Model
	if model == "" {
		model = preset.Model
	}
	apiKey = s.APIKey
	if apiKey != "" {
		return apiKey, baseURL, model
	}
	if host := urlHost(baseURL); host != "" {
		for i := range cfg.Providers {
			p := &cfg.Providers[i]
			if p.APIKey != "" && urlHost(p.BaseURL) == host {
				return p.APIKey, baseURL, model
			}
		}
	}
	return "", baseURL, model
}

// fallbackTranscriber tries an ordered list of transcribers, moving to the next
// only when one returns an error. An empty transcript is a success (silence),
// not a trigger for fallback.
type fallbackTranscriber struct {
	transcribers []Transcriber
}

func (f *fallbackTranscriber) Name() string {
	names := make([]string, len(f.transcribers))
	for i, t := range f.transcribers {
		names[i] = t.Name()
	}
	return strings.Join(names, ",")
}

func (f *fallbackTranscriber) Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error) {
	var lastErr error
	for i, t := range f.transcribers {
		result, err := t.Transcribe(ctx, audioFilePath)
		if err == nil {
			return result, nil
		}
		lastErr = err
		// Don't keep trying after the caller gave up (e.g. turn timeout).
		if ctx.Err() != nil {
			break
		}
		if i < len(f.transcribers)-1 {
			logger.WarnCF("voice", "transcriber failed, trying fallback", map[string]any{
				"failed_provider": t.Name(),
				"next_provider":   f.transcribers[i+1].Name(),
				"error":           err.Error(),
			})
		}
	}
	return nil, lastErr
}

// DetectTranscriber builds the transcription backend from the enabled voice.stt
// entries (each usable when it has its own key or can borrow one from a matching
// provider). Multiple enabled entries form an ordered fallback chain, tried in
// listed order. No enabled entry means transcription is off.
func DetectTranscriber(cfg *config.Config) Transcriber {
	var chain []Transcriber
	for i := range cfg.Voice.STT {
		s := &cfg.Voice.STT[i]
		if !s.Enabled {
			continue
		}
		apiKey, baseURL, model := resolveSTTCredentials(cfg, s)
		if apiKey == "" {
			continue
		}
		chain = append(chain, NewTranscriber(s.Provider, apiKey, baseURL, model))
	}
	switch len(chain) {
	case 0:
		return nil
	case 1:
		return chain[0]
	default:
		return &fallbackTranscriber{transcribers: chain}
	}
}
