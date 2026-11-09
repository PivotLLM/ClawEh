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
	Transcribe(ctx context.Context, audio AudioFile) (*TranscriptionResponse, error)
}

// AudioFile is one audio file to transcribe: where it is, and the name and
// media type it came with (a voice note's own name and type, which the media
// store keeps beside a path that may have no extension). The APIs need the
// format: the name's extension, else the type's.
type AudioFile struct {
	Path        string
	Filename    string
	ContentType string
}

// audioTypes are the audio formats a transcription takes, by extension: an
// explicit table, so the result does not depend on the host's mime.types.
var audioTypes = map[string]string{
	".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg",
	".mp3": "audio/mpeg", ".mpga": "audio/mpeg", ".mpeg": "audio/mpeg",
	".m4a": "audio/mp4", ".mp4": "audio/mp4",
	".wav": "audio/wav", ".flac": "audio/flac",
	".webm": "audio/webm", ".weba": "audio/webm",
	".aac": "audio/aac", ".amr": "audio/amr",
}

// AudioType is the media type of an audio file name by its extension, ""
// when the extension is not a known audio format.
func AudioType(name string) string {
	return audioTypes[strings.ToLower(filepath.Ext(name))]
}

// upload is the file name and media type an audio file is sent with: its
// own name (else the path's), and its own type reduced to type/subtype, else
// the one its name's extension gives.
func (a AudioFile) upload() (string, string) {
	name := filepath.Base(a.Filename)
	if a.Filename == "" {
		name = filepath.Base(a.Path)
	}
	ct := ""
	if mt, _, err := mime.ParseMediaType(strings.TrimSpace(a.ContentType)); err == nil && mt != "application/octet-stream" {
		ct = strings.ToLower(mt)
	}
	if ct == "" {
		ct = AudioType(name)
	}
	if ct == "" {
		ct = AudioType(a.Path)
	}
	if filepath.Ext(name) == "" && filepath.Ext(a.Path) != "" && ct == AudioType(a.Path) {
		name = filepath.Base(a.Path)
	}
	return name, ct
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
func (t *speechTranscriber) Transcribe(ctx context.Context, audio AudioFile) (*TranscriptionResponse, error) {
	logger.InfoCF("voice", "Starting transcription", map[string]any{"audio_file": audio.Path, "model": t.name})
	f, err := os.Open(audio.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to open audio file: %w", err)
	}
	defer utils.CloseQuietly(f)

	callCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	start := time.Now()
	filename, contentType := audio.upload()
	res, err := t.backend.Transcribe(callCtx, &speech.TranscribeRequest{
		Model:       t.model,
		Audio:       f,
		Filename:    filename,
		ContentType: contentType,
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

// alertSkipped raises one alert per skipped model.
func alertSkipped(a alerter.Alerter, skipped []skippedSTT) {
	if a == nil {
		return
	}
	for _, s := range skipped {
		a.Send(alerter.Alert{
			Priority:    alerter.Normal,
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

func (f *fallbackTranscriber) Transcribe(ctx context.Context, audio AudioFile) (*TranscriptionResponse, error) {
	var lastErr error
	for i, t := range f.transcribers {
		result, err := t.Transcribe(ctx, audio)
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
	for _, e := range sttEntries(cfg) {
		if e.problem == "" {
			chain = append(chain, NewTranscriber(e.alias, e.model, e.backend, a))
			continue
		}
		logger.WarnCF("voice", "Speech model skipped: "+e.problem, map[string]any{
			"model": e.alias, "reason": e.problem,
		})
		// A model the operator disabled is a choice, not a fault: logged,
		// never alerted.
		if !e.disabled {
			skipped = append(skipped, skippedSTT{model: e.alias, reason: e.problem})
		}
	}
	alertSkipped(a, skipped)
	switch len(chain) {
	case 0:
		return nil
	case 1:
		return chain[0]
	default:
		return &fallbackTranscriber{transcribers: chain}
	}
}

// sttEntry is one model_list entry of an STT task model as the transcriber
// would use it: its backend, or the problem that keeps it out.
type sttEntry struct {
	alias, model string
	backend      SpeechClient
	problem      string
	// disabled: the model is on the Models page but switched off.
	disabled bool
}

// sttEntries resolves the STT task models, in page order, into the entries
// the transcriber would use, without logging or alerting: a name with no
// enabled model_list entry is one entry with its problem.
func sttEntries(cfg *config.Config) []sttEntry {
	var out []sttEntry
	for _, alias := range cfg.TaskModels.ModelsFor(config.TaskUseSTT) {
		exists, enabled := false, false
		for i := range cfg.Models {
			m := &cfg.Models[i]
			if m.ModelName != alias {
				continue
			}
			exists = true
			if !m.Enabled {
				continue
			}
			enabled = true
			e := sttEntry{alias: alias, model: m.Model}
			if p, err := cfg.GetProvider(m.Provider); err != nil {
				e.problem = "provider not configured"
			} else {
				e.backend, e.problem = SpeechBackend(p)
			}
			out = append(out, e)
		}
		switch {
		case !exists:
			out = append(out, sttEntry{alias: alias, problem: "not on the Models page"})
		case !enabled:
			out = append(out, sttEntry{alias: alias, problem: "disabled on the Models page", disabled: true})
		}
	}
	return out
}

// STTStatus is what the transcriber would do with the STT task models,
// worked out without logging or alerting (for Check Up and the report):
// the models in the chain, in order, and the others with why each is not
// used.
type STTStatus struct {
	Usable   []string
	Unusable []UnusableModel
}

// UnusableModel is a ticked task model that is not used, and why.
type UnusableModel struct {
	Model, Reason string
}

// TranscriptionStatus reports the STT task models' state; see STTStatus.
func TranscriptionStatus(cfg *config.Config) STTStatus {
	var st STTStatus
	for _, e := range sttEntries(cfg) {
		if e.problem == "" {
			st.Usable = append(st.Usable, e.alias)
		} else {
			st.Unusable = append(st.Unusable, UnusableModel{Model: e.alias, Reason: e.problem})
		}
	}
	return st
}
