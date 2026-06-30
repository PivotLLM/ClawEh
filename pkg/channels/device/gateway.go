package device

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/identity"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// DefaultDevicePort is the device gateway's own listen port when unset. It sits
// next to the default WebUI/admin port (18790) so a default install runs both on
// adjacent ports.
const DefaultDevicePort = 18791

// DeviceChannel is the external-device gateway channel. It runs its OWN HTTP
// listener (Host:Port) — independent of the WebUI/admin port — serving the
// OpenClaw Gateway WebSocket protocol so hardware devices (e.g. the Rabbit R1)
// can connect, pair, and converse with an agent. Keeping it on a separate,
// authenticated listener lets it be exposed to the network without exposing the
// unauthenticated WebUI.
type DeviceChannel struct {
	*channels.BaseChannel
	server       *Server
	store        *Store
	host         string
	port         int
	allowedCIDRs []string
	httpSrv      *http.Server
	ctx          context.Context
	cancel       context.CancelFunc
}

// NewDeviceChannel opens the pairing store under <dataDir>/state and builds the
// gateway protocol server. logMessages enables full inbound/outbound content logs.
func NewDeviceChannel(cfg config.DeviceChannelConfig, dataDir string, logMessages bool, b *bus.MessageBus) (*DeviceChannel, error) {
	stateDir := filepath.Join(dataDir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("device: create state dir: %w", err)
	}
	store, err := OpenStore(filepath.Join(stateDir, "gateway.db"))
	if err != nil {
		return nil, err
	}
	srv := NewServer(store, ServerOptions{
		SharedToken:   cfg.Token,
		WordToken:     cfg.WordToken,
		ServerVersion: global.Version,
		AutoApprove:   cfg.AutoApprove,
		AllowOrigins:  cfg.AllowOrigins,
		LogMessages:   logMessages,
	})
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = DefaultDevicePort
	}
	dc := &DeviceChannel{
		BaseChannel:  channels.NewBaseChannel("device", cfg, b, cfg.AllowFrom),
		server:       srv,
		store:        store,
		host:         host,
		port:         port,
		allowedCIDRs: cfg.AllowedCIDRs,
	}
	// Bridge each device utterance into the message bus. The agent's reply returns
	// via Send -> server.DeliverReply.
	srv.SetInbound(func(deviceID, chatID, content, idempotencyKey, agentID string) {
		ctx := dc.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		peer := bus.Peer{Kind: "direct", ID: deviceID}
		sender := bus.SenderInfo{
			Platform:    "device",
			PlatformID:  deviceID,
			CanonicalID: identity.BuildCanonicalID("device", deviceID),
		}
		metadata := map[string]string{"platform": "device", "device_id": deviceID}
		// The operator client encodes its selected agent in the session key; route the
		// turn to that agent. The loop falls back to default routing if it's unknown.
		if agentID != "" {
			metadata["preresolved_agent_id"] = agentID
		}
		dc.HandleMessage(ctx, peer, idempotencyKey, deviceID, chatID, content, nil, metadata, sender)
	})
	return dc, nil
}

// SetAgentQuerier wires the read-only agent/session accessor into the protocol
// server so operator clients can call agents.list / chat.history. Injected from
// internal/gateway (which owns the agent loop) after the channel is built.
func (c *DeviceChannel) SetAgentQuerier(q AgentQuerier) { c.server.SetQuerier(q) }

// Start launches the device gateway's own HTTP listener.
func (c *DeviceChannel) Start(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)

	// WebSocket upgrade at any path (devices connect to ws://host:port/ with no path).
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !websocket.IsWebSocketUpgrade(r) {
			http.NotFound(w, r)
			return
		}
		c.server.HandleWS(w, r)
	})
	wrapped, err := ipAllowlistHandler(c.allowedCIDRs, handler)
	if err != nil {
		return fmt.Errorf("device: %w", err)
	}

	addr := net.JoinHostPort(c.host, strconv.Itoa(c.port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("device: listen %s: %w", addr, err)
	}
	// No Read/WriteTimeout: long-lived WebSocket connections manage their own
	// deadlines after the gorilla upgrade hijacks the conn.
	c.httpSrv = &http.Server{Addr: addr, Handler: wrapped}
	c.SetRunning(true)
	go func() {
		if serveErr := c.httpSrv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			logger.ErrorCF("device", "Device gateway listener error", map[string]any{"error": serveErr.Error()})
		}
	}()
	logger.InfoCF("device", "Device gateway listening", map[string]any{"addr": addr})
	return nil
}

// Stop shuts down the device listener and store.
func (c *DeviceChannel) Stop(_ context.Context) error {
	c.SetRunning(false)
	if c.cancel != nil {
		c.cancel()
	}
	if c.httpSrv != nil {
		// Close immediately rather than graceful Shutdown: a live device WebSocket
		// would otherwise block the shutdown (and a config reload) for seconds. The
		// device reconnects after the listener re-binds.
		_ = c.httpSrv.Close()
	}
	if c.store != nil {
		_ = c.store.Close()
	}
	logger.InfoC("device", "Device gateway stopped")
	return nil
}

// Send implements channels.Channel — routes an agent reply to the device WS as a
// terminal chat event, keyed by the inbound chatID ("device:<deviceID>").
func (c *DeviceChannel) Send(_ context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}
	if c.server.DeliverReply(msg.ChatID, msg.Content) {
		return nil
	}
	return channels.ErrSendFailed
}

// ipAllowlistHandler restricts the device listener to the given CIDRs (loopback
// always allowed). An empty list allows any client IP — the gateway is itself
// authenticated, so the allowlist is optional defense-in-depth.
func ipAllowlistHandler(cidrs []string, next http.Handler) (http.Handler, error) {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("invalid allowed_cidrs entry %q: %w", c, err)
		}
		nets = append(nets, n)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(nets) == 0 || clientIPAllowed(r.RemoteAddr, nets) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}), nil
}

func clientIPAllowed(remoteAddr string, nets []*net.IPNet) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
