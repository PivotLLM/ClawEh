package gateway

import (
	"io"
	"net/http"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/agent"
	"github.com/PivotLLM/ClawEh/pkg/health"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// handlerRegistrar is the channel-manager surface needed to populate the
// shared mux on boot and on config reload. Defined as an interface here only
// so a test can substitute a fake that captures the rebuilt mux.
type handlerRegistrar interface {
	RegisterHandlers(mux *http.ServeMux, healthServer *health.Server)
}

// muxSwapper is the gateway-level surface used to install a freshly-built mux
// without touching the underlying listener. Real callers pass *httpHost; tests
// substitute a fake that captures the mux for assertion.
type muxSwapper interface {
	SetMux(mux *http.ServeMux)
}

// rebuildSharedHTTPServer builds a new shared health.Server, populates a fresh
// mux with the merged WebUI routes, channel webhook handlers, and the message
// route, then atomically swaps it into the gateway's httpHost. The listener
// itself is NEVER recreated here — that lives in httpHost across reloads, so
// in-flight WebUI WebSocket connections survive a config reload.
//
// Both setupAndStartServices (boot) and restartServices (config reload) go
// through this seam so neither the message route nor the WebUI's /api/*
// routes can disappear after a reload — the route-disappears 404 bug fixed in
// e32731eb is the canonical regression.
func rebuildSharedHTTPServer(services *gatewayServices, host string, port int, cm handlerRegistrar, swapper muxSwapper, al *agent.AgentLoop) {
	services.HealthServer = health.NewServer(host, port)
	mux := buildMergedMux(services.WebServer)
	cm.RegisterHandlers(mux, services.HealthServer)
	RegisterMessageRoute(services.HealthServer, al)
	swapper.SetMux(mux)
}

// RegisterMessageRoute registers POST /api/message/{token} on the shared HTTP
// server: an authenticated way for an external party to deliver a message into
// an agent's active conversation. It must be called both during initial gateway
// boot and after every config reload, because restartServices rebuilds the
// shared mux from scratch — without re-registration the route silently
// disappears and every request returns 404. Returns 401 when no valid token is
// found.
//
// The token is a rotating per-agent token (pkg/msgtoken). Today the agent is not
// told about it, so the endpoint is dormant; it exists so a future "notify an
// agent" feature can use it without re-deriving this delivery path.
func RegisterMessageRoute(server *health.Server, agentLoop *agent.AgentLoop) {
	server.Handle("POST /api/message/{token}", func(w http.ResponseWriter, r *http.Request) {
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

		agentID, ok := agentLoop.ValidateMessageToken(token)
		if !ok {
			logger.WarnCF("message", "Rejected external message with invalid or expired token",
				map[string]any{"remote_addr": r.RemoteAddr, "body_len": len(content)})
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}

		logger.InfoCF("message", "Accepted external message",
			map[string]any{"agent": agentID, "remote_addr": r.RemoteAddr, "body_len": len(content)})

		if err := agentLoop.HandleExternalMessage(r.Context(), agentID, content); err != nil {
			logger.WarnCF("message", "Failed to deliver external message",
				map[string]any{"agent": agentID, "error": err.Error()})
			http.Error(w, "failed to deliver message", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusAccepted)
	})
}
