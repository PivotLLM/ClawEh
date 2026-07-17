package api

import (
	"net/http"
	"sync"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// Handler serves HTTP API requests.
type Handler struct {
	configPath   string
	serverPort   int
	serverPublic bool

	// reloadTrigger, when set by the gateway, forces an immediate config reload
	// (bypassing the mtime-debounce). Guarded because it's set at startup on one
	// goroutine and read on HTTP-handler goroutines.
	reloadMu      sync.Mutex
	reloadTrigger func() error
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

// NewHandler creates an instance of the API handler.
func NewHandler(configPath string) *Handler {
	return &Handler{
		configPath: configPath,
		serverPort: config.DefaultGatewayPort,
	}
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

	// Agent tool catalog
	h.registerAgentRoutes(mux)

	// CLI-agent detection (claude/codex/gemini on PATH) for the setup wizard
	h.registerSystemCLIRoutes(mux)

	// First-run setup status (drives the wizard redirect)
	h.registerSetupStatusRoutes(mux)

	// External-device gateway onboarding + pairing management
	h.registerDeviceRoutes(mux)

	// Named message-API tokens (per-agent, long-lived webhook tokens)
	h.registerMessageTokenRoutes(mux)

	// Speech-to-text (voice transcription) backends
	h.registerVoiceRoutes(mux)
}
