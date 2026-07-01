package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/utils"
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

// sttPreset holds the default endpoint and model for a known STT provider.
type sttPreset struct {
	baseURL string
	model   string
}

var sttPresets = map[string]sttPreset{
	"groq":       {"https://api.groq.com/openai/v1", "whisper-large-v3"},
	"openai":     {"https://api.openai.com/v1", "whisper-1"},
	"openrouter": {"https://openrouter.ai/api/v1", "whisper-1"},
}

// NewWhisperTranscriber builds a transcriber for an OpenAI-compatible endpoint.
// Blank baseURL/model fall back to the provider preset (else groq defaults).
func NewWhisperTranscriber(name, apiKey, baseURL, model string) *whisperTranscriber {
	preset := sttPresets[name]
	if baseURL == "" {
		baseURL = preset.baseURL
		if baseURL == "" {
			baseURL = sttPresets["groq"].baseURL
		}
	}
	if model == "" {
		model = preset.model
		if model == "" {
			model = sttPresets["groq"].model
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

// NewGroqTranscriber is a preset for the Groq Whisper endpoint.
func NewGroqTranscriber(apiKey string) *whisperTranscriber {
	return NewWhisperTranscriber("groq", apiKey, "", "")
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
		"text_length":           len(result.Text),
		"language":              result.Language,
		"duration_seconds":      result.Duration,
		"transcription_preview": utils.Truncate(result.Text, 50),
	})

	return &result, nil
}

func (t *whisperTranscriber) Name() string {
	return t.name
}

// DetectTranscriber inspects cfg and returns the appropriate Transcriber, or
// nil if no supported transcription provider is configured. Groq is identified
// by a provider whose base URL targets the Groq API (Groq hosts the Whisper
// transcription endpoint claw uses).
// DetectTranscriber picks the transcription backend. It prefers the first
// enabled voice.stt entry that has an API key; when the list is empty it falls
// back to auto-detecting a Groq provider from the model provider list.
func DetectTranscriber(cfg *config.Config) Transcriber {
	for i := range cfg.Voice.STT {
		s := &cfg.Voice.STT[i]
		if s.Enabled && s.APIKey != "" {
			return NewWhisperTranscriber(s.Provider, s.APIKey, s.BaseURL, s.Model)
		}
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.APIKey != "" && strings.Contains(p.BaseURL, "api.groq.com") {
			return NewGroqTranscriber(p.APIKey)
		}
	}
	return nil
}
