// Package voice transcribes incoming voice messages with the models ticked
// Transcription (STT) on the Task models page. The HTTP calls are spawnllm's
// speech package; ClawEh keeps the policy: which models, in which order, the
// per-attempt deadline, the fallback and the operator alerts.
package voice

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/spawnllm/common"
	"github.com/PivotLLM/spawnllm/speech"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

// Transcriber turns one audio file into text.
type Transcriber interface {
	Name() string
	Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error)
}

// TranscriptionResponse is one transcription's result.
type TranscriptionResponse struct {
	Text     string
	Language string
	Duration float64
	// CostUSD is the cost the API reported; nil when it reported none.
	CostUSD *float64
}

// attemptTimeout bounds one transcription call; the next model in the chain
// is tried after it.
const attemptTimeout = 60 * time.Second

// SpeechClient is a speech backend: it transcribes and synthesizes.
type SpeechClient interface {
	speech.Transcriber
	speech.Synthesizer
}

// SpeechBackend returns the spawnllm speech client for a provider, chosen by
// its protocol: OpenAI-style (openai-chat, openai-responses; OpenRouter's
// JSON transcription body is chosen by spawnllm from the host), Gemini or
// ElevenLabs. A non-empty problem is why the provider cannot serve speech.
// These rules belong to the HTTP backends only; config and the Task models
// page do not apply them.
func SpeechBackend(p *config.Provider) (SpeechClient, string) {
	return SpeechBackendWith(p)
}

// SpeechBackendWith is SpeechBackend with options for the client (an HTTP
// client of the caller's).
func SpeechBackendWith(p *config.Provider, opts ...speech.Option) (SpeechClient, string) {
	switch {
	case isOpenAIProtocol(p.Protocol):
		if p.BaseURL == "" {
			return nil, "provider has no base URL"
		}
		if p.APIKey == "" {
			return nil, "provider has no API key"
		}
		return speech.NewOpenAI(p.BaseURL, p.APIKey, opts...), ""
	case p.Protocol == config.GeminiProtocol:
		if p.APIKey == "" {
			return nil, "provider has no API key"
		}
		return speech.NewGemini(p.BaseURL, p.APIKey, opts...), ""
	case p.Protocol == config.ElevenLabsProtocol:
		if p.APIKey == "" {
			return nil, "provider has no API key"
		}
		return speech.NewElevenLabs(p.BaseURL, p.APIKey, opts...), ""
	default:
		return nil, "provider protocol does not serve speech"
	}
}

func isOpenAIProtocol(protocol string) bool {
	return protocol == "openai-chat" || protocol == "openai-responses"
}

// speechTranscriber is one STT model: a spawnllm backend and the model id.
// name is the task model's name, used in logs, Name() and alerts.
type speechTranscriber struct {
	name    string
	model   string
	backend speech.Transcriber
	alerter alerter.Alerter
}

// NewTranscriber returns a Transcriber calling backend with model. name is
// the task model's name; a receives the alert raised when the API rejects
// the key or account (nil means none).
func NewTranscriber(name, model string, backend speech.Transcriber, a alerter.Alerter) Transcriber {
	return &speechTranscriber{name: name, model: model, backend: backend, alerter: orNop(a)}
}

func (t *speechTranscriber) Name() string { return t.name }

// Transcribe sends the file to the model, giving it attemptTimeout.
func (t *speechTranscriber) Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error) {
	logger.InfoCF("voice", "Starting transcription", map[string]any{"audio_file": audioFilePath, "model": t.name})
	f, err := os.Open(audioFilePath) //nolint:gosec // audio path comes from the media store (loop_transcribe)
	if err != nil {
		return nil, fmt.Errorf("failed to open audio file: %w", err)
	}
	defer utils.CloseQuietly(f)

	callCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	start := time.Now()
	res, err := t.backend.Transcribe(callCtx, &speech.TranscribeRequest{
		Model:       t.model,
		Audio:       f,
		Filename:    filepath.Base(audioFilePath),
		ContentType: mime.TypeByExtension(filepath.Ext(audioFilePath)),
	})
	if err != nil {
		if se, ok := errors.AsType[*common.HTTPStatusError](err); ok {
			alertRejected(t.alerter, t.name, se.StatusCode, se.BodyPreview)
		}
		logger.WarnCF("voice", "Transcription failed", map[string]any{
			"model": t.name, "duration_ms": time.Since(start).Milliseconds(), "error": err.Error(),
		})
		return nil, err
	}
	out := &TranscriptionResponse{Text: res.Text, Language: res.Language, Duration: res.DurationSeconds, CostUSD: res.CostUSD}
	logger.InfoCF("voice", "Transcription completed successfully", map[string]any{
		"model":            t.name,
		"text_length":      len(out.Text),
		"language":         out.Language,
		"duration_seconds": out.Duration,
		"elapsed_ms":       time.Since(start).Milliseconds(),
	})
	return out, nil
}

func orNop(a alerter.Alerter) alerter.Alerter {
	if a == nil {
		return alerter.Nop{}
	}
	return a
}

// alertRejected raises an alert when the API turned the request away for a
// reason that persists across requests: bad credentials (401/403, or
// Gemini's 400 API_KEY_INVALID) or an exhausted account (402). Other
// statuses are per-request failures and stay in the log only.
func alertRejected(a alerter.Alerter, model string, status int, body string) {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusPaymentRequired, status == http.StatusForbidden:
	// Gemini answers a bad key with 400 and the reason API_KEY_INVALID.
	case status == http.StatusBadRequest && strings.Contains(body, "API_KEY_INVALID"):
	default:
		return
	}
	details := []rune(strings.TrimSpace(body))
	if len(details) > 200 {
		details = details[:200]
	}
	a.Send(alerter.Alert{
		Title:       "Voice transcription rejected",
		Description: fmt.Sprintf("%s: status %d; voice messages are not transcribed until the key or account is fixed", model, status),
		Details:     string(details),
		EventID:     "voice:" + model,
	})
}

// skippedSTT is an STT task model that could not be used.
type skippedSTT struct {
	model, reason string
}

// alertSkipped raises one alert per skipped model, urgent when none is usable
// (transcription is off) and normal when another one still serves.
func alertSkipped(a alerter.Alerter, skipped []skippedSTT, noneUsable bool) {
	if a == nil {
		return
	}
	priority := alerter.Normal
	if noneUsable {
		priority = alerter.Urgent
	}
	for _, s := range skipped {
		a.Send(alerter.Alert{
			Priority:    priority,
			Title:       "Speech backend not usable",
			Description: fmt.Sprintf("%s: %s; voice messages are not transcribed by this model until it is fixed", s.model, s.reason),
			EventID:     "voice:config:" + s.model,
		})
	}
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
				"failed_model": t.Name(),
				"next_model":   f.transcribers[i+1].Name(),
				"error":        err.Error(),
			})
		}
	}
	return nil, lastErr
}

// DetectTranscriber builds the transcription backend from the models ticked
// Transcription (STT) on the Task models page, in page order. Every enabled
// model_list entry of such a model is usable when SpeechBackend accepts its
// provider; the usable ones form an ordered fallback chain. No usable entry
// means transcription is off. The result raises no operator alerts; the
// gateway uses DetectTranscriberWithAlerter.
func DetectTranscriber(cfg *config.Config) Transcriber {
	return DetectTranscriberWithAlerter(cfg, nil)
}

// DetectTranscriberWithAlerter is DetectTranscriber with an alerter that
// receives the alert raised when a provider rejects the key or account, and
// one alert per STT model that is skipped as unusable: urgent when no model
// is usable (transcription is off), normal when another one is.
func DetectTranscriberWithAlerter(cfg *config.Config, a alerter.Alerter) Transcriber {
	var chain []Transcriber
	var skipped []skippedSTT
	skip := func(alias, reason string) {
		logger.WarnCF("voice", "Speech model skipped: "+reason, map[string]any{
			"model": alias, "reason": reason,
		})
		skipped = append(skipped, skippedSTT{model: alias, reason: reason})
	}
	for _, alias := range cfg.TaskModels.ModelsFor(config.TaskUseSTT) {
		found := false
		for i := range cfg.Models {
			m := &cfg.Models[i]
			if m.ModelName != alias || !m.Enabled {
				continue
			}
			found = true
			p, err := cfg.GetProvider(m.Provider)
			if err != nil {
				skip(alias, "provider not configured")
				continue
			}
			backend, reason := SpeechBackend(p)
			if reason != "" {
				skip(alias, reason)
				continue
			}
			chain = append(chain, NewTranscriber(alias, m.Model, backend, a))
		}
		if !found {
			skip(alias, "model not configured or disabled")
		}
	}
	alertSkipped(a, skipped, len(chain) == 0)
	switch len(chain) {
	case 0:
		return nil
	case 1:
		return chain[0]
	default:
		return &fallbackTranscriber{transcribers: chain}
	}
}
