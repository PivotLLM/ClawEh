// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// setupTaskModelsEnv writes a config with an OpenAI-style model, an anthropic
// model, a CLI model and a load-balanced name on two providers, and Alice
// with her classifiers on "custom-default".
func setupTaskModelsEnv(t *testing.T) (string, *http.ServeMux, *int) {
	t.Helper()
	configPath := setupTestEnv(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers = append(cfg.Providers,
		config.Provider{Name: "anthropic-api", Protocol: "anthropic", BaseURL: "https://api.anthropic.com/v1", APIKey: "sk-ant-secret"},
		config.Provider{Name: "Claude CLI", Protocol: "claude-cli"},
	)
	cfg.Models = append(cfg.Models,
		config.ModelConfig{ModelName: "sonnet", Model: "claude-sonnet", Provider: "anthropic-api", Enabled: true},
		config.ModelConfig{ModelName: "cc", Model: "claude-cli", Provider: "Claude CLI", Enabled: true},
		config.ModelConfig{ModelName: "mixed", Model: "gpt-4o", Provider: "openai", Enabled: true},
		config.ModelConfig{ModelName: "mixed", Model: "claude-sonnet", Provider: "anthropic-api", Enabled: true},
		config.ModelConfig{ModelName: "off", Model: "gpt-4o", Provider: "openai", Enabled: false},
	)
	cfg.TaskModels.ClassifyModels = []config.TaskModelRef{{Model: "custom-default"}}
	cfg.TaskModels.TextModels = []config.TaskModelRef{{Model: "custom-default"}}
	cfg.TaskModels.ImageModels = []config.ImageTaskModel{{Model: "custom-default", ImageAPI: config.ImageAPIImages}}
	cfg.Agents.List[0].Classifiers = &config.AgentClassifiersConfig{Enabled: true, Model: "custom-default"}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	h := NewHandler(configPath)
	h.SetReloadTrigger(func() error { reloads++; return nil })
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return configPath, mux, &reloads
}

func serveTaskModels(mux *http.ServeMux, method, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, "/api/task-models", strings.NewReader(body)))
	return rec
}

// GET returns the block with every list (an empty one as []), its default
// and every model name once, in config order, with what its provider can
// serve; never a key.
func TestHandleGetTaskModels(t *testing.T) {
	_, mux, _ := setupTaskModelsEnv(t)
	rec := serveTaskModels(mux, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-") {
		t.Fatalf("response carries a key: %s", rec.Body.String())
	}
	var got struct {
		TaskModels map[string]json.RawMessage `json:"task_models"`
		Defaults   struct {
			ResultTextChars int `json:"result_text_chars"`
		} `json:"defaults"`
		Models []taskModelOption `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Defaults.ResultTextChars != config.DefaultTaskResultTextChars {
		t.Errorf("defaults = %+v", got.Defaults)
	}
	for key, want := range map[string]string{
		"text":               `[{"model":"custom-default"}]`,
		"image":              `[{"model":"custom-default","image_api":"images"}]`,
		"tts":                `[]`,
		"stt":                `[]`,
		"vision":             `[]`,
		"classify":           `[{"model":"custom-default"}]`,
		"result_text_chars":  `0`,
		"echo_transcription": `false`,
	} {
		if s := string(got.TaskModels[key]); s != want {
			t.Errorf("task_models.%s = %s, want %s", key, s, want)
		}
	}
	if _, old := got.TaskModels["models"]; old || len(got.TaskModels) != 8 {
		t.Errorf("task_models keys = %v", got.TaskModels)
	}
	byName := map[string]taskModelOption{}
	names := make([]string, 0, len(got.Models))
	for _, m := range got.Models {
		byName[m.Name] = m
		names = append(names, m.Name)
	}
	if strings.Join(names, ",") != "custom-default,sonnet,cc,mixed,off" {
		t.Fatalf("models = %v, want each name once in config order", names)
	}
	for _, tc := range []struct{ model, use, want string }{
		{"custom-default", "stt", "yes"},
		{"sonnet", "stt", "no"},
		{"sonnet", "vision", "yes"},
		{"cc", "vision", "unknown"},
		{"cc", "text", "yes"},
		{"mixed", "stt", "no"}, // one of its entries cannot
		{"custom-default", "tts", "yes"},
		{"sonnet", "tts", "no"},
		{"cc", "image", "unknown"},
		{"cc", "classify", "yes"},
	} {
		if s := byName[tc.model].Support[tc.use]; s != tc.want {
			t.Errorf("%s %s = %q, want %q", tc.model, tc.use, s, tc.want)
		}
	}
	if byName["off"].Enabled || len(byName["off"].Support) != 0 {
		t.Errorf("disabled model = %+v, want not enabled and no support judged", byName["off"])
	}
}

// PUT replaces the block through the store and reloads; the store's refusals
// come back as validation errors and change nothing.
func TestHandleUpdateTaskModels(t *testing.T) {
	configPath, mux, reloads := setupTaskModelsEnv(t)

	body := `{"result_text_chars":3000,"echo_transcription":true,
		"text":[{"model":"cc"},{"model":" sonnet "}],
		"image":[{"model":"custom-default","image_api":"images"},{"model":"cc"}],
		"tts":[{"model":"custom-default","tts_defaults":{"voice":" alloy ","format":"mp3"}},{"model":"cc","tts_defaults":{"voice":" ","format":""}}],
		"stt":[{"model":"custom-default"}],
		"vision":[{"model":"sonnet"},{"model":"custom-default"}],
		"classify":[{"model":"custom-default"},{"model":"cc"}]}`
	rec := serveTaskModels(mux, http.MethodPut, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if *reloads != 1 {
		t.Errorf("reloads = %d, want 1", *reloads)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	tm := cfg.TaskModels
	if tm.ResultTextChars != 3000 || !tm.EchoTranscription {
		t.Fatalf("saved settings = %+v", tm)
	}
	for use, want := range map[config.TaskUse]string{
		config.TaskUseText:     "cc,sonnet",
		config.TaskUseImage:    "custom-default,cc",
		config.TaskUseTTS:      "custom-default,cc",
		config.TaskUseSTT:      "custom-default",
		config.TaskUseVision:   "sonnet,custom-default",
		config.TaskUseClassify: "custom-default,cc",
	} {
		if got := strings.Join(tm.ModelsFor(use), ","); got != want {
			t.Errorf("saved %s = %s, want %s", use, got, want)
		}
	}
	if e, _ := tm.Entry(config.TaskUseImage, "custom-default"); e.ImageAPI != config.ImageAPIImages {
		t.Errorf("image entry = %+v", e)
	}
	if e, _ := tm.Entry(config.TaskUseTTS, "custom-default"); e.TTSDefaults == nil || e.TTSDefaults.Voice != "alloy" || e.TTSDefaults.Format != "mp3" {
		t.Errorf("tts entry = %+v", e)
	}
	if e, _ := tm.Entry(config.TaskUseTTS, "cc"); e.TTSDefaults != nil {
		t.Errorf("blank tts_defaults kept: %+v", e.TTSDefaults)
	}

	for _, tc := range []struct{ name, body, want string }{
		{"unknown model", `{"stt":[{"model":"ghost"}],"classify":[{"model":"custom-default"}]}`, `task_models.stt[ghost]: model \"ghost\" does not exist`},
		{"twice in a list", `{"vision":[{"model":"cc"},{"model":"cc"}],"classify":[{"model":"custom-default"}]}`, "Model cc is listed twice in Describe images."},
		{"negative", `{"result_text_chars":-1,"classify":[{"model":"custom-default"}]}`, "result_text_chars must be 0"},
		{"old single list", `{"models":[{"model":"cc","text":true}]}`, "Invalid task models"},
		{"image_api on text", `{"text":[{"model":"cc","image_api":"images"}]}`, "Invalid task models"},
		{"tts_defaults on stt", `{"stt":[{"model":"cc","tts_defaults":{"voice":"v"}}]}`, "Invalid task models"},
		{"bad image_api", `{"image":[{"model":"cc","image_api":"dalle"}],"classify":[{"model":"custom-default"}]}`, `image_api must be \"chat\" or \"images\"`},
		{"classifier model removed", `{"classify":[{"model":"cc"}]}`, "Agent Main cannot use model custom-default for its classifiers; pick a model in Classification."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveTaskModels(mux, http.MethodPut, tc.body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("status = %d body = %s, want 400 with %q", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
	if *reloads != 1 {
		t.Errorf("a refused save reloaded: %d", *reloads)
	}
	after, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(after.TaskModels.ModelsFor(config.TaskUseText), ","); got != "cc,sonnet" {
		t.Errorf("a refused save changed the file: %+v", after.TaskModels)
	}
}

// A model in a task model list cannot be deleted from the Models page until
// it is taken off; the refusal names the list.
func TestHandleDeleteModel_RefusedWhileATaskModel(t *testing.T) {
	_, mux, _ := setupTaskModelsEnv(t)
	if rec := serveTaskModels(mux, http.MethodPut, `{"classify":[{"model":"custom-default"}],"tts":[{"model":"sonnet"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/models/1", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "task_models.tts[sonnet]") {
		t.Fatalf("delete = %d %s, want a conflict naming the list", rec.Code, rec.Body.String())
	}
}
