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
	cfg.TaskModels.Models = []config.TaskModel{{Model: "custom-default", Classify: true, Text: true}}
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

// GET returns the block, its default and every model name once, in config
// order, with what its provider can serve; never a key.
func TestHandleGetTaskModels(t *testing.T) {
	_, mux, _ := setupTaskModelsEnv(t)
	rec := serveTaskModels(mux, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-") {
		t.Fatalf("response carries a key: %s", rec.Body.String())
	}
	var got taskModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Defaults.ResultTextChars != config.DefaultTaskResultTextChars {
		t.Errorf("defaults = %+v", got.Defaults)
	}
	if len(got.TaskModels.Models) != 1 || got.TaskModels.Models[0].Model != "custom-default" {
		t.Errorf("task_models = %+v", got.TaskModels)
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

	body := `{"result_text_chars":3000,"echo_transcription":true,"models":[
		{"model":" sonnet ","vision":true},
		{"model":"custom-default","classify":true,"stt":true,"tts":true,"tts_defaults":{"voice":" alloy ","format":"mp3"}},
		{"model":"cc","text":true,"tts_defaults":{"voice":" ","format":""}}]}`
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
	if tm.ResultTextChars != 3000 || !tm.EchoTranscription || len(tm.Models) != 3 || tm.Models[0].Model != "sonnet" {
		t.Fatalf("saved task_models = %+v", tm)
	}
	if d := tm.Models[1].TTSDefaults; d == nil || d.Voice != "alloy" || d.Format != "mp3" {
		t.Errorf("tts_defaults = %+v", d)
	}
	if tm.Models[2].TTSDefaults != nil {
		t.Errorf("blank tts_defaults kept: %+v", tm.Models[2].TTSDefaults)
	}

	for _, tc := range []struct{ name, body, want string }{
		{"unknown model", `{"models":[{"model":"ghost","stt":true}]}`, `model \"ghost\" does not exist`},
		{"twice", `{"models":[{"model":"cc"},{"model":"cc"}]}`, "Task model cc is listed twice."},
		{"negative", `{"result_text_chars":-1}`, "result_text_chars must be 0"},
		{"misspelt use", `{"models":[{"model":"cc","sst":true}]}`, "Invalid task models"},
		{"classifier model unticked", `{"models":[{"model":"custom-default","text":true}]}`, "Agent Main cannot use model custom-default for its classifiers; pick a Classify model."},
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
	if len(after.TaskModels.Models) != 3 {
		t.Errorf("a refused save changed the file: %+v", after.TaskModels)
	}
}

// A model on the Task models page cannot be deleted from the Models page
// until it is taken off.
func TestHandleDeleteModel_RefusedWhileATaskModel(t *testing.T) {
	_, mux, _ := setupTaskModelsEnv(t)
	if rec := serveTaskModels(mux, http.MethodPut, `{"models":[{"model":"custom-default","classify":true},{"model":"sonnet","vision":true}]}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/models/1", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "task_models.models[sonnet]") {
		t.Fatalf("delete = %d %s, want a conflict naming the task model", rec.Code, rec.Body.String())
	}
}
