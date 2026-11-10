// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

// sixLists has a different order in every list, the same model in several
// lists, a blank entry in stt and the per-list settings.
func sixLists() TaskModelsConfig {
	return TaskModelsConfig{
		TextModels:     []TaskModelRef{{Model: "b"}, {Model: "a"}},
		ImageModels:    []ImageTaskModel{{Model: "a", ImageAPI: ImageAPIImages}, {Model: "c"}},
		TTSModels:      []TTSTaskModel{{Model: "c", TTSDefaults: &TTSDefaults{Voice: "alloy", Format: "mp3"}}, {Model: "b"}},
		STTModels:      []TaskModelRef{{Model: "c"}, {Model: " "}, {Model: " a "}},
		VisionModels:   []TaskModelRef{{Model: "a"}},
		ClassifyModels: nil,
	}
}

func TestTaskModels_ModelsForFollowsEachListsOrder(t *testing.T) {
	tm := sixLists()
	for _, tc := range []struct {
		use  TaskUse
		want []string
	}{
		{TaskUseText, []string{"b", "a"}},
		{TaskUseImage, []string{"a", "c"}},
		{TaskUseTTS, []string{"c", "b"}},
		{TaskUseSTT, []string{"c", "a"}}, // blank left out, name trimmed
		{TaskUseVision, []string{"a"}},
		{TaskUseClassify, []string{}},
		{TaskUse("nope"), []string{}},
	} {
		if got := tm.ModelsFor(tc.use); !slices.Equal(got, tc.want) {
			t.Errorf("ModelsFor(%s) = %v, want %v", tc.use, got, tc.want)
		}
		if got := tm.Entries(tc.use); got == nil {
			t.Errorf("Entries(%s) = nil, want an empty list", tc.use)
		}
	}
	if !tm.Has("a", TaskUseSTT) || !tm.Has(" c ", TaskUseImage) || tm.Has("b", TaskUseImage) || tm.Has("", TaskUseSTT) || tm.Has("a", TaskUseClassify) {
		t.Error("Has does not follow the lists")
	}
}

// Entries carries each list's own settings and nothing else.
func TestTaskModels_EntriesCarryTheirListsSettings(t *testing.T) {
	tm := sixLists()
	if got := tm.Entries(TaskUseImage); !reflect.DeepEqual(got, []TaskModelEntry{{Model: "a", ImageAPI: ImageAPIImages}, {Model: "c"}}) {
		t.Errorf("Entries(image) = %+v", got)
	}
	tts := tm.Entries(TaskUseTTS)
	if len(tts) != 2 || tts[0].TTSDefaults == nil || tts[0].TTSDefaults.Voice != "alloy" || tts[1].TTSDefaults != nil {
		t.Errorf("Entries(tts) = %+v", tts)
	}
	for _, use := range []TaskUse{TaskUseText, TaskUseSTT, TaskUseVision} {
		for _, e := range tm.Entries(use) {
			if e.ImageAPI != "" || e.TTSDefaults != nil {
				t.Errorf("%s entry %+v carries another list's settings", use, e)
			}
		}
	}
	if e, ok := tm.Entry(TaskUseImage, "a"); !ok || e.EffectiveImageAPI() != ImageAPIImages {
		t.Errorf("Entry(image, a) = %+v, %v", e, ok)
	}
	// "a" is in image with the images API, but its text entry has none.
	if e, ok := tm.Entry(TaskUseText, "a"); !ok || e.EffectiveImageAPI() != ImageAPIChat {
		t.Errorf("Entry(text, a) = %+v, %v", e, ok)
	}
	if _, ok := tm.Entry(TaskUseTTS, "a"); ok {
		t.Error("Entry(tts, a) found a model that is not in that list")
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
// the WebUI shows and the lists in page order; the frontend checks its own
// copy against the same file.
func TestTaskModels_DefaultsFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/task_model_defaults.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ResultTextChars int       `json:"result_text_chars"`
		Lists           []TaskUse `json:"lists"`
	}
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.ResultTextChars != DefaultTaskResultTextChars {
		t.Errorf("task_model_defaults.json result_text_chars = %d, want %d", fixture.ResultTextChars, DefaultTaskResultTextChars)
	}
	if !slices.Equal(fixture.Lists, TaskUses) {
		t.Errorf("task_model_defaults.json lists = %v, want %v", fixture.Lists, TaskUses)
	}
}

func TestTaskModels_Validate(t *testing.T) {
	for _, tc := range []struct {
		name string
		tm   TaskModelsConfig
		want []string
	}{
		{name: "valid", tm: func() TaskModelsConfig { tm := sixLists(); tm.STTModels = tm.STTModels[:1]; return tm }()},
		{name: "same model in every list", tm: TaskModelsConfig{
			TextModels: []TaskModelRef{{Model: "m"}}, ImageModels: []ImageTaskModel{{Model: "m"}}, TTSModels: []TTSTaskModel{{Model: "m"}},
			STTModels: []TaskModelRef{{Model: "m"}}, VisionModels: []TaskModelRef{{Model: "m"}}, ClassifyModels: []TaskModelRef{{Model: "m"}},
		}},
		{name: "negative", tm: TaskModelsConfig{ResultTextChars: -1}, want: []string{"task_models.result_text_chars must be 0 (the default) or more."}},
		{name: "blank", tm: TaskModelsConfig{TTSModels: []TTSTaskModel{{Model: "m"}, {Model: " "}}}, want: []string{"task_models.tts[1] names no model."}},
		{name: "twice in text", tm: TaskModelsConfig{TextModels: []TaskModelRef{{Model: "m"}, {Model: "m "}}}, want: []string{"Model m is listed twice in Generate text."}},
		{name: "twice in image", tm: TaskModelsConfig{ImageModels: []ImageTaskModel{{Model: "m"}, {Model: "m", ImageAPI: ImageAPIImages}}}, want: []string{"Model m is listed twice in Generate images."}},
		{name: "twice in classify and stt", tm: TaskModelsConfig{
			STTModels: []TaskModelRef{{Model: "w"}, {Model: "w"}}, ClassifyModels: []TaskModelRef{{Model: "c"}, {Model: "c"}},
		}, want: []string{"Model w is listed twice in Transcription.", "Model c is listed twice in Classification."}},
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

func TestTaskModels_Normalized(t *testing.T) {
	tm := TaskModelsConfig{
		TextModels:  []TaskModelRef{{Model: " m "}},
		ImageModels: []ImageTaskModel{{Model: " i", ImageAPI: ImageAPIImages}},
		TTSModels: []TTSTaskModel{
			{Model: "s ", TTSDefaults: &TTSDefaults{Voice: " alloy ", Format: " "}},
			{Model: "t", TTSDefaults: &TTSDefaults{Voice: " ", Format: ""}},
		},
		ClassifyModels: []TaskModelRef{{Model: "\tc"}},
	}
	got := tm.Normalized()
	want := TaskModelsConfig{
		TextModels:     []TaskModelRef{{Model: "m"}},
		ImageModels:    []ImageTaskModel{{Model: "i", ImageAPI: ImageAPIImages}},
		TTSModels:      []TTSTaskModel{{Model: "s", TTSDefaults: &TTSDefaults{Voice: "alloy"}}, {Model: "t"}},
		ClassifyModels: []TaskModelRef{{Model: "c"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Normalized() = %+v, want %+v", got, want)
	}
	if tm.TextModels[0].Model != " m " || tm.TTSModels[0].TTSDefaults.Voice != " alloy " {
		t.Errorf("Normalized() changed its receiver: %+v", tm)
	}
}

// A file with an invalid task_models block does not load.
func TestLoadConfig_RefusesInvalidTaskModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"task_models":{"result_text_chars":-5,"stt":[{"model":"m"},{"model":"m"}]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "result_text_chars must be 0") || !strings.Contains(err.Error(), "Model m is listed twice in Transcription.") {
		t.Fatalf("LoadConfig() = %v, want both problems", err)
	}
}

func TestStoreUpdate_TaskModels(t *testing.T) {
	s := storeFor(t, taskModelsTestConfig())

	err := s.Update(func(c *Config) error {
		c.TaskModels.STTModels = []TaskModelRef{{Model: "nope"}}
		return nil
	})
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), `task_models.stt[nope]: model "nope" does not exist`) {
		t.Fatalf("an unknown model = %v, want a ValidationError naming it and its list", err)
	}
	if err := s.Update(func(c *Config) error {
		c.TaskModels.ResultTextChars = -1
		return nil
	}); err == nil {
		t.Fatal("a negative result_text_chars was accepted")
	}
	if err := s.Update(func(c *Config) error {
		c.TaskModels.TextModels = []TaskModelRef{{Model: "whisper"}, {Model: "whisper"}}
		return nil
	}); err == nil || !strings.Contains(err.Error(), "Model whisper is listed twice in Generate text.") {
		t.Fatalf("a model listed twice = %v", err)
	}

	if err := s.Update(func(c *Config) error {
		c.TaskModels = TaskModelsConfig{
			ResultTextChars:   2000,
			EchoTranscription: true,
			TextModels:        []TaskModelRef{{Model: "m"}, {Model: "sonnet"}},
			ImageModels:       []ImageTaskModel{{Model: "m", ImageAPI: ImageAPIImages}},
			TTSModels:         []TTSTaskModel{{Model: "m", TTSDefaults: &TTSDefaults{Voice: "alloy", Format: "mp3"}}},
			STTModels:         []TaskModelRef{{Model: "whisper"}, {Model: "m"}},
			VisionModels:      []TaskModelRef{{Model: "sonnet"}, {Model: "m"}},
			ClassifyModels:    []TaskModelRef{{Model: "cc"}},
		}
		return nil
	}); err != nil {
		t.Fatalf("a valid update was refused: %v", err)
	}
	if _, err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	got := s.Current().TaskModels
	if got.ResultTextChars != 2000 || !got.EchoTranscription {
		t.Fatalf("settings after reload = %+v", got)
	}
	for use, want := range map[TaskUse][]string{
		TaskUseText: {"m", "sonnet"}, TaskUseImage: {"m"}, TaskUseTTS: {"m"},
		TaskUseSTT: {"whisper", "m"}, TaskUseVision: {"sonnet", "m"}, TaskUseClassify: {"cc"},
	} {
		if names := got.ModelsFor(use); !slices.Equal(names, want) {
			t.Errorf("ModelsFor(%s) after reload = %v, want %v", use, names, want)
		}
	}
	if e, _ := got.Entry(TaskUseImage, "m"); e.ImageAPI != ImageAPIImages {
		t.Errorf("image entry after reload = %+v", e)
	}
	if e, _ := got.Entry(TaskUseTTS, "m"); e.TTSDefaults == nil || e.TTSDefaults.Voice != "alloy" {
		t.Errorf("tts entry after reload = %+v", e)
	}

	// A rename follows the model into every list it is in.
	if err := s.Update(func(c *Config) error {
		c.Models[0].ModelName = "gpt"
		c.RenameModelReferences("m", "gpt")
		return nil
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	cur := s.Current().TaskModels
	if !slices.Equal(cur.ModelsFor(TaskUseSTT), []string{"whisper", "gpt"}) || !slices.Equal(cur.ModelsFor(TaskUseText), []string{"gpt", "sonnet"}) {
		t.Errorf("lists after rename = %+v", cur)
	}
	if e, _ := cur.Entry(TaskUseImage, "gpt"); e.ImageAPI != ImageAPIImages {
		t.Errorf("image entry after rename = %+v, want its image_api kept", e)
	}
	want := []string{"agents.defaults.models", "task_models.text[gpt]", "task_models.image[gpt]", "task_models.tts[gpt]", "task_models.stt[gpt]", "task_models.vision[gpt]"}
	if refs := s.Current().ModelReferences("gpt"); !slices.Equal(refs, want) {
		t.Errorf("ModelReferences(gpt) = %v, want %v", refs, want)
	}
	// The reference names the list, so a refused delete says which.
	if refs := s.Current().ModelReferences("cc"); !slices.Equal(refs, []string{"task_models.classify[cc]"}) {
		t.Errorf("ModelReferences(cc) = %v", refs)
	}
}

// The pre-release single list (task_models.models) is an unknown key: the
// file loads without it and the next save drops it.
func TestLoadConfig_OldTaskModelsListIsUnknownAndDropped(t *testing.T) {
	cfg := taskModelsTestConfig()
	s := storeFor(t, cfg)
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["task_models"] = map[string]any{
		"echo_transcription": true,
		"models":             []any{map[string]any{"model": "whisper", "stt": true}},
		"text":               []any{map[string]any{"model": "m"}},
	}
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.Path(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := unknownConfigKeys(decoded); !slices.Equal(got, []string{"task_models.models"}) {
		t.Fatalf("unknownConfigKeys = %v, want [task_models.models]", got)
	}
	cur, err := s.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := cur.TaskModels; len(got.ModelsFor(TaskUseSTT)) != 0 || !slices.Equal(got.ModelsFor(TaskUseText), []string{"m"}) || !got.EchoTranscription {
		t.Fatalf("task_models = %+v, want only the text list and the setting", got)
	}
	if err = s.Update(func(c *Config) error {
		c.TaskModels.ResultTextChars = 100
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		TaskModels map[string]json.RawMessage `json:"task_models"`
	}
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if _, ok := saved.TaskModels["models"]; ok {
		t.Errorf("saved task_models = %s, want the old list dropped", raw)
	}
	if _, ok := saved.TaskModels["text"]; !ok {
		t.Errorf("saved task_models lost its text list: %v", saved.TaskModels)
	}
}

// A setting on a list that does not have it is an unknown key: image_api
// belongs to image entries, tts_defaults to tts entries.
func TestTaskModels_SettingsOnlyOnTheirList(t *testing.T) {
	doc := map[string]any{"task_models": map[string]any{
		"text":  []any{map[string]any{"model": "m", "image_api": "images"}},
		"image": []any{map[string]any{"model": "m", "image_api": "images", "tts_defaults": map[string]any{"voice": "v"}}},
		"tts":   []any{map[string]any{"model": "m", "tts_defaults": map[string]any{"voice": "v"}, "image_api": "chat"}},
		"stt":   []any{map[string]any{"model": "m", "sst": true}},
	}}
	want := []string{"task_models.image[0].tts_defaults", "task_models.stt[0].sst", "task_models.text[0].image_api", "task_models.tts[0].image_api"}
	if got := unknownConfigKeys(doc); !slices.Equal(got, want) {
		t.Errorf("unknownConfigKeys = %v, want %v", got, want)
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
	for _, use := range TaskUses {
		if cfg.TaskModels.EchoTranscription || len(cfg.TaskModels.ModelsFor(use)) != 0 {
			t.Errorf("task_models = %+v, want nothing carried over", cfg.TaskModels)
		}
	}
	doc, err := decodeDocument([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agents.defaults.vision_model", "agents.defaults.vision_model_fallbacks", "voice"}
	if got := unknownConfigKeys(doc); !slices.Equal(got, want) {
		t.Fatalf("unknownConfigKeys = %v, want %v", got, want)
	}
}

func TestTaskModelsUnsupported(t *testing.T) {
	cfg := taskModelsTestConfig()
	cfg.TaskModels = TaskModelsConfig{
		STTModels:    []TaskModelRef{{Model: "sonnet"}, {Model: "whisper"}, {Model: "gone"}, {Model: "cc"}},
		TextModels:   []TaskModelRef{{Model: "sonnet"}},
		VisionModels: []TaskModelRef{{Model: "cc"}, {Model: "sonnet"}},
	}
	if got := cfg.TaskModelsUnsupported(TaskUseSTT); !slices.Equal(got, []string{"sonnet", "cc"}) {
		t.Errorf("stt = %v, want [sonnet cc] in list order (a missing model left out)", got)
	}
	if got := cfg.TaskModelsUnsupported(TaskUseText); len(got) != 0 {
		t.Errorf("text = %v, want none", got)
	}
	if got := cfg.TaskModelsUnsupported(TaskUseVision); len(got) != 0 {
		t.Errorf("vision = %v, want none (unknown for a CLI)", got)
	}
}

func TestTaskModels_ImageAPI(t *testing.T) {
	for _, tc := range []struct {
		api, want string
		bad       bool
	}{{"", ImageAPIChat, false}, {"chat", ImageAPIChat, false}, {"images", ImageAPIImages, false}, {"dalle", ImageAPIChat, true}} {
		tm := TaskModelsConfig{ImageModels: []ImageTaskModel{{Model: "m", ImageAPI: tc.api}}}
		if e, _ := tm.Entry(TaskUseImage, "m"); e.EffectiveImageAPI() != tc.want {
			t.Errorf("EffectiveImageAPI(%q) = %q, want %q", tc.api, e.EffectiveImageAPI(), tc.want)
		}
		err := tm.Validate()
		if (err != nil) != tc.bad {
			t.Errorf("Validate(image_api %q) = %v, want refused %v", tc.api, err, tc.bad)
		}
		if tc.bad && !strings.Contains(err.Error(), `Model m in Generate images: image_api must be "chat" or "images".`) {
			t.Errorf("refusal = %v", err)
		}
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

func TestTaskModelProblem(t *testing.T) {
	cfg := taskModelsTestConfig()
	cfg.Models = append(cfg.Models,
		ModelConfig{ModelName: "off", Model: "x", Provider: "openai", Enabled: false},
		ModelConfig{ModelName: "orphan", Model: "x", Provider: "gone", Enabled: true},
	)
	cfg.Providers = append(cfg.Providers, Provider{Name: "el", Protocol: ElevenLabsProtocol, APIKey: "k"})
	cfg.Models = append(cfg.Models, ModelConfig{ModelName: "scribe", Model: "scribe_v1", Provider: "el", Enabled: true})
	for _, tc := range []struct {
		name string
		use  TaskUse
		want string
	}{
		{"m", TaskUseVision, ""},
		{"cc", TaskUseVision, ""}, // unknown for a CLI: not a problem
		{"nope", TaskUseVision, "not on the Models page"},
		{"off", TaskUseVision, "disabled on the Models page"},
		{"orphan", TaskUseVision, "provider not configured"},
		{"scribe", TaskUseVision, "its provider can't serve Describe images"},
		{"scribe", TaskUseSTT, ""},
	} {
		if got := cfg.TaskModelProblem(tc.name, tc.use); got != tc.want {
			t.Errorf("TaskModelProblem(%s, %s) = %q, want %q", tc.name, tc.use, got, tc.want)
		}
	}
}
