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

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
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
	alerter    alerter.Alerter
}

type TranscriptionResponse struct {
	Text     string  `json:"text"`
	Language string  `json:"language,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

// openRouterHost is the host whose transcription endpoint speaks OpenRouter's
// JSON/base64 protocol rather than the OpenAI multipart one.
const openRouterHost = "openrouter.ai"

// sttDefaultModels maps the host of a known speech-to-text endpoint to the
// model used when a voice.stt entry leaves its model blank. It is the single
// source of truth for those defaults; the WebUI reads it via DefaultSTTModel.
var sttDefaultModels = map[string]string{
	"api.groq.com":   "whisper-large-v3",
	"api.openai.com": "whisper-1",
	openRouterHost:   "openai/whisper-large-v3",
}

// DefaultSTTModel returns the default transcription model for a provider base
// URL, chosen by its host, or "" when the host is not a known STT service.
func DefaultSTTModel(baseURL string) string {
	return sttDefaultModels[urlHost(baseURL)]
}

// NewWhisperTranscriber builds a transcriber for an OpenAI-compatible endpoint.
// name is the configured provider name, used in logs, Name() and alerts.
// a receives the alert raised when the API rejects the key or account; nil
// means none.
func NewWhisperTranscriber(name, apiKey, baseURL, model string, a alerter.Alerter) *whisperTranscriber {
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
		alerter: orNop(a),
	}
}

func (t *whisperTranscriber) Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error) {
	logger.InfoCF("voice", "Starting transcription", map[string]any{"audio_file": audioFilePath})

	audioFile, err := os.Open(audioFilePath) //nolint:gosec // audio path comes from the media store (loop_transcribe)
	if err != nil {
		logger.ErrorCF("voice", "Failed to open audio file", map[string]any{"path": audioFilePath, "error": err})
		return nil, fmt.Errorf("failed to open audio file: %w", err)
	}
	defer utils.CloseQuietly(audioFile)

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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &requestBody)
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
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.DebugCF("voice", "Response body close failed", map[string]any{"error": closeErr.Error()})
		}
	}()

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
		alertRejected(t.alerter, t.name, resp.StatusCode, body)
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
	name       string
	apiKey     string
	apiBase    string
	model      string
	httpClient *http.Client
	alerter    alerter.Alerter
}

// NewOpenRouterTranscriber builds a transcriber for OpenRouter. name and a are
// as for NewWhisperTranscriber.
func NewOpenRouterTranscriber(name, apiKey, baseURL, model string, a alerter.Alerter) *openRouterTranscriber {
	logger.DebugCF("voice", "Creating transcriber", map[string]any{
		"provider": name, "base_url": baseURL, "model": model, "has_api_key": apiKey != "",
	})
	return &openRouterTranscriber{
		name:       name,
		apiKey:     apiKey,
		apiBase:    strings.TrimRight(baseURL, "/"),
		model:      model,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		alerter:    orNop(a),
	}
}

func (t *openRouterTranscriber) Name() string { return t.name }

func (t *openRouterTranscriber) Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error) {
	logger.InfoCF("voice", "Starting transcription", map[string]any{"audio_file": audioFilePath, "provider": t.name})

	raw, err := os.ReadFile(audioFilePath) //nolint:gosec // audio path comes from the media store (loop_transcribe)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.apiKey)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.DebugCF("voice", "Response body close failed", map[string]any{"error": closeErr.Error()})
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		logger.ErrorCF("voice", "API error", map[string]any{"provider": t.name, "status_code": resp.StatusCode, "response": string(body)})
		alertRejected(t.alerter, t.name, resp.StatusCode, body)
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
		"provider":         t.name,
		"text_length":      len(result.Text),
		"duration_seconds": result.Duration,
	})
	return result, nil
}

// NewTranscriber builds the right transcriber for an endpoint, chosen by the
// base URL's host. OpenRouter uses a JSON/base64 protocol; every other host is
// treated as OpenAI-compatible multipart (Groq, OpenAI, or any OpenAI-style
// host). name is the configured provider name.
func NewTranscriber(name, apiKey, baseURL, model string, a alerter.Alerter) Transcriber {
	if urlHost(baseURL) == openRouterHost {
		return NewOpenRouterTranscriber(name, apiKey, baseURL, model, a)
	}
	return NewWhisperTranscriber(name, apiKey, baseURL, model, a)
}

func orNop(a alerter.Alerter) alerter.Alerter {
	if a == nil {
		return alerter.Nop{}
	}
	return a
}

// alertRejected raises a high alert when the API turned the request away for
// a reason that persists across requests: bad credentials (401/403) or an
// exhausted account (402). Other statuses are per-request failures and stay
// in the log only.
func alertRejected(a alerter.Alerter, provider string, status int, body []byte) {
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden:
	default:
		return
	}
	details := []rune(strings.TrimSpace(string(body)))
	if len(details) > 200 {
		details = details[:200]
	}
	a.Send(alerter.Alert{
		Title:       "Voice transcription rejected",
		Description: fmt.Sprintf("%s: status %d; voice messages are not transcribed until the key or account is fixed", provider, status),
		Details:     string(details),
		EventID:     "voice:" + provider,
	})
}

// urlHost returns the lower-cased host name (without port) of a URL, or "" if
// it can't be parsed.
func urlHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// sttBackend is a voice.stt entry resolved against the provider it names.
type sttBackend struct {
	name, apiKey, baseURL, model string
}

// CanTranscribe reports whether a provider can serve speech-to-text: it speaks
// an OpenAI-compatible protocol and has a base URL to send audio to.
func CanTranscribe(p *config.Provider) bool {
	return isOpenAIProtocol(p.Protocol) && p.BaseURL != ""
}

func isOpenAIProtocol(protocol string) bool {
	return protocol == "openai-chat" || protocol == "openai-responses"
}

// resolveSTT resolves an entry to the provider it names (Config.GetProvider,
// matched case-sensitively). The endpoint and key are the provider's; the
// model is the entry's, or the default for the provider's host. A non-empty
// skip is the reason the entry cannot be used.
func resolveSTT(cfg *config.Config, s *config.STTProvider) (b sttBackend, skip string) {
	p, err := cfg.GetProvider(s.Provider)
	if err != nil {
		return b, "provider not configured"
	}
	if !CanTranscribe(p) {
		if p.BaseURL == "" && isOpenAIProtocol(p.Protocol) {
			return b, "provider has no base URL"
		}
		return b, "provider protocol is not OpenAI-compatible"
	}
	if p.APIKey == "" {
		return b, "provider has no API key"
	}
	model := s.Model
	if model == "" {
		model = DefaultSTTModel(p.BaseURL)
	}
	if model == "" {
		return b, "no model set and no default for this endpoint"
	}
	return sttBackend{name: p.Name, apiKey: p.APIKey, baseURL: p.BaseURL, model: model}, ""
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
// entries (each usable when it names a configured OpenAI-compatible provider with a base
// URL and an API key, and has a model or a default for that host). Multiple enabled entries form an ordered fallback chain, tried in
// listed order. No enabled entry means transcription is off. The result raises
// no operator alerts; the gateway uses DetectTranscriberWithAlerter.
func DetectTranscriber(cfg *config.Config) Transcriber {
	return DetectTranscriberWithAlerter(cfg, nil)
}

// DetectTranscriberWithAlerter is DetectTranscriber with an alerter that
// receives the alert raised when a provider rejects the key or account.
func DetectTranscriberWithAlerter(cfg *config.Config, a alerter.Alerter) Transcriber {
	var chain []Transcriber
	for i := range cfg.Voice.STT {
		s := &cfg.Voice.STT[i]
		if !s.Enabled {
			continue
		}
		b, skip := resolveSTT(cfg, s)
		if skip != "" {
			logger.WarnCF("voice", "Speech backend skipped: "+skip, map[string]any{
				"provider": s.Provider, "reason": skip,
			})
			continue
		}
		chain = append(chain, NewTranscriber(b.name, b.apiKey, b.baseURL, b.model, a))
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
