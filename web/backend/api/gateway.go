// Package api: in-process gateway status endpoints.
//
// In the merged claw binary the gateway runs inside the same process as the
// WebUI HTTP handlers, so there is no subprocess to spawn, stop, or
// supervise. The /api/gateway/* endpoints below report the in-process state
// to keep the WebUI frontend compatible.

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// gatewayEvents holds the in-process broadcaster for status SSE updates.
// It is package-level so that the gateway code (which lives in cmd/claw) can
// publish events without taking a reference to a Handler.
var gatewayEvents = NewEventBroadcaster()

// PublishGatewayStatus is called by the in-process gateway to notify SSE
// subscribers of state transitions (e.g. on config reload).
func PublishGatewayStatus(event GatewayEvent) {
	gatewayEvents.Broadcast(event)
}

// registerGatewayRoutes binds gateway status endpoints to the ServeMux.
func (h *Handler) registerGatewayRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/gateway/status", h.handleGatewayStatus)
	mux.HandleFunc("GET /api/gateway/events", h.handleGatewayEvents)
	mux.HandleFunc("GET /api/gateway/logs", h.handleGatewayLogs)
	mux.HandleFunc("POST /api/gateway/logs/clear", h.handleGatewayClearLogs)
	mux.HandleFunc("POST /api/gateway/start", h.handleGatewayStart)
	mux.HandleFunc("POST /api/gateway/stop", h.handleGatewayStop)
	mux.HandleFunc("POST /api/gateway/restart", h.handleGatewayRestart)
}

// TryAutoStartGateway is a no-op in the merged binary — the gateway is already
// running in this process by the time any HTTP handler can be invoked. It is
// kept on the Handler so older call sites compile during the transition.
func (h *Handler) TryAutoStartGateway() {}

// EnsureWebUIChannel and other Handler methods are unchanged; see webui.go.

// gatewayStartReady validates whether current config can serve the gateway.
// It is exported via gatewayStatusData() so the WebUI can show "configure a
// model" hints even though the gateway has already started.
func (h *Handler) gatewayStartReady() (bool, string, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return false, "", fmt.Errorf("failed to load config: %w", err)
	}

	modelName := strings.TrimSpace(cfg.Agents.Defaults.DefaultModelName())
	if modelName == "" {
		return false, "no default model configured", nil
	}

	modelCfg := lookupModelConfig(cfg, modelName)
	if modelCfg == nil {
		return false, fmt.Sprintf("default model %q is invalid", modelName), nil
	}

	if !hasModelConfiguration(*modelCfg) {
		return false, fmt.Sprintf("default model %q has no credentials configured", modelName), nil
	}
	if requiresRuntimeProbe(*modelCfg) && !probeLocalModelAvailability(*modelCfg) {
		return false, fmt.Sprintf("default model %q is not reachable", modelName), nil
	}

	return true, "", nil
}

func lookupModelConfig(cfg *config.Config, modelName string) *config.ModelConfig {
	modelCfg, err := cfg.GetModelConfig(modelName)
	if err != nil {
		return nil
	}
	return modelCfg
}

// handleGatewayStatus reports always-running in-process status to the WebUI.
//
//	GET /api/gateway/status
func (h *Handler) handleGatewayStatus(w http.ResponseWriter, r *http.Request) {
	data := h.gatewayStatusData()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (h *Handler) gatewayStatusData() map[string]any {
	data := map[string]any{}
	cfg, cfgErr := config.LoadConfig(h.configPath)
	configDefaultModel := ""
	if cfgErr == nil && cfg != nil {
		configDefaultModel = strings.TrimSpace(cfg.Agents.Defaults.DefaultModelName())
		if configDefaultModel != "" {
			data["config_default_model"] = configDefaultModel
			data["boot_default_model"] = configDefaultModel
		}
	}

	data["gateway_status"] = "running"
	data["gateway_restart_required"] = false

	ready, reason, readyErr := h.gatewayStartReady()
	if readyErr != nil {
		data["gateway_start_allowed"] = false
		data["gateway_start_reason"] = readyErr.Error()
	} else {
		data["gateway_start_allowed"] = ready
		if !ready {
			data["gateway_start_reason"] = reason
		}
	}

	return data
}

// handleGatewayStart is a no-op in the merged binary. The gateway is already
// running in-process; reporting an "already_running" success keeps the WebUI
// happy without exposing a way to spawn a duplicate.
//
//	POST /api/gateway/start
func (h *Handler) handleGatewayStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "already_running"})
}

// handleGatewayStop is not supported in the merged binary: stopping the
// gateway means stopping this process. Returns 405.
//
//	POST /api/gateway/stop
func (h *Handler) handleGatewayStop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusMethodNotAllowed)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "not_supported",
		"message": "gateway runs in-process; stop by terminating claw",
	})
}

// handleGatewayRestart is a soft restart in the merged binary: the file-based
// config watcher already triggers an in-process reload whenever config.json
// is rewritten, so the WebUI's "save config" flow is the canonical reload
// path. This endpoint just acknowledges the intent.
//
//	POST /api/gateway/restart
func (h *Handler) handleGatewayRestart(w http.ResponseWriter, r *http.Request) {
	logger.InfoC("web", "Gateway restart requested via API — reload is triggered by config save")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"message": "reload happens automatically when config.json changes",
	})
}

// handleGatewayClearLogs is a no-op in the merged binary (logs are emitted to
// the same logger as the rest of the process, not a subprocess pipe buffer).
//
//	POST /api/gateway/logs/clear
func (h *Handler) handleGatewayClearLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":     "cleared",
		"log_total":  0,
		"log_run_id": 0,
	})
}

// handleGatewayLogs returns an empty log payload. The merged binary no longer
// keeps a separate log buffer for the gateway subprocess; consumers should
// read the unified claw log file directly.
//
//	GET /api/gateway/logs
func (h *Handler) handleGatewayLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"logs":       []string{},
		"log_total":  0,
		"log_run_id": 0,
	})
}

// handleGatewayEvents streams gateway status events over SSE. In the merged
// binary the gateway is always running, but the stream stays open so the
// frontend can pick up reload notifications (PublishGatewayStatus) and so an
// idle connection doesn't churn the client side reconnect loop.
//
//	GET /api/gateway/events
func (h *Handler) handleGatewayEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := gatewayEvents.Subscribe()
	defer gatewayEvents.Unsubscribe(ch)

	fmt.Fprintf(w, "data: %s\n\n", h.currentGatewayStatus())
	flusher.Flush()

	// Periodic heartbeat keeps proxies and the client EventSource alive in
	// the absence of any state transitions.
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case data, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (h *Handler) currentGatewayStatus() string {
	encoded, _ := json.Marshal(h.gatewayStatusData())
	return string(encoded)
}
