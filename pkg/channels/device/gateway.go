package device

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gorilla/websocket"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/identity"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// DeviceChannel is the external-device gateway channel. It mounts an OpenClaw
// Gateway-protocol WebSocket endpoint on the shared HTTP server so hardware
// devices (e.g. the Rabbit R1) can connect, pair, and converse with an agent.
type DeviceChannel struct {
	*channels.BaseChannel
	server *Server
	store  *Store
	path   string
	ctx    context.Context
	cancel context.CancelFunc
}

// NewDeviceChannel opens the pairing store under <dataDir>/state and builds the
// gateway protocol server.
func NewDeviceChannel(cfg config.DeviceChannelConfig, dataDir string, b *bus.MessageBus) (*DeviceChannel, error) {
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
		ServerVersion: global.Version,
		AutoApprove:   cfg.AutoApprove,
		AllowOrigins:  cfg.AllowOrigins,
	})
	path := cfg.Path
	if path == "" {
		path = "/gateway/"
	}
	dc := &DeviceChannel{
		BaseChannel: channels.NewBaseChannel("device", cfg, b, cfg.AllowFrom),
		server:      srv,
		store:       store,
		path:        path,
	}
	// Bridge each device utterance into the message bus. The agent's reply returns
	// via Send -> server.DeliverReply.
	srv.SetInbound(func(deviceID, chatID, content, idempotencyKey string) {
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
		dc.HandleMessage(ctx, peer, idempotencyKey, deviceID, chatID, content, nil, metadata, sender)
	})
	return dc, nil
}

// Start implements channels.Channel.
func (c *DeviceChannel) Start(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.SetRunning(true)
	logger.InfoCF("device", "Device gateway channel started", map[string]any{"path": c.path})
	return nil
}

// Stop implements channels.Channel.
func (c *DeviceChannel) Stop(_ context.Context) error {
	c.SetRunning(false)
	if c.cancel != nil {
		c.cancel()
	}
	if c.store != nil {
		_ = c.store.Close()
	}
	logger.InfoC("device", "Device gateway channel stopped")
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

// WebhookPath implements channels.WebhookHandler — the shared-mux mount point.
func (c *DeviceChannel) WebhookPath() string { return c.path }

// ServeHTTP upgrades WebSocket requests to the gateway protocol.
func (c *DeviceChannel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		http.NotFound(w, r)
		return
	}
	c.server.HandleWS(w, r)
}
