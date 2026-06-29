package channels

import "net/http"

// WebhookHandler is an optional interface for channels that receive messages
// via HTTP webhooks. Manager discovers channels implementing this interface
// and registers them on the shared HTTP server.
type WebhookHandler interface {
	// WebhookPath returns the path to mount this handler on the shared server.
	// Example: "/webhook/line"
	WebhookPath() string
	http.Handler // ServeHTTP(w http.ResponseWriter, r *http.Request)
}

// HealthChecker is an optional interface for channels that expose
// a health check endpoint on the shared HTTP server.
type HealthChecker interface {
	HealthPath() string
	HealthHandler(w http.ResponseWriter, r *http.Request)
}

// RootWebSocketClaimer is an optional interface for a channel that should receive
// root-path ("/") WebSocket upgrades on the shared listener. OpenClaw-protocol
// devices (e.g. the Rabbit R1) connect to ws://host:port with no path, so their
// upgrade arrives at "/". A channel returning true gets those upgrades while normal
// HTTP "/" continues to serve the SPA.
type RootWebSocketClaimer interface {
	http.Handler
	ClaimsRootWebSocket() bool
}
