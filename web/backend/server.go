package webserver

import (
	"net/http"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/web/backend/api"
)

// Options configures the in-process web server before it is mounted on the
// gateway's shared HTTP mux.
type Options struct {
	// Store is the gateway's live configuration. The API handlers read and
	// write through it, so the WebUI shows and edits the same in-memory config
	// the gateway runs on. When nil, a store is opened on ConfigPath instead.
	Store *config.Store

	// ConfigPath is the absolute path of the active claw config.json, used only
	// when Store is nil.
	ConfigPath string

	// ListenPort is the port the gateway HTTP server listens on. It is
	// surfaced via SetServerOptions so that /api/* responses that report the
	// "current" listen address are accurate.
	ListenPort int

	// Public mirrors the gateway's all-interfaces bind (Gateway.Host ==
	// "0.0.0.0"). The API uses this hint to pick between localhost and the LAN
	// host when constructing the WebUI WebSocket URL.
	Public bool
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
	var h *api.Handler
	if opts.Store != nil {
		h = api.NewHandlerWithStore(opts.Store)
	} else {
		h = api.NewHandler(opts.ConfigPath)
	}
	h.SetServerOptions(opts.ListenPort, opts.Public)
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
