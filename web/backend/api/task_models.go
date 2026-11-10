// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

// registerTaskModelRoutes binds the Task models page's endpoints.
func (h *Handler) registerTaskModelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/task-models", h.handleGetTaskModels)
	mux.HandleFunc("PUT /api/task-models", h.handleUpdateTaskModels)
}

// taskModelOption is a model_list name the page may add, with whether its
// provider can serve each use, judged from the protocol: "yes", "no" or
// "unknown". A name with several entries (load balancing) is "no" for a use
// when any of its enabled entries cannot serve it.
type taskModelOption struct {
	Name    string            `json:"name"`
	Enabled bool              `json:"enabled"`
	Support map[string]string `json:"support"`
}

// taskModelsResponse is GET /api/task-models: the task_models block (every
// list present, in order, read through Entries), its defaults and the models
// it may name.
type taskModelsResponse struct {
	TaskModels map[string]any `json:"task_models"`
	Defaults   struct {
		ResultTextChars int `json:"result_text_chars"`
	} `json:"defaults"`
	Models []taskModelOption `json:"models"`
}

// handleGetTaskModels returns the task_models block and every model on the
// Models page, in config order, with what its provider can serve. No API keys
// or endpoints are returned.
//
//	GET /api/task-models
func (h *Handler) handleGetTaskModels(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.currentConfig()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	resp := taskModelsResponse{TaskModels: taskModelsBlock(cfg.TaskModels), Models: taskModelOptions(cfg)}
	resp.Defaults.ResultTextChars = config.DefaultTaskResultTextChars
	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, resp)
}

// taskModelsBlock is the task_models block as the page reads it: the two
// settings and one array per use, empty when the list is.
func taskModelsBlock(tm config.TaskModelsConfig) map[string]any {
	block := map[string]any{
		"result_text_chars":  tm.ResultTextChars,
		"echo_transcription": tm.EchoTranscription,
	}
	for _, use := range config.TaskUses {
		block[string(use)] = tm.Entries(use)
	}
	return block
}

// taskModelOptions lists each model_name once, in config order.
func taskModelOptions(cfg *config.Config) []taskModelOption {
	out := []taskModelOption{}
	index := map[string]int{}
	for i := range cfg.Models {
		m := &cfg.Models[i]
		at, seen := index[m.ModelName]
		if !seen {
			at = len(out)
			index[m.ModelName] = at
			out = append(out, taskModelOption{Name: m.ModelName, Support: map[string]string{}})
		}
		opt := &out[at]
		if !m.Enabled {
			continue
		}
		opt.Enabled = true
		protocol := ""
		if p, perr := cfg.GetProvider(m.Provider); perr == nil {
			protocol = p.Protocol
		}
		for _, use := range config.TaskUses {
			s := supportWord(config.TaskUseSupportFor(protocol, use))
			if prev, ok := opt.Support[string(use)]; ok && (prev == "no" || s == "yes") {
				continue // "no" from any entry wins; "yes" never overrides
			}
			opt.Support[string(use)] = s
		}
	}
	return out
}

func supportWord(s config.TaskUseSupport) string {
	switch s {
	case config.TaskUseYes:
		return "yes"
	case config.TaskUseNo:
		return "no"
	default:
		return "unknown"
	}
}

// handleUpdateTaskModels replaces the task_models block. The store refuses an
// unknown model, a model listed twice in a list, a negative limit and a
// change that leaves an agent's classifiers without a classifier model.
//
//	PUT /api/task-models  {"result_text_chars": ..., "echo_transcription": ..., "text": [...], "image": [...], ...}
func (h *Handler) handleUpdateTaskModels(w http.ResponseWriter, r *http.Request) {
	defer utils.CloseQuietly(r.Body)
	var body config.TaskModelsConfig
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeValidationErrors(w, []string{fmt.Sprintf("Invalid task models: %v", err)})
		return
	}
	body.Normalize()

	if err := h.updateConfig(r, func(cfg *config.Config) error {
		cfg.TaskModels = body
		return nil
	}); err != nil {
		writeUpdateError(w, err)
		return
	}
	// Reload so the running agent loop rebuilds its transcriber and the
	// agents' vision chains.
	if reload := h.reloadFunc(); reload != nil {
		if reloadErr := reload(); reloadErr != nil {
			logger.WarnCF("api", "config reload failed", map[string]any{"error": reloadErr.Error()})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]string{"status": "ok"})
}
