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
// from the protocol alone. A person (human protocol) serves none; text and
// classify run on any model; transcription needs the OpenAI-style
// /audio/transcriptions endpoint (openai-chat, openai-responses); vision needs
// an HTTP chat API that takes images. Images and speech (TTS) are not known
// from the protocol.
func TaskUseSupportFor(protocol string, use TaskUse) TaskUseSupport {
	if IsHumanProtocol(protocol) {
		return TaskUseNo
	}
	switch use {
	case TaskUseText, TaskUseClassify:
		return TaskUseYes
	case TaskUseSTT:
		if protocol == "openai-chat" || protocol == "openai-responses" {
			return TaskUseYes
		}
		return TaskUseNo
	case TaskUseVision:
		if _, http := httpProtocols[protocol]; http {
			return TaskUseYes
		}
		return TaskUseUnknown
	default: // images and speech: not known from the protocol
		return TaskUseUnknown
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
