// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// taskModelsTestConfig has three enabled models on an OpenAI-style provider,
// one on a CLI provider and one person.
func taskModelsTestConfig() *Config {
	cfg := DefaultConfig()
	cfg.Providers = []Provider{
		{Name: "openai", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test-0123456789"},
		{Name: "anthropic", Protocol: "anthropic", BaseURL: "https://api.anthropic.com/v1", APIKey: "sk-test-0123456789"},
		{Name: "claude", Protocol: "claude-cli"},
	}
	cfg.Models = []ModelConfig{
		{ModelName: "m", Model: "gpt-4o", Provider: "openai", Enabled: true},
		{ModelName: "whisper", Model: "whisper-1", Provider: "openai", Enabled: true},
		{ModelName: "sonnet", Model: "claude-sonnet", Provider: "anthropic", Enabled: true},
		{ModelName: "cc", Model: "claude-cli", Provider: "claude", Enabled: true},
	}
	cfg.Agents.Defaults.Models = []string{"m"}
	cfg.Agents.List = []AgentConfig{{ID: "alice", Name: "Alice", Default: true}}
	return cfg
}

func TestTaskModels_ModelsForInPageOrder(t *testing.T) {
	tm := TaskModelsConfig{Models: []TaskModel{
		{Model: "b", STT: true, Vision: true},
		{Model: "a", Text: true},
		{Model: " ", STT: true},
		{Model: "c", STT: true, Classify: true},
	}}
	if got := tm.ModelsFor(TaskUseSTT); !slices.Equal(got, []string{"b", "c"}) {
		t.Errorf("ModelsFor(stt) = %v, want [b c] (page order, blank left out)", got)
	}
	if got := tm.ModelsFor(TaskUseImage); len(got) != 0 {
		t.Errorf("ModelsFor(image) = %v, want none", got)
	}
	if !tm.Has("c", TaskUseClassify) || tm.Has("a", TaskUseClassify) || tm.Has("", TaskUseSTT) {
		t.Error("Has does not follow the ticks")
	}
	if got := tm.Models[0].Uses(); !slices.Equal(got, []TaskUse{TaskUseSTT, TaskUseVision}) {
		t.Errorf("Uses() = %v", got)
	}
}

func TestTaskModels_EffectiveResultTextChars(t *testing.T) {
	for _, tc := range []struct{ set, want int }{{0, DefaultTaskResultTextChars}, {-3, DefaultTaskResultTextChars}, {500, 500}} {
		if got := (TaskModelsConfig{ResultTextChars: tc.set}).EffectiveResultTextChars(); got != tc.want {
			t.Errorf("EffectiveResultTextChars(%d) = %d, want %d", tc.set, got, tc.want)
		}
	}
}

// testdata/task_model_defaults.json holds the defaults of task_models that
// the WebUI shows; the frontend checks its own copy against the same file.
func TestTaskModels_DefaultsFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/task_model_defaults.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ResultTextChars int `json:"result_text_chars"`
	}
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.ResultTextChars != DefaultTaskResultTextChars {
		t.Errorf("task_model_defaults.json result_text_chars = %d, want %d", fixture.ResultTextChars, DefaultTaskResultTextChars)
	}
}

func TestTaskModels_Validate(t *testing.T) {
	for _, tc := range []struct {
		name string
		tm   TaskModelsConfig
		want []string
	}{
		{name: "valid", tm: TaskModelsConfig{ResultTextChars: 100, Models: []TaskModel{{Model: "m", Text: true}, {Model: "whisper"}}}},
		{name: "negative", tm: TaskModelsConfig{ResultTextChars: -1}, want: []string{"task_models.result_text_chars must be 0 (the default) or more."}},
		{name: "blank", tm: TaskModelsConfig{Models: []TaskModel{{Model: "m"}, {Model: ""}}}, want: []string{"task_models.models[1] names no model."}},
		{name: "twice", tm: TaskModelsConfig{Models: []TaskModel{{Model: "m"}, {Model: "m", STT: true}}}, want: []string{"Task model m is listed twice."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tm.Validate()
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("Validate() = %v", err)
				}
				return
			}
			for _, w := range tc.want {
				if err == nil || !strings.Contains(err.Error(), w) {
					t.Errorf("Validate() = %v, want %q", err, w)
				}
			}
		})
	}
}

// A file with an invalid task_models block does not load.
func TestLoadConfig_RefusesInvalidTaskModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"task_models":{"result_text_chars":-5,"models":[{"model":"m"},{"model":"m"}]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "result_text_chars must be 0") || !strings.Contains(err.Error(), "listed twice") {
		t.Fatalf("LoadConfig() = %v, want both problems", err)
	}
}

func TestStoreUpdate_TaskModels(t *testing.T) {
	s := storeFor(t, taskModelsTestConfig())

	err := s.Update(func(c *Config) error {
		c.TaskModels.Models = []TaskModel{{Model: "nope", STT: true}}
		return nil
	})
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), `task_models.models[nope]: model "nope" does not exist`) {
		t.Fatalf("an unknown model = %v, want a ValidationError naming it", err)
	}
	if err := s.Update(func(c *Config) error {
		c.TaskModels.ResultTextChars = -1
		return nil
	}); err == nil {
		t.Fatal("a negative result_text_chars was accepted")
	}
	if err := s.Update(func(c *Config) error {
		c.TaskModels.Models = []TaskModel{{Model: "whisper", STT: true}, {Model: "whisper", Text: true}}
		return nil
	}); err == nil || !strings.Contains(err.Error(), "Task model whisper is listed twice.") {
		t.Fatalf("a model listed twice = %v", err)
	}

	if err := s.Update(func(c *Config) error {
		c.TaskModels = TaskModelsConfig{
			ResultTextChars:   2000,
			EchoTranscription: true,
			Models: []TaskModel{
				{Model: "whisper", STT: true},
				{Model: "m", Text: true, Vision: true, TTS: true, TTSDefaults: &TTSDefaults{Voice: "alloy", Format: "mp3"}},
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("a valid update was refused: %v", err)
	}
	if _, err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	got := s.Current().TaskModels
	if got.ResultTextChars != 2000 || !got.EchoTranscription || len(got.Models) != 2 ||
		got.Models[1].TTSDefaults == nil || got.Models[1].TTSDefaults.Voice != "alloy" {
		t.Fatalf("task_models after reload = %+v", got)
	}

	// A model referenced from the page cannot be renamed away silently: the
	// rename follows it.
	if err := s.Update(func(c *Config) error {
		c.Models[1].ModelName = "stt"
		c.RenameModelReferences("whisper", "stt")
		return nil
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := s.Current().TaskModels.ModelsFor(TaskUseSTT); !slices.Equal(got, []string{"stt"}) {
		t.Errorf("STT models after rename = %v, want [stt]", got)
	}
	if refs := s.Current().ModelReferences("stt"); !slices.Equal(refs, []string{"task_models.models[stt]"}) {
		t.Errorf("ModelReferences(stt) = %v", refs)
	}
}

// voice.stt, voice.echo_transcription and the vision model keys are gone:
// a config still holding them loads, and they are reported as unknown.
func TestLoadConfig_RemovedVoiceAndVisionKeysAreUnknown(t *testing.T) {
	body := `{"voice":{"echo_transcription":true,"stt":[{"provider":"groq","enabled":true}]},` +
		`"agents":{"defaults":{"vision_model":"m","vision_model_fallbacks":["n"],"max_tokens":77}}}`
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Agents.Defaults.MaxTokens != 77 {
		t.Errorf("max_tokens = %d, want 77 (the removed keys must not disturb their neighbours)", cfg.Agents.Defaults.MaxTokens)
	}
	if cfg.TaskModels.EchoTranscription || len(cfg.TaskModels.Models) != 0 {
		t.Errorf("task_models = %+v, want nothing carried over", cfg.TaskModels)
	}
	doc, err := decodeDocument([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agents.defaults.vision_model", "agents.defaults.vision_model_fallbacks", "voice"}
	if got := unknownConfigKeys(doc); !slices.Equal(got, want) {
		t.Fatalf("unknownConfigKeys = %v, want %v", got, want)
	}
	if got := unknownConfigKeys(map[string]any{"task_models": map[string]any{"models": []any{map[string]any{"model": "m", "sst": true}}}}); !slices.Equal(got, []string{"task_models.models[0].sst"}) {
		t.Errorf("a misspelt use = %v, want it reported", got)
	}
}

func TestTaskUseSupportFor(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		use      TaskUse
		want     TaskUseSupport
	}{
		{"openai-chat", TaskUseSTT, TaskUseYes},
		{"openai-responses", TaskUseSTT, TaskUseYes},
		{"anthropic", TaskUseSTT, TaskUseNo},
		{"claude-cli", TaskUseSTT, TaskUseNo},
		{"anthropic", TaskUseVision, TaskUseYes},
		{"claude-cli", TaskUseVision, TaskUseUnknown},
		{"claude-cli", TaskUseText, TaskUseYes},
		{"azure", TaskUseClassify, TaskUseYes},
		{"openai-chat", TaskUseImage, TaskUseYes},
		{"anthropic", TaskUseImage, TaskUseUnknown},
		{"openai-chat", TaskUseTTS, TaskUseYes},
		{"anthropic", TaskUseTTS, TaskUseNo},
		{HumanProtocol, TaskUseText, TaskUseNo},
		{GeminiProtocol, TaskUseSTT, TaskUseYes},
		{GeminiProtocol, TaskUseTTS, TaskUseYes},
		{GeminiProtocol, TaskUseText, TaskUseNo},
		{GeminiProtocol, TaskUseImage, TaskUseNo},
		{ElevenLabsProtocol, TaskUseSTT, TaskUseYes},
		{ElevenLabsProtocol, TaskUseTTS, TaskUseYes},
		{ElevenLabsProtocol, TaskUseVision, TaskUseNo},
	} {
		if got := TaskUseSupportFor(tc.protocol, tc.use); got != tc.want {
			t.Errorf("TaskUseSupportFor(%s, %s) = %v, want %v", tc.protocol, tc.use, got, tc.want)
		}
	}
}

func TestTaskModelUnsupported(t *testing.T) {
	cfg := taskModelsTestConfig()
	if got := cfg.TaskModelUnsupported(TaskModel{Model: "sonnet", STT: true, Text: true, Vision: true}); !slices.Equal(got, []TaskUse{TaskUseSTT}) {
		t.Errorf("sonnet = %v, want [stt]", got)
	}
	if got := cfg.TaskModelUnsupported(TaskModel{Model: "cc", Vision: true, Classify: true}); len(got) != 0 {
		t.Errorf("cc = %v, want none (vision unknown for a CLI)", got)
	}
	if got := cfg.TaskModelUnsupported(TaskModel{Model: "gone", STT: true}); got != nil {
		t.Errorf("missing model = %v, want nil", got)
	}
}

func TestTaskModels_ImageAPI(t *testing.T) {
	for _, tc := range []struct {
		api, want string
		bad       bool
	}{{"", ImageAPIChat, false}, {"chat", ImageAPIChat, false}, {"images", ImageAPIImages, false}, {"dalle", ImageAPIChat, true}} {
		m := TaskModel{Model: "m", Image: true, ImageAPI: tc.api}
		if got := m.EffectiveImageAPI(); got != tc.want {
			t.Errorf("EffectiveImageAPI(%q) = %q, want %q", tc.api, got, tc.want)
		}
		err := TaskModelsConfig{Models: []TaskModel{m}}.Validate()
		if (err != nil) != tc.bad {
			t.Errorf("Validate(image_api %q) = %v, want refused %v", tc.api, err, tc.bad)
		}
		if tc.bad && !strings.Contains(err.Error(), `Task model m: image_api must be "chat" or "images".`) {
			t.Errorf("refusal = %v", err)
		}
	}
}

func TestAgentConfig_TaskModelAllowed(t *testing.T) {
	var none *AgentConfig
	off := &AgentConfig{ID: "alice"}
	some := &AgentConfig{ID: "bob", TaskModels: &AgentTaskModels{Image: true, STT: true}}
	for _, use := range TaskSpawnUses {
		if none.TaskModelAllowed(use) || off.TaskModelAllowed(use) {
			t.Errorf("%s allowed without a grant", use)
		}
	}
	if !some.TaskModelAllowed(TaskUseImage) || !some.TaskModelAllowed(TaskUseSTT) ||
		some.TaskModelAllowed(TaskUseText) || some.TaskModelAllowed(TaskUseTTS) || some.TaskModelAllowed(TaskUseVision) {
		t.Errorf("grant %+v not followed", some.TaskModels)
	}
}

// gemini and elevenlabs are HTTP protocols with an optional base URL and a
// required API key; a model on one has no chat client.
func TestTaskOnlyProtocols_Validation(t *testing.T) {
	for _, proto := range []string{GeminiProtocol, ElevenLabsProtocol} {
		if !TaskOnlyProtocol(proto) {
			t.Errorf("%s is not task-only", proto)
		}
		c := &Config{Providers: []Provider{{Name: "v", Protocol: proto, APIKey: "k"}}}
		if err := c.ValidateProviders(); err != nil {
			t.Errorf("%s without base_url: %v", proto, err)
		}
		c.Providers[0].APIKey = ""
		if err := c.ValidateProviders(); err == nil || !strings.Contains(err.Error(), "api_key is required") {
			t.Errorf("%s without api_key = %v", proto, err)
		}
		if err := c.ValidateProvider(0); err == nil {
			t.Errorf("%s without api_key accepted by ValidateProvider", proto)
		}
		if dp, _ := c.PruneInvalid(); dp != 1 {
			t.Errorf("%s without api_key not pruned", proto)
		}
	}
	if TaskOnlyProtocol("openai-chat") {
		t.Error("openai-chat is task-only")
	}
}
