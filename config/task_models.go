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
// tools), each ticked for the uses it serves. The list order is the fallback
// order within every use.
type TaskModelsConfig struct {
	// ResultTextChars is how much text a task result carries inline; longer
	// text is cut. 0 or absent uses DefaultTaskResultTextChars; negative is
	// refused at load and on save.
	ResultTextChars int `json:"result_text_chars,omitempty"`
	// EchoTranscription sends the transcript of an incoming voice message
	// back to the sender before the agent answers.
	EchoTranscription bool        `json:"echo_transcription,omitempty" env:"CLAW_TASK_MODELS_ECHO_TRANSCRIPTION"`
	Models            []TaskModel `json:"models,omitempty"`
}

// TaskModel is one row of the Task models page: a model_list[].model_name
// and the uses it is ticked for. Absent = off.
type TaskModel struct {
	Model    string `json:"model"`
	Text     bool   `json:"text,omitempty"`
	Image    bool   `json:"image,omitempty"`
	TTS      bool   `json:"tts,omitempty"`
	STT      bool   `json:"stt,omitempty"`
	Vision   bool   `json:"vision,omitempty"`
	Classify bool   `json:"classify,omitempty"`
	// ImageAPI is how an Images model is called: "chat" (the default: image
	// output of a chat call) or "images" (the OpenAI-style image endpoints,
	// /images/generations and /images/edits; OpenRouter's /images).
	ImageAPI string `json:"image_api,omitempty"`
	// TTSDefaults are the voice and format a speech call uses unless the
	// caller names its own.
	TTSDefaults *TTSDefaults `json:"tts_defaults,omitempty"`
}

// TTSDefaults is a TTS model's default voice and audio format.
type TTSDefaults struct {
	Voice  string `json:"voice,omitempty"`
	Format string `json:"format,omitempty"`
}

// TaskUse is one use a task model can be ticked for.
type TaskUse string

// The uses, in the order of the page's checkboxes.
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

// Label is the use as the WebUI and Check Up name it.
func (u TaskUse) Label() string {
	switch u {
	case TaskUseText:
		return "Text"
	case TaskUseImage:
		return "Images"
	case TaskUseTTS:
		return "Speech (TTS)"
	case TaskUseSTT:
		return "Transcription (STT)"
	case TaskUseVision:
		return "Vision"
	case TaskUseClassify:
		return "Classify"
	}
	return string(u)
}

// The values of TaskModel.ImageAPI.
const (
	ImageAPIChat   = "chat"
	ImageAPIImages = "images"
)

// EffectiveImageAPI is image_api with "" at its default, chat.
func (m TaskModel) EffectiveImageAPI() string {
	if m.ImageAPI == ImageAPIImages {
		return ImageAPIImages
	}
	return ImageAPIChat
}

// DefaultTaskResultTextChars is the default of task_models.result_text_chars.
// The WebUI shows it from its own copy (TASK_MODEL_DEFAULTS); both are
// checked against testdata/task_model_defaults.json.
const DefaultTaskResultTextChars = 8000

// Has reports whether the model is ticked for use.
func (m TaskModel) Has(use TaskUse) bool {
	switch use {
	case TaskUseText:
		return m.Text
	case TaskUseImage:
		return m.Image
	case TaskUseTTS:
		return m.TTS
	case TaskUseSTT:
		return m.STT
	case TaskUseVision:
		return m.Vision
	case TaskUseClassify:
		return m.Classify
	}
	return false
}

// Uses returns the uses the model is ticked for, in page order.
func (m TaskModel) Uses() []TaskUse {
	var out []TaskUse
	for _, u := range TaskUses {
		if m.Has(u) {
			out = append(out, u)
		}
	}
	return out
}

// EffectiveResultTextChars is result_text_chars with 0 at its default.
func (t TaskModelsConfig) EffectiveResultTextChars() int {
	if t.ResultTextChars <= 0 {
		return DefaultTaskResultTextChars
	}
	return t.ResultTextChars
}

// ModelsFor returns the names of the models ticked for use, in page order:
// the fallback order for that use. Blank names are left out.
func (t TaskModelsConfig) ModelsFor(use TaskUse) []string {
	var out []string
	for _, m := range t.Models {
		if name := strings.TrimSpace(m.Model); name != "" && m.Has(use) {
			out = append(out, name)
		}
	}
	return out
}

// Has reports whether the named model is ticked for use.
func (t TaskModelsConfig) Has(model string, use TaskUse) bool {
	model = strings.TrimSpace(model)
	return model != "" && slices.Contains(t.ModelsFor(use), model)
}

// Validate refuses a negative result_text_chars, an entry that names no
// model and a model listed twice, naming every problem. The messages are
// sentences shown on the WebUI as they are.
func (t TaskModelsConfig) Validate() error {
	var errs []error
	if t.ResultTextChars < 0 {
		errs = append(errs, errors.New("task_models.result_text_chars must be 0 (the default) or more.")) //nolint:staticcheck // ST1005: user-facing sentence
	}
	seen := make(map[string]bool, len(t.Models))
	for i, m := range t.Models {
		name := strings.TrimSpace(m.Model)
		switch {
		case name == "":
			errs = append(errs, fmt.Errorf("task_models.models[%d] names no model.", i)) //nolint:staticcheck // ST1005: user-facing sentence
		case seen[name]:
			errs = append(errs, fmt.Errorf("Task model %s is listed twice.", name)) //nolint:staticcheck // ST1005: user-facing sentence
		}
		if api := m.ImageAPI; api != "" && api != ImageAPIChat && api != ImageAPIImages {
			errs = append(errs, fmt.Errorf("Task model %s: image_api must be %q or %q.", name, ImageAPIChat, ImageAPIImages)) //nolint:staticcheck // ST1005: user-facing sentence
		}
		seen[name] = true
	}
	return errors.Join(errs...)
}

// dropBlankTaskModels removes the task model entries whose model was blanked
// (a reference pruned for a missing or human model): a row without a model
// is not a task model.
func (c *Config) dropBlankTaskModels() {
	c.TaskModels.Models = slices.DeleteFunc(c.TaskModels.Models, func(m TaskModel) bool {
		return strings.TrimSpace(m.Model) == ""
	})
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

// TaskModelUnsupported returns, in page order, the uses the named task model
// is ticked for that its provider cannot serve (TaskUseSupportFor says No),
// judged by every enabled model_list entry with that name. A model with no
// enabled entry returns nil: the missing-model rules report it.
func (c *Config) TaskModelUnsupported(m TaskModel) []TaskUse {
	var protocols []string
	for i := range c.Models {
		if c.Models[i].ModelName != m.Model || !c.Models[i].Enabled {
			continue
		}
		if p, err := c.GetProvider(c.Models[i].Provider); err == nil {
			protocols = append(protocols, p.Protocol)
		}
	}
	if len(protocols) == 0 {
		return nil
	}
	var out []TaskUse
	for _, u := range m.Uses() {
		if slices.ContainsFunc(protocols, func(p string) bool { return TaskUseSupportFor(p, u) == TaskUseNo }) {
			out = append(out, u)
		}
	}
	return out
}
