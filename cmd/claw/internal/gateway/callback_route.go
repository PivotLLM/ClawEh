package gateway

import (
	"io"
	"net/http"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/agent"
	"github.com/PivotLLM/ClawEh/pkg/health"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// RegisterCallbackRoute registers POST /api/reply/{token} on the shared HTTP
// server. It must be called both during initial gateway boot and after every
// config reload, because restartServices rebuilds the shared mux from scratch
// — without re-registration the route silently disappears and every callback
// returns 404. Returns 401 when no valid token is found.
func RegisterCallbackRoute(server *health.Server, agentLoop *agent.AgentLoop) {
	server.Handle("POST /api/reply/{token}", func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		content := strings.TrimSpace(string(body))
		if content == "" {
			http.Error(w, "empty body", http.StatusBadRequest)
			return
		}

		agentID, ok := agentLoop.ValidateCallbackToken(token)
		if !ok {
			logger.WarnCF("callback", "Rejected callback with invalid or expired token",
				map[string]any{"remote_addr": r.RemoteAddr, "body_len": len(content)})
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}

		logger.InfoCF("callback", "Accepted callback",
			map[string]any{"agent": agentID, "remote_addr": r.RemoteAddr, "body_len": len(content)})

		if err := agentLoop.HandleCallbackMessage(r.Context(), agentID, content); err != nil {
			logger.WarnCF("callback", "Failed to deliver callback message",
				map[string]any{"agent": agentID, "error": err.Error()})
			http.Error(w, "failed to deliver message", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusAccepted)
	})
}
