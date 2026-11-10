// ClawEh
// License: MIT

package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// TaskModelsConfig is the Task models page: the models ClawEh and its agents
// use for single calls (one input, one output, never a conversation, never
// tools), in one ordered list per use. A list's order is that use's fallback
// order; a model may be in several lists, at most once in each.
//
// Code outside this package reads a use's models only through Entries (or
// ModelsFor and the helpers built on it), never through the per-use fields:
// the guard TestTaskModelLists_OnlyReadThroughEntries enforces it.
type TaskModelsConfig struct {
	// ResultTextChars is how much text a task result carries inline; longer
	// text is cut. 0 or absent uses DefaultTaskResultTextChars; negative is
	// refused at load and on save.
	ResultTextChars int `json:"result_text_chars,omitempty"`
	// EchoTranscription sends the transcript of an incoming voice message
	// back to the sender before the agent answers.
	EchoTranscription bool `json:"echo_transcription,omitempty" env:"CLAW_TASK_MODELS_ECHO_TRANSCRIPTION"`

	TextModels     []TaskModelRef   `json:"text,omitempty"`
	ImageModels    []ImageTaskModel `json:"image,omitempty"`
	TTSModels      []TTSTaskModel   `json:"tts,omitempty"`
	STTModels      []TaskModelRef   `json:"stt,omitempty"`
	VisionModels   []TaskModelRef   `json:"vision,omitempty"`
	ClassifyModels []TaskModelRef   `json:"classify,omitempty"`
}

// TaskModelRef is an entry of a list whose entries have no settings: a
// model_list[].model_name.
type TaskModelRef struct {
	Model string `json:"model"`
}

// ImageTaskModel is an entry of the image list.
type ImageTaskModel struct {
	Model string `json:"model"`
	// ImageAPI is how the model is called: "chat" (the default: image output
	// of a chat call) or "images" (the OpenAI-style image endpoints,
	// /images/generations and /images/edits; OpenRouter's /images).
	ImageAPI string `json:"image_api,omitempty"`
}

// TTSTaskModel is an entry of the tts list.
type TTSTaskModel struct {
	Model string `json:"model"`
	// TTSDefaults are the voice and format a speech call uses unless the
	// caller names its own.
	TTSDefaults *TTSDefaults `json:"tts_defaults,omitempty"`
}

// TTSDefaults is a TTS model's default voice and audio format.
type TTSDefaults struct {
	Voice  string `json:"voice,omitempty"`
	Format string `json:"format,omitempty"`
}

// TaskModelEntry is one entry of a use's list as Entries returns it: the
// model and the settings that list has (ImageAPI on image, TTSDefaults on
// tts; empty elsewhere). Its JSON form is the list entry's.
type TaskModelEntry struct {
	Model       string       `json:"model"`
	ImageAPI    string       `json:"image_api,omitempty"`
	TTSDefaults *TTSDefaults `json:"tts_defaults,omitempty"`
}

// TaskUse is one use, and so one list, of the Task models page.
type TaskUse string

// The uses, in the order of the page's lists.
const (
	TaskUseText     TaskUse = "text"
	TaskUseImage    TaskUse = "image"
	TaskUseTTS      TaskUse = "tts"
	TaskUseSTT      TaskUse = "stt"
	TaskUseVision   TaskUse = "vision"
	TaskUseClassify TaskUse = "classify"
)

// TaskUses lists every use in page order.
var TaskUses = []TaskUse{TaskUseText, TaskUseImage, TaskUseTTS, TaskUseSTT, TaskUseVision, TaskUseClassify}

// Label is the use's list as the WebUI and Check Up name it.
func (u TaskUse) Label() string {
	switch u {
	case TaskUseText:
		return "Generate text"
	case TaskUseImage:
		return "Generate images"
	case TaskUseTTS:
		return "Text to speech"
	case TaskUseSTT:
		return "Transcription"
	case TaskUseVision:
		return "Describe images"
	case TaskUseClassify:
		return "Classification"
	}
	return string(u)
}

// The values of ImageTaskModel.ImageAPI.
const (
	ImageAPIChat   = "chat"
	ImageAPIImages = "images"
)

// EffectiveImageAPI is image_api with "" (and so any entry outside the image
// list) at its default, chat.
func (e TaskModelEntry) EffectiveImageAPI() string {
	if e.ImageAPI == ImageAPIImages {
		return ImageAPIImages
	}
	return ImageAPIChat
}

// DefaultTaskResultTextChars is the default of task_models.result_text_chars.
// The WebUI shows it from its own copy (TASK_MODEL_DEFAULTS); both are
// checked against testdata/task_model_defaults.json.
const DefaultTaskResultTextChars = 8000

// EffectiveResultTextChars is result_text_chars with 0 at its default.
func (t TaskModelsConfig) EffectiveResultTextChars() int {
	if t.ResultTextChars <= 0 {
		return DefaultTaskResultTextChars
	}
	return t.ResultTextChars
}

// Entries returns use's list in order (the fallback order for that use),
// each entry with its settings and its model name trimmed; blank names are
// left out. It is the one way to read a use's models. Never nil.
func (t TaskModelsConfig) Entries(use TaskUse) []TaskModelEntry {
	out := []TaskModelEntry{}
	add := func(e TaskModelEntry) {
		if e.Model = strings.TrimSpace(e.Model); e.Model != "" {
			out = append(out, e)
		}
	}
	switch use {
	case TaskUseImage:
		for _, m := range t.ImageModels {
			add(TaskModelEntry{Model: m.Model, ImageAPI: m.ImageAPI})
		}
	case TaskUseTTS:
		for _, m := range t.TTSModels {
			add(TaskModelEntry{Model: m.Model, TTSDefaults: m.TTSDefaults})
		}
	default:
		if refs := t.refList(use); refs != nil {
			for _, m := range *refs {
				add(TaskModelEntry{Model: m.Model})
			}
		}
	}
	return out
}

// ModelsFor returns the names in use's list, in order: the fallback order
// for that use.
func (t TaskModelsConfig) ModelsFor(use TaskUse) []string {
	entries := t.Entries(use)
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Model
	}
	return out
}

// Entry returns the named model's entry in use's list.
func (t TaskModelsConfig) Entry(use TaskUse, model string) (TaskModelEntry, bool) {
	model = strings.TrimSpace(model)
	for _, e := range t.Entries(use) {
		if e.Model == model {
			return e, true
		}
	}
	return TaskModelEntry{}, false
}

// Has reports whether the named model is in use's list.
func (t TaskModelsConfig) Has(model string, use TaskUse) bool {
	_, ok := t.Entry(use, model)
	return ok
}

// refList is the list of a use whose entries have no settings; nil for
// image, tts and an unknown use.
func (t *TaskModelsConfig) refList(use TaskUse) *[]TaskModelRef {
	switch use {
	case TaskUseText:
		return &t.TextModels
	case TaskUseSTT:
		return &t.STTModels
	case TaskUseVision:
		return &t.VisionModels
	case TaskUseClassify:
		return &t.ClassifyModels
	}
	return nil
}

// modelFields returns a pointer to the model name of every entry of use's
// list, in order, blank ones included: what validation, normalising and the
// model reference sites walk.
func (t *TaskModelsConfig) modelFields(use TaskUse) []*string {
	var out []*string
	switch use {
	case TaskUseImage:
		for i := range t.ImageModels {
			out = append(out, &t.ImageModels[i].Model)
		}
	case TaskUseTTS:
		for i := range t.TTSModels {
			out = append(out, &t.TTSModels[i].Model)
		}
	default:
		if refs := t.refList(use); refs != nil {
			for i := range *refs {
				out = append(out, &(*refs)[i].Model)
			}
		}
	}
	return out
}

// Normalize trims every model name, voice and format, and drops TTS
// defaults that set nothing. The WebUI's save runs it before validation.
func (t *TaskModelsConfig) Normalize() {
	for _, use := range TaskUses {
		for _, name := range t.modelFields(use) {
			*name = strings.TrimSpace(*name)
		}
	}
	for i := range t.TTSModels {
		if d := t.TTSModels[i].TTSDefaults; d != nil {
			d.Voice = strings.TrimSpace(d.Voice)
			d.Format = strings.TrimSpace(d.Format)
			if d.Voice == "" && d.Format == "" {
				t.TTSModels[i].TTSDefaults = nil
			}
		}
	}
}

// Validate refuses a negative result_text_chars, an entry that names no
// model, a model listed twice in one list and an unknown image_api, naming
// every problem. The messages are sentences shown on the WebUI as they are.
func (t TaskModelsConfig) Validate() error {
	var errs []error
	if t.ResultTextChars < 0 {
		errs = append(errs, errors.New("task_models.result_text_chars must be 0 (the default) or more.")) //nolint:staticcheck // ST1005: user-facing sentence
	}
	for _, use := range TaskUses {
		seen := map[string]bool{}
		for i, field := range t.modelFields(use) {
			name := strings.TrimSpace(*field)
			switch {
			case name == "":
				errs = append(errs, fmt.Errorf("task_models.%s[%d] names no model.", use, i)) //nolint:staticcheck // ST1005: user-facing sentence
			case seen[name]:
				errs = append(errs, fmt.Errorf("Model %s is listed twice in %s.", name, use.Label())) //nolint:staticcheck // ST1005: user-facing sentence
			}
			seen[name] = true
		}
	}
	for _, m := range t.ImageModels {
		if api := m.ImageAPI; api != "" && api != ImageAPIChat && api != ImageAPIImages {
			errs = append(errs, fmt.Errorf("Model %s in %s: image_api must be %q or %q.", //nolint:staticcheck // ST1005: user-facing sentence
				strings.TrimSpace(m.Model), TaskUseImage.Label(), ImageAPIChat, ImageAPIImages))
		}
	}
	return errors.Join(errs...)
}

// dropBlankTaskModels removes, from every list, the entries whose model was
// blanked (a reference pruned for a missing or human model): an entry
// without a model is not a task model.
func (c *Config) dropBlankTaskModels() {
	t := &c.TaskModels
	blank := func(name string) bool { return strings.TrimSpace(name) == "" }
	t.ImageModels = slices.DeleteFunc(t.ImageModels, func(m ImageTaskModel) bool { return blank(m.Model) })
	t.TTSModels = slices.DeleteFunc(t.TTSModels, func(m TTSTaskModel) bool { return blank(m.Model) })
	for _, use := range TaskUses {
		if refs := t.refList(use); refs != nil {
			*refs = slices.DeleteFunc(*refs, func(m TaskModelRef) bool { return blank(m.Model) })
		}
	}
}

// TaskUseSupport is whether a provider protocol can serve a use: Yes, No, or
// Unknown when the protocol alone does not tell.
type TaskUseSupport int

const (
	TaskUseUnknown TaskUseSupport = iota
	TaskUseYes
	TaskUseNo
)

// TaskUseSupportFor says whether a provider with this protocol can serve use,
// from the protocol alone:
//   - a person (human protocol) serves none;
//   - gemini and elevenlabs (task-only protocols) serve speech and
//     transcription only;
//   - text and classify run on any other model;
//   - transcription and speech need OpenAI-style /audio endpoints
//     (openai-chat, openai-responses);
//   - vision needs an HTTP chat API that takes images (unknown for a CLI);
//   - images: OpenAI-style protocols can (chat image output or the images
//     endpoints); for the others it is not known.
func TaskUseSupportFor(protocol string, use TaskUse) TaskUseSupport {
	if IsHumanProtocol(protocol) {
		return TaskUseNo
	}
	openAI := protocol == "openai-chat" || protocol == "openai-responses"
	if TaskOnlyProtocol(protocol) {
		if use == TaskUseSTT || use == TaskUseTTS {
			return TaskUseYes
		}
		return TaskUseNo
	}
	switch use {
	case TaskUseText, TaskUseClassify:
		return TaskUseYes
	case TaskUseSTT, TaskUseTTS:
		if openAI {
			return TaskUseYes
		}
		return TaskUseNo
	case TaskUseVision:
		if _, http := httpProtocols[protocol]; http {
			return TaskUseYes
		}
		return TaskUseUnknown
	case TaskUseImage:
		if openAI {
			return TaskUseYes
		}
		return TaskUseUnknown
	default:
		return TaskUseUnknown
	}
}

// AgentTaskModels is an agent's grant to use task models through
// agent_spawn, one switch per kind. Absent = all off.
type AgentTaskModels struct {
	Text  bool `json:"text,omitempty"`
	Image bool `json:"image,omitempty"`
	TTS   bool `json:"tts,omitempty"`
	STT   bool `json:"stt,omitempty"`
}

// TaskSpawnUses are the uses an agent can be granted, in the Agents page's
// order.
var TaskSpawnUses = []TaskUse{TaskUseText, TaskUseImage, TaskUseTTS, TaskUseSTT}

// TaskModelAllowed reports whether the agent may use task models of use
// through agent_spawn.
func (a *AgentConfig) TaskModelAllowed(use TaskUse) bool {
	if a == nil || a.TaskModels == nil {
		return false
	}
	switch use {
	case TaskUseText:
		return a.TaskModels.Text
	case TaskUseImage:
		return a.TaskModels.Image
	case TaskUseTTS:
		return a.TaskModels.TTS
	case TaskUseSTT:
		return a.TaskModels.STT
	default:
		return false
	}
}

// TaskSpawnKindName is a spawnable use as a refusal names it ("images").
func TaskSpawnKindName(use TaskUse) string {
	switch use {
	case TaskUseText:
		return "text"
	case TaskUseImage:
		return "images"
	case TaskUseTTS:
		return "speech"
	case TaskUseSTT:
		return "transcription"
	default:
		return string(use)
	}
}

// TaskModelsUnsupported returns, in list order, the models in use's list
// whose provider cannot serve it (TaskUseSupportFor says No for an enabled
// model_list entry with that name). A model with no enabled entry is left
// out: the missing-model rules report it.
func (c *Config) TaskModelsUnsupported(use TaskUse) []string {
	var out []string
	for _, name := range c.TaskModels.ModelsFor(use) {
		if c.taskModelUnsupported(name, use) {
			out = append(out, name)
		}
	}
	return out
}

// taskModelUnsupported reports whether an enabled model_list entry named
// name is on a provider that can't serve use.
func (c *Config) taskModelUnsupported(name string, use TaskUse) bool {
	for i := range c.Models {
		if c.Models[i].ModelName != name || !c.Models[i].Enabled {
			continue
		}
		if p, err := c.GetProvider(c.Models[i].Provider); err == nil && TaskUseSupportFor(p.Protocol, use) == TaskUseNo {
			return true
		}
	}
	return false
}

// TaskModelProblem says why the named task model can't serve use, judged
// from the configuration alone: not on the Models page, disabled there, its
// provider missing, or a provider that can't serve the use
// (TaskUseSupportFor says No). "" when it can, as far as the configuration
// tells.
func (c *Config) TaskModelProblem(name string, use TaskUse) string {
	exists, problem := false, "disabled on the Models page"
	for i := range c.Models {
		m := &c.Models[i]
		if m.ModelName != name {
			continue
		}
		exists = true
		if !m.Enabled {
			continue
		}
		p, err := c.GetProvider(m.Provider)
		switch {
		case err != nil:
			problem = "provider not configured"
		case TaskUseSupportFor(p.Protocol, use) == TaskUseNo:
			problem = "its provider can't serve " + use.Label()
		default:
			return ""
		}
	}
	if !exists {
		return "not on the Models page"
	}
	return problem
}
