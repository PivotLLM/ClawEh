package webserver

import (
	"net/http"

	"github.com/PivotLLM/ClawEh/web/backend/api"
)

// Options configures the in-process web server before it is mounted on the
// gateway's shared HTTP mux.
type Options struct {
	// ConfigPath is the absolute path of the active claw config.json. The web
	// API handlers load/save this file when serving CRUD requests, so it must
	// match the config that the surrounding gateway loaded at boot.
	ConfigPath string

	// ListenPort is the port the gateway HTTP server listens on. It is
	// surfaced via SetServerOptions so that /api/* responses that report the
	// "current" listen address (e.g. /api/webui/token's ws_url) are accurate.
	ListenPort int

	// Public mirrors the launcher's --public flag. With the launcher gone the
	// gateway's own Gateway.Host setting determines bind interface, but the
	// API still uses this hint to pick between localhost and the LAN host
	// when constructing the WebUI WebSocket URL.
	Public bool

	// AllowedCIDRs is the launcher's IP allowlist. Currently consumed only by
	// the launcher_config endpoints; the gateway-shared mux does not enforce
	// it (that would require wrapping the entire mux in middleware, which is
	// out of scope for the in-process merge).
	AllowedCIDRs []string
}

// Server bundles the API handler with its mount-time configuration so callers
// can register both the JSON API and the embedded frontend on a single mux.
//
// The merged claw binary instantiates one Server and calls RegisterRoutes
// against the same http.ServeMux that backs the gateway's HTTP server, so
// /api/*, /webui/* (WebSocket), /health, /ready and the frontend SPA all
// share port 18790. The SPA fallback at "/" is registered last and Go's
// http.ServeMux precedence rules ensure the more specific channel/health
// patterns continue to win.
type Server struct {
	apiHandler *api.Handler
	opts       Options
}

// New constructs a Server. The returned value is safe to use concurrently
// once RegisterRoutes has been called; the underlying api.Handler holds its
// own mutexes for OAuth state and config writes.
func New(opts Options) *Server {
	h := api.NewHandler(opts.ConfigPath)
	h.SetServerOptions(opts.ListenPort, opts.Public, opts.Public, opts.AllowedCIDRs)
	return &Server{apiHandler: h, opts: opts}
}

// APIHandler exposes the underlying api.Handler so the gateway can ensure
// the WebUI channel is configured before the channels.Manager initialises
// channels from the config file.
func (s *Server) APIHandler() *api.Handler {
	return s.apiHandler
}

// RegisterRoutes mounts the WebUI JSON API and the embedded frontend SPA on
// mux. It must be called before channels.Manager.SetupHTTPServer so that the
// channel webhook handlers and health endpoints have the chance to register
// more specific patterns that take precedence over the SPA's "/" fallback.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	s.apiHandler.RegisterRoutes(mux)
	RegisterEmbedRoutes(mux)
}
