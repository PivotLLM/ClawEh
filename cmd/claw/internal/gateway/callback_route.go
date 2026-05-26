package gateway

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/agent"
	"github.com/PivotLLM/ClawEh/pkg/health"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// httpServerInstaller is the channel-manager surface needed to rebuild the
// shared HTTP server on config reload. Defined as an interface here only so a
// test can substitute a fake that captures the rebuilt mux.
type httpServerInstaller interface {
	SetupHTTPServer(addr string, healthServer *health.Server, mux *http.ServeMux)
}

// rebuildSharedHTTPServer builds a new shared health.Server, wires it into the
// channel manager's mux, and re-registers the callback route and the merged
// WebUI routes. Both setupAndStartServices (boot, indirectly via gatewayCmd)
// and restartServices (config reload) go through this seam so neither the
// callback route nor the WebUI's /api/* routes can disappear after a config
// reload — the callback-404 bug fixed in e32731eb is the canonical regression,
// and the merged-binary work in this branch makes the same seam load-bearing
// for the WebUI API surface.
func rebuildSharedHTTPServer(services *gatewayServices, host string, port int, cm httpServerInstaller, al *agent.AgentLoop) {
	addr := fmt.Sprintf("%s:%d", host, port)
	services.HealthServer = health.NewServer(host, port)
	cm.SetupHTTPServer(addr, services.HealthServer, buildMergedMux(services.WebServer))
	RegisterCallbackRoute(services.HealthServer, al)
}

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
