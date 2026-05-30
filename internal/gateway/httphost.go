package gateway

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// httpHost owns the gateway's shared HTTP listener. The listener is started
// once at gateway boot and stays up across config reloads; on reload the
// handler mux is swapped atomically via SetMux. This keeps WebUI WebSocket
// connections, channel webhooks, the WebUI API, and the callback route alive
// while the channel manager and other services are rebuilt.
type httpHost struct {
	server *http.Server
	mux    atomic.Pointer[http.ServeMux]
}

func newHTTPHost(addr string) *httpHost {
	h := &httpHost{}
	h.server = &http.Server{
		Addr:         addr,
		Handler:      http.HandlerFunc(h.serveHTTP),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return h
}

// SetMux atomically swaps the handler mux. In-flight requests served by the
// previous mux continue to completion; subsequent requests use the new mux.
func (h *httpHost) SetMux(mux *http.ServeMux) {
	h.mux.Store(mux)
}

func (h *httpHost) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if m := h.mux.Load(); m != nil {
		m.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

// Start launches the listener in its own goroutine. It returns immediately.
func (h *httpHost) Start() {
	go func() {
		logger.InfoCF("gateway", "Shared HTTP server listening", map[string]any{
			"addr": h.server.Addr,
		})
		if err := h.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.ErrorCF("gateway", "Shared HTTP server error", map[string]any{
				"error": err.Error(),
			})
		}
	}()
}

// Stop shuts down the listener. Only invoked on full gateway shutdown — never
// during a config reload.
func (h *httpHost) Stop(ctx context.Context) error {
	return h.server.Shutdown(ctx)
}
