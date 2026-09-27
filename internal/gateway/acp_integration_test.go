package gateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a3tai/openclaw-go/identity"
	"github.com/a3tai/openclaw-go/protocol"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels/device"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
	"github.com/PivotLLM/ClawEh/utils"
)

const acpTestToken = "acp-integration-shared-token"

// acpTestGateway is a real device channel on a free loopback port, wired the
// way the gateway wires it, with the bus it publishes inbound messages to.
type acpTestGateway struct {
	dataDir string
	dev     config.DeviceChannelConfig
	channel *device.DeviceChannel
	bus     *bus.MessageBus
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is %T", ln.Addr())
	}
	port := addr.Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// startACPTestGateway starts the device channel on dataDir. With useTLS the
// channel serves the gateway's self-signed certificate, generated under
// dataDir/tls by internal/tlscert and lent through SetTLSConfig as
// injectDeviceTLS does.
func startACPTestGateway(t *testing.T, dataDir string, useTLS bool) *acpTestGateway {
	t.Helper()
	g := &acpTestGateway{
		dataDir: dataDir,
		dev: config.DeviceChannelConfig{
			Enabled: true,
			Token:   acpTestToken,
			Host:    "127.0.0.1",
			Port:    freeLoopbackPort(t),
			TLS:     useTLS,
		},
		bus: bus.NewMessageBus(),
	}
	t.Cleanup(g.bus.Close)
	dc, err := device.NewDeviceChannel(g.dev, dataDir, false, g.bus, "", nil)
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	if useTLS {
		certs, err := tlscert.Load(tlscert.Options{DataDir: dataDir})
		if err != nil {
			t.Fatalf("tlscert.Load: %v", err)
		}
		dc.SetTLSConfig(certs.TLSConfig())
	}
	if err := dc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := dc.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	g.channel = dc
	return g
}

// connectArgs resolves the URL and TLS configuration as acpCmd does.
func (g *acpTestGateway) connectArgs() (string, *tls.Config) {
	wsURL := defaultDeviceWSURL(g.dev.Host, g.dev.Port, g.dev.TLS)
	if !strings.HasPrefix(wsURL, "wss://") {
		return wsURL, nil
	}
	return wsURL, pinnedGatewayTLS(tlscert.Options{DataDir: g.dataDir})
}

// eventLog collects the gateway events the bridge client receives.
type eventLog struct {
	mu     sync.Mutex
	events []protocol.Event
}

func (l *eventLog) add(ev protocol.Event) {
	l.mu.Lock()
	l.events = append(l.events, ev)
	l.mu.Unlock()
}

// chatFinal returns the text of the chat "final" event for runID, if one has
// arrived.
func (l *eventLog) chatFinal(runID string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ev := range l.events {
		if ev.EventName != protocol.EventChat {
			continue
		}
		var p struct {
			RunID   string `json:"runId"`
			State   string `json:"state"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.RunID == runID && p.State == "final" {
			return p.Message.Text, true
		}
	}
	return "", false
}

func (g *acpTestGateway) connect(t *testing.T, autoPair bool, events *eventLog) (*bridgeConn, error) {
	t.Helper()
	wsURL, tlsConfig := g.connectArgs()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	onEvent := func(protocol.Event) {}
	if events != nil {
		onEvent = events.add
	}
	return connectBridge(ctx, g.dataDir, wsURL, acpTestToken, tlsConfig, autoPair, onEvent)
}

func openDeviceStore(t *testing.T, dataDir string) *device.Store {
	t.Helper()
	store, err := device.OpenStore(context.Background(), filepath.Join(dataDir, "state", "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { utils.CloseQuietly(store) })
	return store
}

func storedBridgeToken(t *testing.T, dataDir string) string {
	t.Helper()
	st, err := identity.NewStore(filepath.Join(dataDir, "state", "acp-bridge"))
	if err != nil {
		t.Fatal(err)
	}
	return st.LoadDeviceToken()
}

// TestACPBridgeAgainstDeviceChannel drives the `claw acp` connect path against
// a real device channel, over ws:// and over wss:// with the pinned gateway
// certificate: the first connect is rejected as not paired, the bridge
// approves its own pairing and reconnects, a chat round trip reaches the
// channel and back, and a second connect reuses the stored identity without
// pairing again.
func TestACPBridgeAgainstDeviceChannel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		useTLS bool
	}{
		{"ws", false},
		{"wss", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			g := startACPTestGateway(t, dataDir, tc.useTLS)
			if wsURL, _ := g.connectArgs(); !strings.HasPrefix(wsURL, tc.name+"://") {
				t.Fatalf("bridge URL %q, want %s://", wsURL, tc.name)
			}

			// Without auto-pairing the handshake is authenticated but the
			// device is refused as not paired, leaving a pending request.
			if _, err := g.connect(t, false, nil); err == nil || !isPairingError(err) {
				t.Fatalf("first connect without auto-pair: err = %v, want a not-paired rejection", err)
			}
			store := openDeviceStore(t, dataDir)
			ctx := context.Background()
			pending, err := store.ListPending(ctx)
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending after not-paired rejection = %v (err %v), want one", pending, err)
			}
			deviceID := pending[0].DeviceID

			// With auto-pairing the bridge approves itself and reconnects.
			events := &eventLog{}
			conn, err := g.connect(t, true, events)
			if err != nil {
				t.Fatalf("connect with auto-pair: %v", err)
			}
			t.Cleanup(func() { utils.CloseQuietly(conn.client) })
			if !conn.selfApproved {
				t.Fatal("connect did not report the self-approval")
			}
			if conn.identity.DeviceID != deviceID {
				t.Fatalf("paired device %s, want the pending one %s", conn.identity.DeviceID, deviceID)
			}
			hello := conn.client.Hello()
			if hello == nil || hello.Protocol == 0 {
				t.Fatalf("hello-ok = %+v, want an accepted handshake", hello)
			}
			if _, ok, pairErr := store.GetPaired(ctx, deviceID); pairErr != nil || !ok {
				t.Fatalf("device not paired after self-approval (ok=%v err=%v)", ok, pairErr)
			}
			if storedBridgeToken(t, dataDir) == "" {
				t.Fatal("no device token stored after pairing")
			}

			// Chat round trip: chat.send reaches the channel's bus, and the
			// reply sent through the channel comes back as a chat final event.
			const runID = "acp-run-1"
			ack, err := conn.client.ChatSend(ctx, protocol.ChatSendParams{SessionKey: "main", Message: "hello from acp", IdempotencyKey: runID})
			if err != nil {
				t.Fatalf("chat.send: %v", err)
			}
			if ack.RunID != runID {
				t.Fatalf("ack runId %q, want %q", ack.RunID, runID)
			}
			recvCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			in, ok := g.bus.ConsumeInbound(recvCtx)
			if !ok {
				t.Fatal("chat.send never reached the bus")
			}
			if in.Channel != "device" || in.Content != "hello from acp" || in.Sender.PlatformID != deviceID {
				t.Fatalf("inbound = %+v", in)
			}
			if sendErr := g.channel.Send(ctx, bus.OutboundMessage{Channel: "device", ChatID: in.ChatID, Content: "hello from claw"}); sendErr != nil {
				t.Fatalf("channel Send: %v", sendErr)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				if text, ok := events.chatFinal(runID); ok {
					if text != "hello from claw" {
						t.Fatalf("reply text %q", text)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no chat final event for the run")
				}
				time.Sleep(20 * time.Millisecond)
			}
			utils.CloseQuietly(conn.client)

			// A second connect reuses the stored identity and device token.
			again, err := g.connect(t, true, nil)
			if err != nil {
				t.Fatalf("second connect: %v", err)
			}
			utils.CloseQuietly(again.client)
			if again.selfApproved {
				t.Fatal("second connect paired again")
			}
			if again.identity.DeviceID != deviceID {
				t.Fatalf("second connect used device %s, want %s", again.identity.DeviceID, deviceID)
			}
			if storedBridgeToken(t, dataDir) == "" {
				t.Fatal("no device token stored after the second connect")
			}
			if pending, err := store.ListPending(ctx); err != nil || len(pending) != 0 {
				t.Fatalf("pending after reconnect = %v (err %v), want none", pending, err)
			}
			if paired, err := store.ListPaired(ctx); err != nil || len(paired) != 1 {
				t.Fatalf("paired devices = %d (err %v), want 1", len(paired), err)
			}
		})
	}
}

// TestACPBridgePinRejectsOtherCertificate: the bridge refuses a gateway whose
// certificate is not the one in its own data directory.
func TestACPBridgePinRejectsOtherCertificate(t *testing.T) {
	g := startACPTestGateway(t, t.TempDir(), true)
	other := t.TempDir()
	if _, err := tlscert.Load(tlscert.Options{DataDir: other}); err != nil {
		t.Fatalf("tlscert.Load: %v", err)
	}
	wsURL, _ := g.connectArgs()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// The identity lives with the gateway; only the pinned certificate differs.
	_, err := connectBridge(ctx, g.dataDir, wsURL, acpTestToken, pinnedGatewayTLS(tlscert.Options{DataDir: other}), true, func(protocol.Event) {})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("connect with a different pinned certificate: err = %v, want a certificate mismatch", err)
	}
	if isPairingError(err) {
		t.Fatalf("certificate mismatch reported as a pairing error: %v", err)
	}
}
