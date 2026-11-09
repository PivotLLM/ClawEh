// ClawEh
// License: MIT

package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PivotLLM/spawnllm/imagegen"
	"github.com/PivotLLM/spawnllm/protocoltypes"
	"github.com/PivotLLM/spawnllm/speech"
	"github.com/google/uuid"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
	"github.com/PivotLLM/ClawEh/tools/files"
	toolsmsg "github.com/PivotLLM/ClawEh/tools/msg"
	"github.com/PivotLLM/ClawEh/utils"
	"github.com/PivotLLM/ClawEh/voice"
)

// taskAttemptTimeout bounds one model call of a task when the model sets no
// request_timeout of its own.
const taskAttemptTimeout = 5 * time.Minute

// taskTempPrefix names the empty folder a CLI text call runs in, under the
// temporary agents' root.
const taskTempPrefix = "task-"

// taskModelHost runs agent_spawn's task-model calls (TaskModelRunner): one
// call per model, never a turn, never tools. Candidates are the models
// ticked for the kind on the Task models page, in page order (or the one
// named), skipping those in cooldown and falling back on failure. Output is
// written in the caller's workspace (a clone's is its source's) under
// files/generated/<date>/.
type taskModelHost struct {
	al *AgentLoop
	// httpClient is used by the speech and image clients; nil is the
	// default (no timeout of its own: each call has a context deadline).
	httpClient *http.Client
}

var _ toolsagents.TaskModelRunner = (*taskModelHost)(nil)

// kindUse maps a spawn kind to the Task models use it needs.
func kindUse(kind string) config.TaskUse {
	switch kind {
	case toolsagents.TaskKindImage:
		return config.TaskUseImage
	case toolsagents.TaskKindTTS:
		return config.TaskUseTTS
	case toolsagents.TaskKindSTT:
		return config.TaskUseSTT
	default:
		return config.TaskUseText
	}
}

// taskInput is one input file, read in full.
type taskInput struct {
	name        string
	contentType string
	data        []byte
}

func (in taskInput) isImage() bool { return strings.HasPrefix(in.contentType, "image/") }
func (in taskInput) isText() bool {
	return strings.HasPrefix(in.contentType, "text/") || in.contentType == "application/json" ||
		(in.contentType == "" || in.contentType == "application/octet-stream") && utf8.Valid(in.data)
}

// Check implements TaskModelRunner.
func (h *taskModelHost) Check(_ context.Context, agentID, workspace string, req toolsagents.TaskModelRequest) error {
	_, _, err := h.prepare(agentID, workspace, req)
	return err
}

// prepare applies every rule a call must pass and returns the candidate
// model names and the inputs read.
func (h *taskModelHost) prepare(agentID, workspace string, req toolsagents.TaskModelRequest) ([]string, []taskInput, error) {
	cfg := h.al.GetConfig()
	if cfg == nil {
		return nil, nil, errLoopNotReady
	}
	use := kindUse(req.Kind)
	a := cfg.AgentByID(agentID)
	name := agentID
	if a != nil {
		name = a.DisplayName()
	}
	if !a.TaskModelAllowed(use) {
		return nil, nil, fmt.Errorf("%s is not allowed to use task models for %s.", name, config.TaskSpawnKindName(use)) //nolint:staticcheck // ST1005: user-facing sentence
	}
	ticked := cfg.TaskModels.ModelsFor(use)
	candidates := ticked
	if req.Model != "" {
		if !cfg.TaskModels.Has(req.Model, use) {
			return nil, nil, fmt.Errorf("Model %s is not a task model for %s; tick it on the Task models page.", req.Model, config.TaskSpawnKindName(use)) //nolint:staticcheck // ST1005: user-facing sentence
		}
		candidates = []string{strings.TrimSpace(req.Model)}
	}
	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("No model is ticked for %s on the Task models page.", config.TaskSpawnKindName(use)) //nolint:staticcheck // ST1005: user-facing sentence
	}
	inputs, err := h.readInputs(cfg, a, workspace, req)
	if err != nil {
		return nil, nil, err
	}
	switch req.Kind {
	case toolsagents.TaskKindSTT:
		if len(inputs) != 1 {
			return nil, nil, errors.New("Transcription needs exactly one audio file in files or media.") //nolint:staticcheck // ST1005: user-facing sentence
		}
	case toolsagents.TaskKindTTS:
		if len(inputs) > 0 {
			return nil, nil, errors.New("Speech takes no files: give the text to speak as task.") //nolint:staticcheck // ST1005: user-facing sentence
		}
	case toolsagents.TaskKindText:
		for _, in := range inputs {
			if !in.isText() && !in.isImage() {
				return nil, nil, fmt.Errorf("%s can't be used for text: it is neither text nor an image.", in.name) //nolint:staticcheck // ST1005: user-facing sentence
			}
		}
	case toolsagents.TaskKindImage:
		for _, in := range inputs {
			if !in.isImage() {
				return nil, nil, fmt.Errorf("%s can't be used for images: it is not an image.", in.name) //nolint:staticcheck // ST1005: user-facing sentence
			}
		}
	}
	return candidates, inputs, nil
}

// readInputs reads the request's files with the agent's file permission
// (as msg_send_file does: only for an agent granted a file-reading tool,
// each opened once and checked) and its media refs from the media store.
func (h *taskModelHost) readInputs(cfg *config.Config, a *config.AgentConfig, workspace string, req toolsagents.TaskModelRequest) ([]taskInput, error) {
	maxSize := int64(cfg.Agents.Defaults.GetMaxMediaSize())
	var out []taskInput
	if len(req.Files) > 0 {
		if a == nil || !toolsmsg.CanReadFiles(a) {
			return nil, errors.New("You can only use files you're allowed to read.") //nolint:staticcheck // ST1005: user-facing sentence
		}
		reader := files.NewReader(cfg, workspace)
		for _, p := range req.Files {
			f, _, problem := toolsmsg.OpenReadable(reader, p, maxSize, "use")
			if problem != "" {
				return nil, errors.New(problem)
			}
			data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
			utils.CloseQuietly(f)
			if err != nil {
				return nil, fmt.Errorf("Can't use %q: %w.", p, err) //nolint:staticcheck // ST1005: user-facing sentence
			}
			out = append(out, taskInput{name: filepath.Base(p), contentType: contentTypeOf(p, data), data: data})
		}
	}
	for _, ref := range req.Media {
		if h.al.mediaStore == nil {
			return nil, fmt.Errorf("Can't use %s: attachments are not available.", ref) //nolint:staticcheck // ST1005: user-facing sentence
		}
		path, meta, err := h.al.mediaStore.ResolveWithMeta(ref)
		if err != nil {
			return nil, fmt.Errorf("Can't use %s: it is no longer available.", ref) //nolint:staticcheck // ST1005: user-facing sentence
		}
		data, err := readLimited(path, maxSize)
		if err != nil {
			return nil, fmt.Errorf("Can't use %s: %w.", ref, err) //nolint:staticcheck // ST1005: user-facing sentence
		}
		name := meta.Filename
		if name == "" {
			name = filepath.Base(path)
		}
		ct := meta.ContentType
		if ct == "" {
			ct = contentTypeOf(name, data)
		}
		out = append(out, taskInput{name: name, contentType: ct, data: data})
	}
	return out, nil
}

// readLimited reads a media-store file of at most maxSize bytes.
func readLimited(path string, maxSize int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // a path the media store resolved
	if err != nil {
		return nil, err
	}
	defer utils.CloseQuietly(f)
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("more than the limit of %d bytes", maxSize)
	}
	return data, nil
}

// contentTypeOf is the media type of a file: by its extension, else by its
// bytes, without parameters.
func contentTypeOf(name string, data []byte) string {
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	ct, _, _ = strings.Cut(ct, ";")
	return strings.TrimSpace(ct)
}

// Run implements TaskModelRunner.
func (h *taskModelHost) Run(ctx context.Context, agentID, workspace string, req toolsagents.TaskModelRequest) (*toolsagents.TaskModelResult, error) {
	res := &toolsagents.TaskModelResult{}
	candidates, inputs, err := h.prepare(agentID, workspace, req)
	if err != nil {
		return res, err
	}
	cfg := h.al.GetConfig()
	tracker := h.al.cooldownTracker()
	for _, alias := range candidates {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		mc, err := cfg.GetModelConfig(alias)
		if err != nil || mc == nil {
			res.Attempts = append(res.Attempts, toolsagents.TaskAttempt{Model: alias, Error: "not configured or disabled on the Models page"})
			continue
		}
		prov, err := cfg.GetProvider(mc.Provider)
		if err != nil || prov == nil {
			res.Attempts = append(res.Attempts, toolsagents.TaskAttempt{Model: alias, Error: "its provider is not configured"})
			continue
		}
		if tracker != nil && !tracker.IsAvailable(mc.Provider, mc.Model) {
			wait := tracker.CooldownRemaining(mc.Provider, mc.Model).Round(time.Second)
			res.Attempts = append(res.Attempts, toolsagents.TaskAttempt{Model: alias, Error: fmt.Sprintf("in cooldown for %s", wait)})
			continue
		}
		start := h.al.clk().Now()
		out, callErr := h.call(ctx, cfg, agentID, alias, mc, prov, req, inputs)
		elapsed := h.al.clk().Since(start)
		if out != nil && out.cost != nil {
			h.al.recordSpend(*out.cost)
		}
		fields := map[string]any{
			"agent_id": agentID, "kind": req.Kind, "model": alias, "duration_ms": elapsed.Milliseconds(),
		}
		if callErr != nil {
			if ctx.Err() == nil && tracker != nil {
				if fe := providers.ClassifyError(callErr, mc.Provider, mc.Model); fe != nil {
					tracker.MarkFailure(mc.Provider, mc.Model, fe.Reason, fe.Status, fe.RetryAfter)
				}
			}
			fields["outcome"], fields["error"] = "error", redactedError(callErr)
			logger.InfoCF("agent", "Task model call", fields)
			h.audit(agentID, req.Kind, alias, audit.OutcomeError, redactSecrets(callErr.Error()))
			res.Attempts = append(res.Attempts, toolsagents.TaskAttempt{Model: alias, Error: redactSecrets(callErr.Error())})
			continue
		}
		if tracker != nil {
			tracker.MarkSuccess(mc.Provider, mc.Model)
		}
		saveErr := h.save(cfg, workspace, req.Kind, out, res)
		fields["outcome"], fields["usage"], fields["paths"] = "ok", usageText(out.usage, out.images), res.Paths
		if out.cost != nil {
			fields["cost_usd"] = *out.cost
		}
		if saveErr != nil {
			fields["outcome"], fields["error"] = "not saved", saveErr.Error()
		}
		logger.InfoCF("agent", "Task model call", fields)
		res.Model, res.Usage, res.CostUSD = alias, usageText(out.usage, out.images), out.cost
		if saveErr != nil {
			h.audit(agentID, req.Kind, alias, audit.OutcomeError, saveErr.Error())
			return res, fmt.Errorf("the output could not be saved: %w", saveErr)
		}
		h.audit(agentID, req.Kind, alias, audit.OutcomeOK, strings.Join(res.Paths, ", "))
		return res, nil
	}
	return res, errors.New("no task model served this call")
}

// callOutput is what one model call returned.
type callOutput struct {
	text   string
	images []providers.GeneratedImage
	audio  []byte
	// audioType is the audio's media type, as the API declared it.
	audioType string
	usage     *protocoltypes.TaskUsage
	cost      *float64
}

// call makes one model call of the kind.
func (h *taskModelHost) call(ctx context.Context, cfg *config.Config, agentID, alias string, mc *config.ModelConfig,
	prov *config.Provider, req toolsagents.TaskModelRequest, inputs []taskInput,
) (*callOutput, error) {
	timeout := taskAttemptTimeout
	if mc.RequestTimeout > 0 {
		timeout = time.Duration(mc.RequestTimeout) * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch req.Kind {
	case toolsagents.TaskKindTTS:
		return h.speak(callCtx, cfg, alias, mc, prov, req)
	case toolsagents.TaskKindSTT:
		return h.transcribe(callCtx, mc, prov, req, inputs[0])
	case toolsagents.TaskKindImage:
		entry := taskModelEntry(cfg, alias)
		if entry.EffectiveImageAPI() == config.ImageAPIImages {
			return h.imagesAPI(callCtx, mc, prov, req, inputs)
		}
		return h.chat(callCtx, cfg, agentID, alias, mc, prov, req, inputs, true)
	default:
		return h.chat(callCtx, cfg, agentID, alias, mc, prov, req, inputs, false)
	}
}

// taskModelEntry is the Task models row of alias.
func taskModelEntry(cfg *config.Config, alias string) config.TaskModel {
	for _, m := range cfg.TaskModels.Models {
		if strings.TrimSpace(m.Model) == alias {
			return m
		}
	}
	return config.TaskModel{Model: alias}
}

// chat is one dispatcher Chat with no tool definitions: the prompt, text
// inputs added as text, images attached. wantImages makes it an image call.
func (h *taskModelHost) chat(ctx context.Context, cfg *config.Config, agentID, alias string, mc *config.ModelConfig,
	prov *config.Provider, req toolsagents.TaskModelRequest, inputs []taskInput, wantImages bool,
) (*callOutput, error) {
	if config.TaskOnlyProtocol(prov.Protocol) {
		return nil, fmt.Errorf("provider %s serves speech only", prov.Name)
	}
	if h.al.dispatcher == nil {
		return nil, errors.New("no model dispatcher")
	}
	var provider providers.LLMProvider
	var err error
	if config.CLIAgentByProtocol(prov.Protocol) != nil {
		dir, cleanup, derr := h.isolatedFolder(cfg, agentID)
		if derr != nil {
			return nil, derr
		}
		defer cleanup()
		provider, err = h.al.dispatcher.GetIsolated(alias, dir)
	} else {
		provider, err = h.al.dispatcher.Get(alias)
	}
	if err != nil {
		return nil, err
	}
	var content strings.Builder
	content.WriteString(req.Prompt)
	var media []string
	for _, in := range inputs {
		if in.isImage() {
			media = append(media, "data:"+in.contentType+";base64,"+base64.StdEncoding.EncodeToString(in.data))
			continue
		}
		fmt.Fprintf(&content, "\n\n--- %s ---\n%s", in.name, string(in.data))
	}
	resp, err := provider.Chat(ctx, []providers.Message{{Role: "user", Content: content.String(), Media: media}}, nil, mc.Model, nil)
	out := &callOutput{}
	if resp != nil && resp.Status != nil {
		out.cost = resp.Status.CostUSD
	}
	if err != nil {
		return out, err
	}
	if resp == nil {
		return out, errors.New("the model returned no reply")
	}
	if resp.Usage != nil {
		out.usage = &protocoltypes.TaskUsage{InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens, TotalTokens: resp.Usage.TotalTokens}
	}
	out.text, out.images = resp.Content, resp.Images
	if wantImages && len(resp.Images) == 0 {
		return out, errors.New("the model returned no image")
	}
	if !wantImages && strings.TrimSpace(resp.Content) == "" {
		return out, errors.New("the model returned no text")
	}
	return out, nil
}

// imagesAPI calls the OpenAI-style image endpoints: an edit with input
// images, else a generation.
func (h *taskModelHost) imagesAPI(ctx context.Context, mc *config.ModelConfig, prov *config.Provider,
	req toolsagents.TaskModelRequest, inputs []taskInput,
) (*callOutput, error) {
	if prov.Protocol != "openai-chat" && prov.Protocol != "openai-responses" {
		return nil, fmt.Errorf("provider %s has no OpenAI-style image API", prov.Name)
	}
	client := imagegen.NewOpenAI(prov.BaseURL, prov.APIKey, imagegen.WithHTTPClient(h.httpClient))
	var r *imagegen.Result
	var err error
	if len(inputs) > 0 {
		imgs := make([]imagegen.InputImage, 0, len(inputs))
		for _, in := range inputs {
			imgs = append(imgs, imagegen.InputImage{Data: bytes.NewReader(in.data), Filename: in.name, ContentType: in.contentType})
		}
		r, err = client.Edit(ctx, &imagegen.EditRequest{Model: mc.Model, Prompt: req.Prompt, Images: imgs})
	} else {
		r, err = client.Generate(ctx, &imagegen.GenerateRequest{Model: mc.Model, Prompt: req.Prompt})
	}
	if err != nil {
		return nil, err
	}
	if len(r.Images) == 0 {
		return &callOutput{usage: r.Usage, cost: r.CostUSD}, errors.New("the model returned no image")
	}
	return &callOutput{images: r.Images, usage: r.Usage, cost: r.CostUSD}, nil
}

// speak synthesizes the prompt with the call's voice and format, else the
// model's tts_defaults.
func (h *taskModelHost) speak(ctx context.Context, cfg *config.Config, alias string, mc *config.ModelConfig,
	prov *config.Provider, req toolsagents.TaskModelRequest,
) (*callOutput, error) {
	backend, problem := h.speechBackend(prov)
	if problem != "" {
		return nil, errors.New(problem)
	}
	voiceName, format := req.Voice, req.Format
	if d := taskModelEntry(cfg, alias).TTSDefaults; d != nil {
		if voiceName == "" {
			voiceName = d.Voice
		}
		if format == "" {
			format = d.Format
		}
	}
	sp, err := backend.Synthesize(ctx, &speech.SynthesizeRequest{Model: mc.Model, Text: req.Prompt, Voice: voiceName, Format: format})
	if err != nil {
		return nil, err
	}
	out := &callOutput{audio: sp.Audio, audioType: sp.ContentType, usage: sp.Usage, cost: sp.CostUSD}
	if len(sp.Audio) == 0 {
		return out, errors.New("the model returned no audio")
	}
	if out.audioType == "" {
		out.audioType = formatType(format)
	}
	return out, nil
}

// transcribe sends the one audio input to the model.
func (h *taskModelHost) transcribe(ctx context.Context, mc *config.ModelConfig, prov *config.Provider,
	req toolsagents.TaskModelRequest, in taskInput,
) (*callOutput, error) {
	backend, problem := h.speechBackend(prov)
	if problem != "" {
		return nil, errors.New(problem)
	}
	tr, err := backend.Transcribe(ctx, &speech.TranscribeRequest{
		Model: mc.Model, Audio: bytes.NewReader(in.data), Filename: in.name,
		ContentType: in.contentType, Prompt: req.Prompt,
	})
	if err != nil {
		return nil, err
	}
	return &callOutput{text: tr.Text, usage: tr.Usage, cost: tr.CostUSD}, nil
}

// speechBackend is voice.SpeechBackend with the host's HTTP client.
func (h *taskModelHost) speechBackend(prov *config.Provider) (voice.SpeechClient, string) {
	if h.httpClient == nil {
		return voice.SpeechBackend(prov)
	}
	return voice.SpeechBackendWith(prov, speech.WithHTTPClient(h.httpClient))
}

// save writes the call's output in the workspace and fills the result:
// text and transcripts as .md (inline up to result_text_chars), images and
// audio in their own formats (paths only).
func (h *taskModelHost) save(cfg *config.Config, workspace, kind string, out *callOutput, res *toolsagents.TaskModelResult) error {
	switch kind {
	case toolsagents.TaskKindTTS:
		data, ext := out.audio, audioExtension(out.audioType)
		if rate, ok := pcmRate(out.audioType); ok {
			data, ext = wavFromPCM(out.audio, rate), ".wav"
		}
		rel, err := saveGenerated(h.al, cfg, workspace, "speech", ext, data)
		if err != nil {
			return err
		}
		res.Paths = append(res.Paths, rel)
	case toolsagents.TaskKindImage:
		for _, img := range out.images {
			if len(img.Data) == 0 {
				if img.URL != "" && !strings.HasPrefix(strings.ToLower(img.URL), "data:") {
					res.URLs = append(res.URLs, capImageURL(img.URL))
				}
				continue
			}
			rel, err := saveGenerated(h.al, cfg, workspace, "image", imageExtension(img), img.Data)
			if err != nil {
				return err
			}
			res.Paths = append(res.Paths, rel)
		}
		if strings.TrimSpace(out.text) != "" {
			res.Text = cutText(out.text, cfg.TaskModels.EffectiveResultTextChars(), "")
		}
	default: // text, stt
		prefix := "text"
		if kind == toolsagents.TaskKindSTT {
			prefix = "transcript"
		}
		rel, err := saveGenerated(h.al, cfg, workspace, prefix, ".md", []byte(out.text))
		if err != nil {
			return err
		}
		res.Paths = append(res.Paths, rel)
		res.Text = cutText(out.text, cfg.TaskModels.EffectiveResultTextChars(), rel)
	}
	return nil
}

// cutText is text cut to limit characters, marked as cut and naming the
// file holding all of it.
func cutText(text string, limit int, path string) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	marker := "\n[cut at " + strconv.Itoa(limit) + " characters"
	if path != "" {
		marker += "; full text in " + path
	}
	return string(runes[:limit]) + marker + "]"
}

// saveGenerated writes data in workspace under files/generated/<date>/ as
// <prefix>-<time>-<rand><ext>, with the area's exact modes, and returns
// the workspace path.
func saveGenerated(al *AgentLoop, cfg *config.Config, workspace, prefix, ext string, data []byte) (string, error) {
	now := al.clk().Now()
	name := fmt.Sprintf("%s-%s%03d-%s%s", prefix, now.Format("150405"), now.Nanosecond()/1e6, uuid.NewString()[:8], ext)
	rel := generatedImagesDir + "/" + now.Format("2006-01-02") + "/" + name
	full := filepath.Join(workspace, filepath.FromSlash(rel))
	modes := agentModes(cfg, full)
	if err := fileutil.MkdirAllExact(filepath.Dir(full), modes.Dir); err != nil {
		return "", err
	}
	if err := fileutil.WriteFileExact(full, data, modes.File); err != nil {
		return "", err
	}
	return rel, nil
}

// pcmRate reports whether a media type is raw 16-bit PCM (audio/L16, as
// Gemini returns it) and its sample rate (24000 when not given).
func pcmRate(contentType string) (int, bool) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mt, "audio/l16") && !strings.EqualFold(mt, "audio/pcm") {
		return 0, false
	}
	rate := 24000
	if r, err := strconv.Atoi(params["rate"]); err == nil && r > 0 {
		rate = r
	}
	return rate, true
}

// wavFromPCM wraps 16-bit signed little-endian mono PCM in a WAV header.
func wavFromPCM(pcm []byte, rate int) []byte {
	const channels, bits = 1, 16
	b := make([]byte, 0, 44+len(pcm))
	put32 := func(v int) { b = binary.LittleEndian.AppendUint32(b, uint32(v)) } //nolint:gosec // sizes of one generated audio file
	put16 := func(v int) { b = binary.LittleEndian.AppendUint16(b, uint16(v)) } //nolint:gosec // constant header fields
	b = append(b, "RIFF"...)
	put32(36 + len(pcm))
	b = append(b, "WAVEfmt "...)
	put32(16)
	put16(1) // PCM
	put16(channels)
	put32(rate)
	put32(rate * channels * bits / 8)
	put16(channels * bits / 8)
	put16(bits)
	b = append(b, "data"...)
	put32(len(pcm))
	return append(b, pcm...)
}

// audioExtension is the file extension for an audio media type.
func audioExtension(contentType string) string {
	mt, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(contentType)), ";")
	switch mt {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/ogg":
		return ".ogg"
	case "audio/opus":
		return ".opus"
	case "audio/aac":
		return ".aac"
	case "audio/flac":
		return ".flac"
	}
	return ".bin"
}

// formatType guesses the media type of an OpenAI-style or ElevenLabs format
// name, for an API that declared none.
func formatType(format string) string {
	f := strings.ToLower(format)
	switch {
	case strings.HasPrefix(f, "mp3"):
		return "audio/mpeg"
	case f == "wav":
		return "audio/wav"
	case f == "opus":
		return "audio/opus"
	case f == "aac":
		return "audio/aac"
	case f == "flac":
		return "audio/flac"
	}
	return ""
}

// usageText is usage as a short phrase.
func usageText(u *protocoltypes.TaskUsage, images []providers.GeneratedImage) string {
	var parts []string
	if u != nil {
		if u.InputTokens > 0 || u.OutputTokens > 0 {
			parts = append(parts, fmt.Sprintf("%d input and %d output tokens", u.InputTokens, u.OutputTokens))
		} else if u.TotalTokens > 0 {
			parts = append(parts, fmt.Sprintf("%d tokens", u.TotalTokens))
		}
		if u.Seconds > 0 {
			parts = append(parts, strconv.FormatFloat(u.Seconds, 'f', -1, 64)+" seconds of audio")
		}
		if u.Characters > 0 {
			parts = append(parts, fmt.Sprintf("%d characters", u.Characters))
		}
	}
	if len(images) > 0 {
		parts = append(parts, fmt.Sprintf("%d image(s)", len(images)))
	}
	return strings.Join(parts, ", ")
}

// isolatedFolder makes the empty folder one CLI text call runs in, under the
// temporary agents' root, with that root's exact modes, and returns its
// removal.
func (h *taskModelHost) isolatedFolder(cfg *config.Config, agentID string) (string, func(), error) {
	r := h.al.GetRegistry()
	if r == nil {
		return "", nil, errLoopNotReady
	}
	root := r.TempRoot()
	if root == "" {
		return "", nil, errors.New("no folder for an isolated CLI call in this process")
	}
	modes := cfg.AgentModes(root)
	if err := fileutil.MkdirAllExact(root, modes.Dir); err != nil {
		return "", nil, fmt.Errorf("folder for an isolated CLI call: %w", err)
	}
	dir, err := os.MkdirTemp(root, taskTempPrefix+agentID+"-")
	if err != nil {
		return "", nil, fmt.Errorf("folder for an isolated CLI call: %w", err)
	}
	if err := os.Chmod(dir, modes.Dir); err != nil {
		return "", nil, fmt.Errorf("folder for an isolated CLI call: %w", errors.Join(err, os.RemoveAll(dir)))
	}
	return dir, func() {
		if err := os.RemoveAll(dir); err != nil {
			logger.WarnCF("agent", "Could not remove a task model's CLI folder", map[string]any{
				"agent_id": agentID, "path": dir, "error": err.Error(),
			})
		}
	}, nil
}

// audit records one task-model call: kind task_model under the agent, the
// model as the tool, the files written (or the error) as the summary.
func (h *taskModelHost) audit(agentID, kind, model, outcome, summary string) {
	store := audit.Default()
	if store == nil {
		return
	}
	b, err := json.Marshal(map[string]any{"kind": kind, "model": model})
	if err != nil {
		b = []byte(`{}`)
	}
	store.Record(audit.Event{
		Kind:    audit.KindTaskModel,
		Agent:   agentID,
		Tool:    kind,
		Summary: summary,
		Details: string(b),
		Outcome: outcome,
	})
}
