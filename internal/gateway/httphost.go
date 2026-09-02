package gateway

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

// httpHost owns the gateway's shared HTTP listener. The listener is started
// once at gateway boot and stays up across config reloads; on reload the
// handler mux is swapped atomically via SetMux and the IP allowlist via
// SetAllowlist. This keeps WebUI WebSocket connections, channel webhooks, the
// WebUI API, and the callback route alive while the channel manager and other
// services are rebuilt.
type httpHost struct {
	server *http.Server
	mux    atomic.Pointer[http.ServeMux]
	allow  atomic.Pointer[middleware.Allowlist]
}

func newHTTPHost(addr string, allowedCIDRs []string) (*httpHost, error) {
	h := &httpHost{}
	// Wrap the dynamic mux dispatch in the IP allowlist so the no-auth WebUI/API
	// (and everything else on this shared port) is reachable only from loopback
	// (always allowed) plus the configured networks — regardless of the bind
	// address. The allowlist is read per request through h.allow, so SetAllowlist
	// can change it on a live listener. Note: it matches the TCP peer
	// (RemoteAddr), so behind a reverse proxy enforce access control at the proxy
	// instead.
	if err := h.SetAllowlist(allowedCIDRs); err != nil {
		return nil, err
	}
	h.server = &http.Server{
		Addr:         addr,
		Handler:      middleware.IPAllowlist(h.allow.Load, http.HandlerFunc(h.serveMux)),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return h, nil
}

// SetAllowlist compiles cidrs and swaps the result in for subsequent requests.
// An empty list means loopback only. Invalid input returns an error and leaves
// the current allowlist untouched, so a bad edit during a config reload cannot
// widen or drop access as a side effect.
func (h *httpHost) SetAllowlist(cidrs []string) error {
	list, err := middleware.CompileAllowlist(cidrs)
	if err != nil {
		return err
	}
	h.allow.Store(list)
	return nil
}

// SetMux atomically swaps the handler mux. In-flight requests served by the
// previous mux continue to completion; subsequent requests use the new mux.
func (h *httpHost) SetMux(mux *http.ServeMux) {
	h.mux.Store(mux)
}

func (h *httpHost) serveMux(w http.ResponseWriter, r *http.Request) {
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
