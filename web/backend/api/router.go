package api

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/channels/device"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

// Handler serves HTTP API requests.
type Handler struct {
	configPath   string
	serverPort   int
	serverPublic bool

	// store is the live configuration every handler reads and writes: one
	// in-memory config, saved under a lock, shared with the gateway when it
	// passes its own store in (NewHandlerWithStore). NewHandler(configPath)
	// opens one from the file on first use; a load failure is reported to that
	// request and retried by the next.
	storeMu sync.Mutex
	store   *config.Store

	// reloadTrigger, when set by the gateway, forces an immediate config reload
	// (bypassing the mtime-debounce). Guarded because it's set at startup on one
	// goroutine and read on HTTP-handler goroutines.
	reloadMu      sync.Mutex
	reloadTrigger func() error
	// alertsPath is the alerts log the gateway writes (SetAlertsPath); empty
	// when alerting is disabled. Guarded by reloadMu.
	alertsPath string
	// alerter raises operator alerts from handlers (SetAlerter); a no-op until
	// the gateway sets it. Guarded by reloadMu.
	alerter alerter.Alerter
	// msgTokenLoop is the live AgentLoop the message-token endpoints operate on
	// (injected via SetMessageTokenLoop). Guarded by reloadMu since it is set at
	// startup on one goroutine and read on HTTP-handler goroutines.
	msgTokenLoop messageTokenLoop
	// secmsgLinker resolves a configured secmsg channel name to its live linker
	// so the WebUI QR pairing panel can reach the running channel instance
	// (injected via SetSecMsgLinker). Guarded by reloadMu like the fields above.
	secmsgLinker SecMsgLinkerLookup
	// mcpStatusLoop is the live AgentLoop the MCP status endpoint reads outbound
	// connection state from (injected via SetMCPStatusLoop). Guarded by reloadMu.
	mcpStatusLoop mcpStatusLoop
	// sessionReleaser closes the running loop's handles on a session before
	// DELETE /api/sessions removes its archive (SetSessionReleaser). Nil until
	// the gateway sets it. Guarded by reloadMu.
	sessionReleaser func(sessionKey string) error
	// auth is the gateway's login session store (SetAuth); nil until the
	// gateway sets it, which the auth endpoints report as "no admin account".
	// Guarded by reloadMu.
	auth *middleware.AuthStore
	// loginLimiter applies the failed-login backoff. Owned by the handler for
	// its lifetime; safe for concurrent use.
	loginLimiter *middleware.LoginLimiter

	// deviceStore caches the pairing DB handle. It used to be opened and closed
	// per request, which re-ran the WAL pragma, the schema and a failing
	// ALTER TABLE on every call and raced the device channel for the same file.
	// deviceStorePath records which file the cached handle belongs to, so a data
	// dir change on config reload reopens rather than serving the old database.
	deviceMu        sync.Mutex
	deviceStore     *device.Store
	deviceStorePath string
}

// SetReloadTrigger wires the gateway's force-reload function into the handler so
// POST /api/gateway/reload can apply config changes immediately.
func (h *Handler) SetReloadTrigger(fn func() error) {
	h.reloadMu.Lock()
	h.reloadTrigger = fn
	h.reloadMu.Unlock()
}

func (h *Handler) reloadFunc() func() error {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	return h.reloadTrigger
}

// SetAlerter routes the handlers' operator alerts to a; nil restores the no-op.
func (h *Handler) SetAlerter(a alerter.Alerter) {
	h.reloadMu.Lock()
	h.alerter = a
	h.reloadMu.Unlock()
}

func (h *Handler) alerterRef() alerter.Alerter {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	if h.alerter == nil {
		return alerter.Nop{}
	}
	return h.alerter
}

// NewHandler creates an API handler that opens its own config.Store on
// configPath. The gateway uses NewHandlerWithStore so the API and the gateway
// share one in-memory configuration; this constructor serves tests and tools
// that have only the path.
func NewHandler(configPath string) *Handler {
	return &Handler{
		configPath:   configPath,
		serverPort:   config.DefaultGatewayPort,
		loginLimiter: middleware.NewLoginLimiter(time.Now),
	}
}

// NewHandlerWithStore creates an API handler on an existing config store, so
// every handler reads and writes the same configuration the gateway runs on.
func NewHandlerWithStore(store *config.Store) *Handler {
	h := NewHandler(store.Path())
	h.store = store
	return h
}

// configStore returns the handler's config store, opening it from configPath
// on first use when none was injected.
func (h *Handler) configStore() (*config.Store, error) {
	h.storeMu.Lock()
	defer h.storeMu.Unlock()
	if h.store != nil {
		return h.store, nil
	}
	st, err := config.NewStore(h.configPath)
	if err != nil {
		return nil, err
	}
	h.store = st
	return st, nil
}

// currentConfig returns the live configuration. It is shared: read it, do not
// change it — changes go through updateConfig.
func (h *Handler) currentConfig() (*config.Config, error) {
	st, err := h.configStore()
	if err != nil {
		return nil, err
	}
	return st.Current(), nil
}

// updateConfig applies fn to the live configuration under the store's write
// lock, saving and publishing the result. See config.Store.Update.
func (h *Handler) updateConfig(fn func(cfg *config.Config) error) error {
	st, err := h.configStore()
	if err != nil {
		return err
	}
	return st.Update(fn)
}

// httpError is an error raised inside an updateConfig callback that already
// knows how the request should fail (a 404 for an index out of range, a 400
// for a validation failure). writeUpdateError sends it as-is.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &httpError{status: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...any) error {
	return &httpError{status: http.StatusNotFound, msg: fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) error {
	return &httpError{status: http.StatusConflict, msg: fmt.Sprintf(format, args...)}
}

// writeUpdateError reports an updateConfig failure: an *httpError carries its
// own status, a *config.ValidationError is the client's doing (400), anything
// else is a failed save (500).
func writeUpdateError(w http.ResponseWriter, err error) {
	if herr, ok := errors.AsType[*httpError](err); ok {
		http.Error(w, herr.msg, herr.status)
		return
	}
	if verr, ok := errors.AsType[*config.ValidationError](err); ok {
		http.Error(w, fmt.Sprintf("Validation error: %v", verr), http.StatusBadRequest)
		return
	}
	http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
}

// SetServerOptions stores the current backend listen options. serverPublic
// mirrors the gateway's all-interfaces bind and feeds the WebUI WebSocket URL
// host (see gateway_host.go); serverPort records the active listen port.
func (h *Handler) SetServerOptions(port int, public bool) {
	h.serverPort = port
	h.serverPublic = public
}

// RegisterRoutes binds all API endpoint handlers to the ServeMux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Login, logout and session status (exempt from the auth middleware)
	h.registerAuthRoutes(mux)

	// Config CRUD
	h.registerConfigRoutes(mux)

	// WebUI Channel (WebSocket chat)
	h.registerWebUIRoutes(mux)

	// Gateway process lifecycle
	h.registerGatewayRoutes(mux)

	// Session history
	h.registerSessionRoutes(mux)

	// Cognitive-memory browser (read-only)
	h.registerMemoryRoutes(mux)

	// OAuth login and credential management

	// Named provider management
	h.registerProviderRoutes(mux)

	// Model list management
	h.registerModelRoutes(mux)

	// Channel catalog (for frontend navigation/config pages)
	h.registerChannelRoutes(mux)

	// SecMsg (secure-messaging daemon) device linking / QR pairing
	h.registerSecMsgRoutes(mux)

	// Outbound MCP client connection status (live health of external servers)
	h.registerMCPStatusRoutes(mux)

	// Skills and tools support/actions
	h.registerSkillRoutes(mux)
	h.registerToolRoutes(mux)

	// Running ClawEh build version (shown in the WebUI sidebar footer)
	h.registerVersionRoutes(mux)

	// Configuration report (PDF)
	h.registerReportRoutes(mux)

	// Audit log (tool calls, config writes, auth events)
	h.registerAuditRoutes(mux)

	// Agent tool catalog
	h.registerAgentRoutes(mux)

	// CLI-agent detection (claude/codex/gemini on PATH) for the setup wizard
	h.registerSystemCLIRoutes(mux)
	h.registerSystemStatusRoutes(mux)

	// First-run setup status (drives the wizard redirect)
	h.registerSetupStatusRoutes(mux)

	// External-device gateway onboarding + pairing management
	h.registerDeviceRoutes(mux)

	// Named message-API tokens (per-agent, long-lived webhook tokens)
	h.registerMessageTokenRoutes(mux)

	// Speech-to-text (voice transcription) backends
	h.registerVoiceRoutes(mux)
}
