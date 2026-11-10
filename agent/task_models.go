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

// taskAttemptTimeout bounds one model call of a task when neither the model
// nor agents.defaults sets a request_timeout.
const taskAttemptTimeout = 5 * time.Minute

// taskTempPrefix names the empty folder a CLI text call runs in, under the
// temporary agents' root.
const taskTempPrefix = "task-"

// taskModelHost runs agent_spawn's task-model calls (TaskModelRunner): one
// call per model, never a turn, never tools. Candidates are the kind's list
// on the Task models page, in order (or the one named), skipping those in
// cooldown and falling back on failure. Output is written in the caller's
// workspace (a clone's is its source's) under files/generated/<date>/.
type taskModelHost struct {
	al *AgentLoop
	// httpClient is used by the speech and image clients; nil is the
	// default (no timeout of its own: each call has a context deadline).
	httpClient *http.Client
	// clone is the short id of the clone whose spawner this is, "" for any
	// other agent: its audit rows say so, as its tool calls' do.
	clone string
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

// taskInput is one input file. Check only opens and checks it; Run reads
// it in full (data).
type taskInput struct {
	name        string
	contentType string
	data        []byte
}

// imageTypes are the image formats, by extension.
var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp",
}

func (in taskInput) isImage() bool { return strings.HasPrefix(in.contentType, "image/") }

func (in taskInput) isAudio() bool {
	return strings.HasPrefix(in.contentType, "audio/") || in.contentType == "application/ogg" ||
		voice.AudioType(in.name) != ""
}

// isText reports whether the input reads as text: valid UTF-8 with no NUL
// byte, whatever type its name suggests (yaml, sh, json, …), unless it is
// an image or audio.
func (in taskInput) isText() bool {
	return !in.isImage() && !in.isAudio() && utf8.Valid(in.data) && !bytes.ContainsRune(in.data, 0)
}

// Check implements TaskModelRunner: every rule, the inputs opened and
// checked but not read.
func (h *taskModelHost) Check(_ context.Context, agentID, workspace string, req toolsagents.TaskModelRequest) error {
	_, _, err := h.prepare(agentID, workspace, req, false)
	return err
}

// prepare applies every rule a call must pass and returns the candidate
// model names and the inputs, read in full when read is set. The checks that
// need an input's bytes (text) run only then.
func (h *taskModelHost) prepare(agentID, workspace string, req toolsagents.TaskModelRequest, read bool) ([]string, []taskInput, error) {
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
	candidates := cfg.TaskModels.ModelsFor(use)
	if req.Model != "" {
		if !cfg.TaskModels.Has(req.Model, use) {
			return nil, nil, fmt.Errorf("Model %s is not in %s; add it on the Task models page.", req.Model, use.Label()) //nolint:staticcheck // ST1005: user-facing sentence
		}
		candidates = []string{strings.TrimSpace(req.Model)}
	}
	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("No model in %s; add one on the Task models page.", use.Label()) //nolint:staticcheck // ST1005: user-facing sentence
	}
	inputs, err := h.inputs(cfg, a, workspace, req, read)
	if err != nil {
		return nil, nil, err
	}
	if err := checkInputKinds(req.Kind, inputs, read); err != nil {
		return nil, nil, err
	}
	return candidates, inputs, nil
}

// checkInputKinds refuses inputs a kind can't use. Text needs the bytes, so
// it is checked only once they are read.
func checkInputKinds(kind string, inputs []taskInput, read bool) error {
	switch kind {
	case toolsagents.TaskKindSTT:
		if len(inputs) != 1 {
			return errors.New("Transcription needs exactly one audio file in files or media.") //nolint:staticcheck // ST1005: user-facing sentence
		}
		if !inputs[0].isAudio() {
			return fmt.Errorf("%s can't be transcribed: it is not an audio file.", inputs[0].name) //nolint:staticcheck // ST1005: user-facing sentence
		}
	case toolsagents.TaskKindTTS:
		if len(inputs) > 0 {
			return errors.New("Speech takes no files: give the text to speak as task.") //nolint:staticcheck // ST1005: user-facing sentence
		}
	case toolsagents.TaskKindText:
		for _, in := range inputs {
			if in.isAudio() || read && !in.isImage() && !in.isText() {
				return fmt.Errorf("%s can't be used for text: it is neither text nor an image.", in.name) //nolint:staticcheck // ST1005: user-facing sentence
			}
		}
	case toolsagents.TaskKindImage:
		for _, in := range inputs {
			if !in.isImage() {
				return fmt.Errorf("%s can't be used for images: it is not an image.", in.name) //nolint:staticcheck // ST1005: user-facing sentence
			}
		}
	}
	return nil
}

// inputs opens the request's files with the agent's file permission (as
// msg_send_file does: only for an agent granted a file-reading tool, each
// opened once and checked: the file checked, regular, within
// max_media_size) and resolves its media refs from the media store; with
// read, each is read in full.
func (h *taskModelHost) inputs(cfg *config.Config, a *config.AgentConfig, workspace string, req toolsagents.TaskModelRequest, read bool) ([]taskInput, error) {
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
			in := taskInput{name: filepath.Base(p)}
			var err error
			if read {
				in.data, err = readAtMost(f, maxSize)
			}
			sniffed := in.data
			if !read && err == nil {
				sniffed, err = sniff(f)
			}
			utils.CloseQuietly(f)
			if err != nil {
				return nil, fmt.Errorf("Can't use %q: %w.", p, err) //nolint:staticcheck // ST1005: user-facing sentence
			}
			in.contentType = contentTypeOf(in.name, sniffed)
			out = append(out, in)
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
		in := taskInput{name: meta.Filename}
		if in.name == "" {
			in.name = filepath.Base(path)
		}
		f, err := os.Open(path) //nolint:gosec // a path the media store resolved
		if err == nil {
			var info os.FileInfo
			if info, err = f.Stat(); err == nil && info.Size() > maxSize {
				err = fmt.Errorf("it is %d bytes, more than the limit of %d bytes", info.Size(), maxSize)
			}
			if err == nil && read {
				in.data, err = readAtMost(f, maxSize)
			}
			if err == nil {
				head := in.data
				if !read {
					head, err = sniff(f)
				}
				in.contentType = mediaType(meta.ContentType)
				if in.contentType == "" || in.contentType == "application/octet-stream" {
					in.contentType = contentTypeOf(in.name, head)
				}
			}
			utils.CloseQuietly(f)
		}
		if err != nil {
			return nil, fmt.Errorf("Can't use %s: %w.", ref, err) //nolint:staticcheck // ST1005: user-facing sentence
		}
		out = append(out, in)
	}
	return out, nil
}

// sniffLen is how much of a file Check reads to tell its type when neither
// its name nor a stored type does: what http.DetectContentType looks at.
const sniffLen = 512

// sniff reads the first sniffLen bytes of a file (fewer when it is
// shorter), the part type detection looks at.
func sniff(r io.Reader) ([]byte, error) {
	buf := make([]byte, sniffLen)
	n, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}

// readAtMost reads r to the end, refusing more than maxSize bytes (a file
// that grew after it was checked).
func readAtMost(r io.Reader, maxSize int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("it is more than the limit of %d bytes", maxSize)
	}
	return data, nil
}

// mediaType is a content type reduced to type/subtype, lower case; "" when
// it does not parse.
func mediaType(ct string) string {
	mt, _, err := mime.ParseMediaType(strings.TrimSpace(ct))
	if err != nil {
		return ""
	}
	return strings.ToLower(mt)
}

// contentTypeOf is the media type of a file: audio and images by their
// extension (an explicit table), else by the bytes when there are any, else
// "".
func contentTypeOf(name string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(name))
	if t := voice.AudioType(name); t != "" {
		return t
	}
	if t := imageTypes[ext]; t != "" {
		return t
	}
	if len(data) == 0 {
		return ""
	}
	switch t := mediaType(http.DetectContentType(data)); t {
	case "application/ogg":
		return "audio/ogg" // Ogg detected by its bytes: audio, as transcription APIs name it
	case "audio/wave":
		return "audio/wav"
	default:
		return t
	}
}

// Run implements TaskModelRunner.
func (h *taskModelHost) Run(ctx context.Context, agentID, workspace string, req toolsagents.TaskModelRequest) (*toolsagents.TaskModelResult, error) {
	res := &toolsagents.TaskModelResult{}
	candidates, inputs, err := h.prepare(agentID, workspace, req, true)
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
				if fe := providers.ClassifyError(callErr, mc.Provider, mc.Model); fe != nil && cooldownWorthy(fe) {
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

// cooldownWorthy reports whether a failure says the model can't serve
// anyone for a while (rate limit, overload, an outage, a timeout, the key
// or the account), so it is put in cooldown. A failure the request caused
// (a bad input, a format or size the model refused, an unknown model) is
// not: the model stays available to the agents chatting with it and to the
// next call.
func cooldownWorthy(fe *providers.FailoverError) bool {
	if fe.Status >= 500 {
		return true
	}
	switch fe.Reason {
	case providers.FailoverRateLimit, providers.FailoverOverloaded, providers.FailoverTimeout,
		providers.FailoverAuth, providers.FailoverBilling:
		return true
	default:
		return false
	}
}

// callOutput is what one model call returned.
type callOutput struct {
	text   string
	images []providers.GeneratedImage
	audio  []byte
	// audioType is the audio's media type, as the API declared it.
	audioType string
	// format is the audio format the call asked for (the API's terms).
	format string
	usage  *protocoltypes.TaskUsage
	cost   *float64
}

// call makes one model call of the kind.
func (h *taskModelHost) call(ctx context.Context, cfg *config.Config, agentID, alias string, mc *config.ModelConfig,
	prov *config.Provider, req toolsagents.TaskModelRequest, inputs []taskInput,
) (*callOutput, error) {
	callCtx, cancel := context.WithTimeout(ctx, attemptTimeout(cfg, mc))
	defer cancel()
	switch req.Kind {
	case toolsagents.TaskKindTTS:
		return h.speak(callCtx, cfg, alias, mc, prov, req)
	case toolsagents.TaskKindSTT:
		return h.transcribe(callCtx, mc, prov, req, inputs[0])
	case toolsagents.TaskKindImage:
		entry, _ := cfg.TaskModels.Entry(config.TaskUseImage, alias)
		if entry.EffectiveImageAPI() == config.ImageAPIImages {
			return h.imagesAPI(callCtx, mc, prov, req, inputs)
		}
		return h.chat(callCtx, cfg, agentID, alias, mc, prov, req, inputs, true)
	default:
		return h.chat(callCtx, cfg, agentID, alias, mc, prov, req, inputs, false)
	}
}

// attemptTimeout is how long one model call of a task may take: the
// model's request_timeout, else agents.defaults.request_timeout, else
// taskAttemptTimeout.
func attemptTimeout(cfg *config.Config, mc *config.ModelConfig) time.Duration {
	switch {
	case mc.RequestTimeout > 0:
		return time.Duration(mc.RequestTimeout) * time.Second
	case cfg.Agents.Defaults.RequestTimeout > 0:
		return time.Duration(cfg.Agents.Defaults.RequestTimeout) * time.Second
	default:
		return taskAttemptTimeout
	}
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
	if e, _ := cfg.TaskModels.Entry(config.TaskUseTTS, alias); e.TTSDefaults != nil {
		d := e.TTSDefaults
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
	out := &callOutput{audio: sp.Audio, audioType: sp.ContentType, format: format, usage: sp.Usage, cost: sp.CostUSD}
	if len(sp.Audio) == 0 {
		return out, errors.New("the model returned no audio")
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
		switch audio, rate := audioKind(out.audioType, out.format); audio {
		case "pcm":
			data, ext = wavFromPCM(out.audio, rate), ".wav"
		case "ulaw":
			ext = ".ulaw"
		default:
			if ext == ".bin" {
				ext = audioExtension(formatType(out.format))
			}
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
			// The model's words beside the images: kept like a text result.
			rel, err := saveGenerated(h.al, cfg, workspace, "text", ".md", []byte(out.text))
			if err != nil {
				return err
			}
			res.Paths = append(res.Paths, rel)
			res.Text = cutText(out.text, cfg.TaskModels.EffectiveResultTextChars(), rel)
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

// audioKind says how synthesized audio is saved: "pcm" (raw 16-bit
// little-endian mono PCM, wrapped in WAV at rate), "ulaw" (raw µ-law, saved
// as it is) or "" (a container format, saved as it is). The content type
// decides (audio/L16 as Gemini returns it, with its rate); when it does not
// tell, the format asked for does (ElevenLabs pcm_16000, ulaw_8000; OpenAI
// pcm, 24 kHz).
func audioKind(contentType, format string) (string, int) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = ""
	}
	mt = strings.ToLower(mt)
	f := strings.ToLower(strings.TrimSpace(format))
	formatRate := func(def int) int {
		if _, r, ok := strings.Cut(f, "_"); ok {
			if n, err := strconv.Atoi(r); err == nil && n > 0 {
				return n
			}
		}
		return def
	}
	switch {
	case mt == "audio/l16" || mt == "audio/pcm":
		if r, err := strconv.Atoi(params["rate"]); err == nil && r > 0 {
			return "pcm", r
		}
		return "pcm", formatRate(24000)
	case mt == "audio/basic" || mt == "audio/x-mulaw" || mt == "audio/mulaw" || mt == "audio/ulaw":
		return "ulaw", 0
	case mt != "" && mt != "application/octet-stream":
		return "", 0
	case f == "pcm" || strings.HasPrefix(f, "pcm_"):
		return "pcm", formatRate(24000)
	case strings.HasPrefix(f, "ulaw_"):
		return "ulaw", 0
	}
	return "", 0
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
	details := map[string]any{"kind": kind, "model": model}
	if h.clone != "" {
		details["clone"] = h.clone
	}
	b, err := json.Marshal(details)
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
