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
	"github.com/PivotLLM/ClawEh/pkg/media"
	"github.com/PivotLLM/ClawEh/pkg/utils"
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
	}
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
			if agentID := agentIDFromSessionKey(sessionKey); agentID != "" {
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
		_ = f.Close()
		if werr != nil {
			_ = os.Remove(f.Name())
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
