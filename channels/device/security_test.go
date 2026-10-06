package device

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/gatewayproto"
	"github.com/PivotLLM/ClawEh/utils"
)

// recordingQuerier knows the agents amber, wendy and bob and counts History
// reads (each would open that session's store).
type recordingQuerier struct {
	mu      sync.Mutex
	history []string
}

func (q *recordingQuerier) Agents() ([]DeviceAgentInfo, string, string) {
	return []DeviceAgentInfo{{ID: "amber"}, {ID: "wendy"}, {ID: "bob"}}, "amber", "agent:amber:main"
}
func (q *recordingQuerier) DefaultAgentID() string { return "amber" }
func (q *recordingQuerier) History(key string) []DeviceHistoryMessage {
	q.mu.Lock()
	q.history = append(q.history, key)
	q.mu.Unlock()
	return nil
}

func (q *recordingQuerier) reads() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.history...)
}

type resFrame struct {
	Type    string                   `json:"type"`
	ID      string                   `json:"id"`
	OK      bool                     `json:"ok"`
	Event   string                   `json:"event"`
	Payload json.RawMessage          `json:"payload"`
	Error   *gatewayproto.ErrorShape `json:"error"`
}

// readRes reads frames until the response to id, skipping events.
func readRes(t *testing.T, conn *websocket.Conn, id string) resFrame {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		var f resFrame
		if err := conn.ReadJSON(&f); err != nil {
			t.Fatalf("reading response %s: %v", id, err)
		}
		if f.Type == gatewayproto.FrameRes && f.ID == id {
			return f
		}
	}
}

// readUntilClose reads until the connection ends and returns the close code
// the server sent, or -1 when it ended without a close frame.
func readUntilClose(t *testing.T, conn *websocket.Conn, within time.Duration) int {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(within)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if ce, ok := errors.AsType[*websocket.CloseError](err); ok {
				return ce.Code
			}
			if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
				t.Fatalf("connection still open after %s", within)
			}
			return -1
		}
	}
}

func mustOpen(t *testing.T, em *emulator, wsURL, token string) *websocket.Conn {
	t.Helper()
	conn, r := em.open(t, wsURL, token)
	if !r.OK {
		t.Fatalf("connect refused: %+v", r.Error)
	}
	t.Cleanup(func() { utils.CloseQuietly(conn) })
	return conn
}

// H1: removing a device closes all of its open connections at once and leaves
// other devices connected.
func TestRemovedDeviceIsDisconnected(t *testing.T) {
	srv, store, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true})
	em, other := newEmulator(t), newEmulator(t)
	first := mustOpen(t, em, wsURL, "")
	second := mustOpen(t, em, wsURL, "")
	keep := mustOpen(t, other, wsURL, "")

	if err := store.RemovePaired(context.Background(), em.deviceID()); err != nil {
		t.Fatal(err)
	}
	srv.DisconnectDevice(em.deviceID())
	for i, conn := range []*websocket.Conn{first, second} {
		if code := readUntilClose(t, conn, 2*time.Second); code != websocket.ClosePolicyViolation {
			t.Fatalf("connection %d: close code %d, want %d", i+1, code, websocket.ClosePolicyViolation)
		}
	}
	other.writeReq(t, keep, "h1", "health", map[string]any{})
	if r := readRes(t, keep, "h1"); !r.OK {
		t.Fatalf("other device's health refused: %+v", r.Error)
	}
}

// H1: a device removed while connected (without the WebUI's disconnect) can
// send nothing more: its next chat.send is refused and never reaches the agent.
func TestRemovedDeviceRequestRefused(t *testing.T) {
	srv, store, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true})
	var inbound atomic.Int32
	srv.SetInbound(func(string, string, string, string, string, []InboundAttachment) { inbound.Add(1) })
	em := newEmulator(t)
	conn := mustOpen(t, em, wsURL, "")

	if err := store.RemovePaired(context.Background(), em.deviceID()); err != nil {
		t.Fatal(err)
	}
	em.writeReq(t, conn, "s1", "chat.send", map[string]any{"message": "hi", "idempotencyKey": "r1"})
	r := readRes(t, conn, "s1")
	if r.OK || r.Error == nil || r.Error.Code != gatewayproto.CodeNotPaired {
		t.Fatalf("chat.send after removal: ok=%v err=%+v, want NOT_PAIRED", r.OK, r.Error)
	}
	if code := readUntilClose(t, conn, 2*time.Second); code != websocket.ClosePolicyViolation {
		t.Fatalf("close code %d, want %d", code, websocket.ClosePolicyViolation)
	}
	time.Sleep(50 * time.Millisecond)
	if n := inbound.Load(); n != 0 {
		t.Fatalf("inbound called %d times after removal", n)
	}
}

// H1: a connection whose device tokens were rotated away (another connect on
// the shared secret) is refused on its next request too.
func TestRevokedTokenRequestRefused(t *testing.T) {
	_, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true, SharedToken: "shared"})
	em := newEmulator(t)
	old := mustOpen(t, em, wsURL, "shared")
	mustOpen(t, em, wsURL, "shared") // issues fresh tokens, revoking the old ones

	em.writeReq(t, old, "h1", "health", map[string]any{})
	if r := readRes(t, old, "h1"); r.OK {
		t.Fatal("request on a connection with revoked tokens was served")
	}
}

// M7: stopping the channel (a config reload rebuilds it) closes every device
// connection with a close frame, so the client knows to reconnect; nothing is
// acknowledged on the old connection afterwards.
func TestChannelStopClosesConnections(t *testing.T) {
	port := freePort(t)
	dc, err := NewDeviceChannel(config.DeviceChannelConfig{Enabled: true, Host: "127.0.0.1", Port: port, AutoApprove: true},
		t.TempDir(), false, bus.NewMessageBus(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if startErr := dc.Start(context.Background()); startErr != nil {
		t.Fatal(startErr)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	waitFor(t, "listener", func() bool { return canConnect(addr) })
	em := newEmulator(t)
	conn := mustOpen(t, em, "ws://"+addr+"/", "")

	if stopErr := dc.Stop(context.Background()); stopErr != nil {
		t.Fatal(stopErr)
	}
	if code := readUntilClose(t, conn, 2*time.Second); code != websocket.CloseGoingAway {
		t.Fatalf("close code %d, want %d", code, websocket.CloseGoingAway)
	}
	// The client may still get the frame onto the wire, but no ack comes back.
	raw, err := json.Marshal(map[string]any{"message": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(gatewayproto.RequestFrame{Type: gatewayproto.FrameReq, ID: "s1", Method: "chat.send", Params: raw}); err != nil {
		return // refused outright: nothing was sent, so nothing was dropped
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("old connection still answered after the channel stopped")
	}
}

// H2: a device that stops reading is disconnected within the write deadline,
// and neither the replies aimed at it nor another device's traffic wait on it.
func TestSlowReaderDisconnectedWithoutBlocking(t *testing.T) {
	srv, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true})
	stuck, healthy := newEmulator(t), newEmulator(t)
	mustOpen(t, stuck, wsURL, "") // never read from again
	fine := mustOpen(t, healthy, wsURL, "")

	big := strings.Repeat("x", 1<<20)
	start := time.Now()
	for i := range 40 {
		t0 := time.Now()
		// The stuck device is closed once its queue fills: later replies fail.
		if err := srv.DeliverReply(context.Background(), "device:"+stuck.deviceID(), big); err != nil &&
			!errors.Is(err, channels.ErrRecipientOffline) {
			t.Fatalf("DeliverReply %d to a stuck device: %v", i, err)
		}
		if d := time.Since(t0); d > 500*time.Millisecond {
			t.Fatalf("DeliverReply %d to a stuck device blocked for %s", i, d)
		}
	}

	// The healthy device gets its reply promptly while the stuck one is wedged.
	if err := srv.DeliverReply(context.Background(), "device:"+healthy.deviceID(), "hello"); err != nil {
		t.Fatalf("DeliverReply to the healthy device failed: %v", err)
	}
	if err := fine.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		var f resFrame
		if err := fine.ReadJSON(&f); err != nil {
			t.Fatalf("healthy device did not get its reply: %v", err)
		}
		if f.Type == gatewayproto.FrameEvent && f.Event == "chat" {
			break
		}
	}

	stuckOpen := func() bool {
		return len(srv.liveConns(func(lc *liveConn) bool { return lc.deviceID == stuck.deviceID() })) > 0
	}
	deadline := start.Add(deviceWriteTimeout + 3*time.Second)
	for stuckOpen() {
		if time.Now().After(deadline) {
			t.Fatalf("stuck device still connected %s after the first write", time.Since(start))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := srv.DeliverReply(context.Background(), "device:"+stuck.deviceID(), "late"); !errors.Is(err, channels.ErrRecipientOffline) {
		t.Fatalf("DeliverReply to a closed connection = %v, want ErrRecipientOffline", err)
	}
}

func newQuerierServer(t *testing.T) (*Server, *recordingQuerier, *atomic.Int32, string) {
	t.Helper()
	srv, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true})
	q := &recordingQuerier{}
	srv.SetQuerier(q)
	var inbound atomic.Int32
	srv.SetInbound(func(string, string, string, string, string, []InboundAttachment) { inbound.Add(1) })
	return srv, q, &inbound, wsURL
}

// H3: a device cannot read or write another session (the report's probe: a
// Telegram chat key). Any agent-scoped key it sends resolves to that agent's
// main conversation, the one every surface shares.
func TestForeignSessionKeyResolvesToMain(t *testing.T) {
	_, q, inbound, wsURL := newQuerierServer(t)
	em := newEmulator(t)
	conn := mustOpen(t, em, wsURL, "")
	const foreign = "agent:bob:telegram:direct:123456"

	em.writeReq(t, conn, "h1", "chat.history", map[string]any{"sessionKey": foreign})
	if r := readRes(t, conn, "h1"); !r.OK {
		t.Fatalf("chat.history for %s refused: %+v", foreign, r.Error)
	}
	em.writeReq(t, conn, "s1", "chat.send", map[string]any{"sessionKey": foreign, "message": "hi", "idempotencyKey": "r1"})
	if r := readRes(t, conn, "s1"); !r.OK {
		t.Fatalf("chat.send for %s refused: %+v", foreign, r.Error)
	}
	if reads := q.reads(); len(reads) != 1 || reads[0] != "agent:bob:main" {
		t.Fatalf("history reads = %v, want [agent:bob:main]", reads)
	}
	deadline := time.Now().Add(2 * time.Second)
	for inbound.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := inbound.Load(); n != 1 {
		t.Fatalf("inbound called %d times, want 1", n)
	}
}

// M1: chat.history for agents that do not exist is refused before any session
// is read, so random ids cannot create session stores or hold descriptors.
func TestUnknownAgentHistoryOpensNothing(t *testing.T) {
	_, q, _, wsURL := newQuerierServer(t)
	em := newEmulator(t)
	conn := mustOpen(t, em, wsURL, "")

	for i := range 50 {
		id := "h" + strconv.Itoa(i)
		em.writeReq(t, conn, id, "chat.history", map[string]any{"sessionKey": "agent:" + randomToken(6) + ":main"})
		r := readRes(t, conn, id)
		if r.OK || r.Error == nil || r.Error.Message != "unknown agent" {
			t.Fatalf("history %d: ok=%v err=%+v, want unknown agent", i, r.OK, r.Error)
		}
	}
	if reads := q.reads(); len(reads) != 0 {
		t.Fatalf("%d session reads for unknown agents", len(reads))
	}
}

// newProxiedServer serves srv behind the trusted-proxy handler, presenting
// every request as coming from peer (host:port; "" keeps the loopback peer).
func newProxiedServer(t *testing.T, opts ServerOptions, trusted []string, peer string, inner func(http.Handler) http.Handler) (*Server, *Store, string) {
	t.Helper()
	set, err := config.CompileTrustedProxies(trusted)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { utils.CloseQuietly(store) })
	srv := NewServer(store, opts)
	var h http.Handler = http.HandlerFunc(srv.HandleWS)
	if inner != nil {
		h = inner(h)
	}
	h = trustedProxyHandler(set, h)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if peer != "" {
			r.RemoteAddr = peer
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	return srv, store, "ws" + strings.TrimPrefix(hs.URL, "http")
}

func realIP(ip string) http.Header { return http.Header{"X-Real-Ip": []string{ip}} }

// H5 + M6: the pending pairing records the client the trusted proxy names;
// from an untrusted peer the header is ignored.
func TestTrustedProxyPendingAddress(t *testing.T) {
	for _, tc := range []struct {
		name   string
		peer   string
		header http.Header
		want   string
	}{
		{"trusted, X-Real-IP", "192.0.2.1:5000", realIP("203.0.113.7"), "203.0.113.7"},
		{"trusted, X-Forwarded-For", "192.0.2.1:5000", http.Header{"X-Forwarded-For": []string{"203.0.113.8, 192.0.2.1"}}, "203.0.113.8"},
		{"untrusted peer", "198.51.100.9:5000", realIP("203.0.113.7"), "198.51.100.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, store, wsURL := newProxiedServer(t, ServerOptions{ServerVersion: "test-1"}, []string{"192.0.2.1"}, tc.peer, nil)
			em := newEmulator(t)
			conn, r := em.openWith(t, wsURL, "", tc.header)
			utils.CloseQuietly(conn)
			if r.OK {
				t.Fatal("unpaired device accepted")
			}
			pend, err := store.ListPending(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(pend) != 1 || pend[0].RemoteIP != tc.want {
				t.Fatalf("pending = %+v, want RemoteIP %s", pend, tc.want)
			}
		})
	}
}

// H5: behind a trusted proxy the auth lockout keys on the forwarded client, so
// one client's failures do not lock out everyone behind the proxy.
func TestTrustedProxyLockoutKeysOnClient(t *testing.T) {
	_, _, wsURL := newProxiedServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true, SharedToken: "right"},
		[]string{"192.0.2.1"}, "192.0.2.1:5000", nil)
	em := newEmulator(t)
	dialAs := func(ip, token string) connectResp {
		t.Helper()
		conn, r := em.openWith(t, wsURL, token, realIP(ip))
		utils.CloseQuietly(conn)
		return r
	}
	for range channels.DeviceAuthFailThreshold {
		dialAs("203.0.113.7", "wrong")
	}
	if r := dialAs("203.0.113.7", "right"); r.Error == nil || detailCode(t, r.Error) != gatewayproto.DetailAuthRateLimited {
		t.Fatalf("failing client not locked out: %+v", r.Error)
	}
	if r := dialAs("203.0.113.8", "right"); !r.OK {
		t.Fatalf("another client behind the proxy refused: %+v", r.Error)
	}
}

// H5: the device allowlist judges the forwarded client; a trusted loopback
// proxy no longer lets every client through as loopback.
func TestTrustedProxyAllowlist(t *testing.T) {
	inner := func(h http.Handler) http.Handler {
		wrapped, err := ipAllowlistHandler([]string{"10.0.0.0/8"}, h)
		if err != nil {
			t.Fatal(err)
		}
		return wrapped
	}
	_, _, wsURL := newProxiedServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true}, []string{"127.0.0.1"}, "", inner)
	dialAs := func(ip string) int {
		t.Helper()
		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, realIP(ip))
		status := 0
		if resp != nil {
			status = resp.StatusCode
			if closeErr := resp.Body.Close(); closeErr != nil {
				t.Errorf("close dial response body: %v", closeErr)
			}
		}
		if conn != nil {
			utils.CloseQuietly(conn)
		}
		if status == 0 {
			t.Fatalf("dial: %v", err)
		}
		return status
	}
	if code := dialAs("10.1.2.3"); code != http.StatusSwitchingProtocols {
		t.Fatalf("allowed client: status %d", code)
	}
	if code := dialAs("203.0.113.7"); code != http.StatusForbidden {
		t.Fatalf("client outside the allowlist: status %d, want 403", code)
	}
}

// The channel factory, run again on every config reload, applies
// gateway.trusted_proxies.
func TestDeviceFactory_AppliesTrustedProxies(t *testing.T) {
	t.Setenv("CLAW_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Channels.Device.Enabled = true
	cfg.Channels.Device.Token = "shared"
	cfg.Gateway.TrustedProxies = []string{"192.0.2.1"}
	cm, err := channels.NewManager(cfg, bus.NewMessageBus(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ch, ok := cm.Channel("device")
	if !ok {
		t.Fatal("device channel not built")
	}
	dc, ok := ch.(*DeviceChannel)
	if !ok {
		t.Fatalf("device channel is %T", ch)
	}
	t.Cleanup(func() { utils.CloseQuietly(dc.store) })
	if got := dc.trusted.ClientAddr("192.0.2.1:5000", "203.0.113.7", ""); got != "203.0.113.7" {
		t.Fatalf("ClientAddr via the configured proxy = %q, want 203.0.113.7", got)
	}
}
