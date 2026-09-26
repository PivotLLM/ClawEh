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
	"time"

	"github.com/gorilla/websocket"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/identity"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/utils"
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
	allowedHosts hostAllowlist
	loopDone     chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
}

// NewDeviceChannel opens the pairing store under <dataDir>/state and builds the
// gateway protocol server. logMessages enables full inbound/outbound content logs.
// The listener answers only to Host names it is known by: localhost, its bind
// host, the hosts of cfg.ExternalURL and gatewayExternalURL, any IP literal,
// and extraHosts (reserved for TLS certificate names).
func NewDeviceChannel(cfg config.DeviceChannelConfig, dataDir string, logMessages bool, b *bus.MessageBus, gatewayExternalURL string, extraHosts []string) (*DeviceChannel, error) {
	stateDir := filepath.Join(dataDir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("device: create state dir: %w", err)
	}
	store, err := OpenStore(context.Background(), filepath.Join(stateDir, "gateway.db"))
	if err != nil {
		return nil, err
	}
	srv := NewServer(store, ServerOptions{
		SharedToken: cfg.Token,
		WordToken:   cfg.WordToken,
		// Protocol handshake: bare semver. Paired devices (the R1, the Android
		// app) read this field; a build suffix could break a version comparison.
		ServerVersion: app.SemVer(),
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
	// The device gateway already authenticates every connection (shared/device
	// token + Ed25519 pairing approval + optional CIDR allowlist), so an unset
	// sender allow-list means "allow any paired device" rather than "deny all" —
	// otherwise a correctly paired device (e.g. the R1) is silently dropped at
	// HandleMessage. Set allow_from explicitly to restrict which paired devices
	// may talk to an agent.
	allowFrom := cfg.AllowFrom
	if len(allowFrom) == 0 {
		allowFrom = []string{"*"}
	}
	dc := &DeviceChannel{
		BaseChannel:  channels.NewBaseChannel("device", cfg, b, allowFrom),
		server:       srv,
		store:        store,
		host:         host,
		port:         port,
		allowedCIDRs: cfg.AllowedCIDRs,
		allowedHosts: newHostAllowlist(host, []string{cfg.ExternalURL, gatewayExternalURL}, extraHosts),
	}
	srv.SetAlerter(dc.Alert)
	// Bridge each device utterance into the message bus. The agent's reply returns
	// via Send -> server.DeliverReply.
	srv.SetInbound(func(deviceID, chatID, content, idempotencyKey, sessionKey string, attachments []InboundAttachment) {
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
		// Pin the conversation session (per-device / per-profile isolation, and the
		// key chat.history reads). The agent is the session key's 2nd segment; the
		// loop falls back to default routing when it's "main"/unknown.
		if sessionKey != "" {
			metadata["session_key"] = sessionKey
			if agentID := routing.AgentIDFromSessionKey(sessionKey); agentID != "" {
				metadata["preresolved_agent_id"] = agentID
			}
		}
		// Persist inline attachments (photos) to the media store; the agent loop
		// materializes the refs and its vision-describe path handles non-vision models.
		mediaRefs := dc.storeInboundAttachments(chatID, idempotencyKey, attachments)
		logger.InfoCF("device", "inbound → bus", map[string]any{
			"deviceId": deviceID, "chatId": chatID, "sessionKey": sessionKey,
			"preresolvedAgent": metadata["preresolved_agent_id"], "chars": len(content), "media": len(mediaRefs),
		})
		dc.HandleMessage(ctx, peer, idempotencyKey, deviceID, chatID, content, mediaRefs, metadata, sender)
	})
	return dc, nil
}

// storeInboundAttachments writes each decoded attachment to the media staging
// dir and registers it in the media store, returning the resulting "media://"
// refs (scoped to this turn so they're cleaned up with it). Best-effort: a
// missing store or a failed write drops that attachment with a warning.
func (c *DeviceChannel) storeInboundAttachments(chatID, messageID string, atts []InboundAttachment) []string {
	if len(atts) == 0 {
		return nil
	}
	store := c.GetMediaStore()
	if store == nil {
		logger.WarnCF("device", "media store unavailable; dropping attachments", map[string]any{"chatId": chatID, "count": len(atts)})
		return nil
	}
	mediaDir := utils.MediaTempDir()
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		logger.WarnCF("device", "create media dir failed; dropping attachments", map[string]any{"dir": mediaDir, "error": err.Error()})
		return nil
	}
	scope := channels.BuildMediaScope("device", chatID, messageID)
	refs := make([]string, 0, len(atts))
	for i, a := range atts {
		ext := extForMIME(a.MimeType)
		f, err := os.CreateTemp(mediaDir, "device-media-*"+ext)
		if err != nil {
			logger.WarnCF("device", "attachment temp file failed", map[string]any{"error": err.Error()})
			continue
		}
		_, werr := f.Write(a.Data)
		if closeErr := f.Close(); closeErr != nil && werr == nil {
			werr = closeErr
		}
		if werr != nil {
			if rmErr := os.Remove(f.Name()); rmErr != nil {
				logger.WarnCF("device", "attachment temp file remove failed", map[string]any{"path": f.Name(), "error": rmErr.Error()})
			}
			logger.WarnCF("device", "attachment write failed", map[string]any{"error": werr.Error()})
			continue
		}
		name := a.Name
		if name == "" {
			name = "attachment-" + strconv.Itoa(i) + ext
		}
		ref, serr := store.Store(f.Name(), media.MediaMeta{Filename: name, ContentType: a.MimeType, Source: "device"}, scope)
		if serr != nil {
			logger.WarnCF("device", "media store failed for attachment", map[string]any{"error": serr.Error()})
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

// extForMIME maps a few common attachment MIME types to a file extension so the
// stored file (and downstream MIME sniffing) behaves. Defaults to ".bin".
func extForMIME(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/heic":
		return ".heic"
	default:
		return ".bin"
	}
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
	wrapped, err := ipAllowlistHandler(c.allowedCIDRs, hostCheckHandler(c.allowedHosts, handler))
	if err != nil {
		return fmt.Errorf("device: %w", err)
	}

	addr := net.JoinHostPort(c.host, strconv.Itoa(c.port))
	ln, err := listenTCP(addr)
	if err != nil {
		return fmt.Errorf("device: listen %s: %w", addr, err)
	}
	c.loopDone = make(chan struct{})
	c.SetRunning(true)
	go c.serveLoop(ln, addr, wrapped)
	logger.InfoCF("device", "Device gateway listening", map[string]any{"addr": addr})
	return nil
}

// Test seams: listenTCP binds the listener and retryAfter waits between
// re-listen attempts.
var (
	listenTCP  = func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }
	retryAfter = time.After
)

// serveLoop serves ln until the channel's context is cancelled. If Serve fails
// it re-listens on addr with channels.NextConnRetry backoff (a failed re-bind,
// such as the port being in use, waits the same way), raising "Channel receive
// loop stopped" once per outage and logging when the listener is back.
func (c *DeviceChannel) serveLoop(ln net.Listener, addr string, handler http.Handler) {
	defer close(c.loopDone)
	var backoff time.Duration
	for {
		err := c.serve(ln, addr, handler)
		if c.ctx.Err() != nil {
			return
		}
		// Each Serve failure opens an outage: alert once here, not on the
		// re-bind attempts that follow.
		backoff = channels.NextConnRetry(backoff)
		logger.ErrorCF("device", "Device gateway listener error; re-listening", map[string]any{
			"addr": addr, "error": err.Error(), "retry_in": backoff.String(),
		})
		c.Alert(alerter.Alert{
			Title:       "Channel receive loop stopped",
			Description: c.Name() + ": device gateway listener error; re-listening on " + addr + " with backoff until it is back",
			Details:     err.Error(),
		})
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-retryAfter(backoff):
			}
			ln, err = listenTCP(addr)
			if err == nil {
				break
			}
			backoff = channels.NextConnRetry(backoff)
			logger.WarnCF("device", "Device gateway re-listen failed", map[string]any{
				"addr": addr, "error": err.Error(), "retry_in": backoff.String(),
			})
		}
		logger.InfoCF("device", "Device gateway listener restored", map[string]any{"addr": addr})
		backoff = 0
	}
}

// serve runs one http.Server on ln until it fails or the channel's context is
// cancelled, which closes the server (and ln) so Serve returns. It always
// returns a non-nil error; the caller decides whether it was a stop or a fault.
func (c *DeviceChannel) serve(ln net.Listener, addr string, handler http.Handler) error {
	// No Read/WriteTimeout: long-lived WebSocket connections manage their own
	// deadlines after the gorilla upgrade hijacks the conn.
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	// Close immediately rather than graceful Shutdown: a live device WebSocket
	// would otherwise block the shutdown (and a config reload) for seconds. The
	// device reconnects after the listener re-binds.
	stop := context.AfterFunc(c.ctx, func() { utils.CloseQuietly(srv) })
	defer stop()
	return srv.Serve(ln)
}

// Stop shuts down the device listener and store. It returns once the serve
// loop has exited, so the port is free for a restarted channel to bind.
func (c *DeviceChannel) Stop(_ context.Context) error {
	c.SetRunning(false)
	if c.cancel != nil {
		c.cancel()
	}
	if c.loopDone != nil {
		<-c.loopDone
	}
	if c.store != nil {
		utils.CloseQuietly(c.store)
	}
	logger.InfoC("device", "Device gateway stopped")
	return nil
}

// StreamDelta implements channels.StreamCapable — forwards a partial-assistant-
// text delta to the connected device as incremental chat/agent stream events.
// Best-effort: a missing connection is a no-op (the terminal Send remains
// authoritative).
func (c *DeviceChannel) StreamDelta(_ context.Context, chatID, delta string) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}
	c.server.StreamDelta(chatID, delta)
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
