package device

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/gatewayproto"
	"github.com/PivotLLM/ClawEh/utils"
)

// dial opens a socket and returns the connection plus the handshake's HTTP
// status. A failed handshake yields a nil conn.
func dial(t *testing.T, wsURL string) (*websocket.Conn, int, error) {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	status := 0
	if resp != nil {
		status = resp.StatusCode
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close dial response body: %v", closeErr)
		}
	}
	if conn != nil {
		t.Cleanup(func() { utils.CloseQuietly(conn) })
	}
	return conn, status, err
}

// dialRaw opens a socket and consumes the challenge, leaving the connection in
// the pre-auth state.
func dialRaw(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	conn, _, err := dial(t, wsURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read challenge: %v", err)
	}
	return conn
}

// paddedFrame is a request frame of about n bytes: the padding sits in an
// ignored params field so the frame still parses.
func paddedFrame(t *testing.T, id, method string, n int) []byte {
	t.Helper()
	params, err := json.Marshal(map[string]string{"pad": strings.Repeat("x", n)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(gatewayproto.RequestFrame{Type: gatewayproto.FrameReq, ID: id, Method: method, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// writeOversized sends a frame the server is expected to refuse. The write may
// itself fail once the server closes mid-frame, which is not a test failure.
func writeOversized(t *testing.T, conn *websocket.Conn, frame []byte) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		t.Logf("oversized write ended early (server closed): %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
}

func detailCode(t *testing.T, err *gatewayproto.ErrorShape) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error response")
	}
	if m, ok := err.Details.(map[string]any); ok {
		if code, ok := m["code"].(string); ok {
			return code
		}
	}
	return ""
}

// TestPreauthReadLimit: before authentication a frame over
// MaxPreauthPayloadBytes closes the connection, while a frame under it is
// still read and answered.
func TestPreauthReadLimit(t *testing.T) {
	_, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1"})

	// Under the limit: parsed and rejected as a bad handshake, so it was read.
	small := dialRaw(t, wsURL)
	if err := small.WriteMessage(websocket.TextMessage, paddedFrame(t, "s", "nope", gatewayproto.MaxPreauthPayloadBytes-1024)); err != nil {
		t.Fatalf("write: %v", err)
	}
	var resp struct {
		Error *gatewayproto.ErrorShape `json:"error"`
	}
	if err := small.ReadJSON(&resp); err != nil {
		t.Fatalf("frame under the pre-auth limit was not answered: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != gatewayproto.CodeInvalidRequest {
		t.Fatalf("expected INVALID_REQUEST for a non-connect frame, got %+v", resp.Error)
	}

	// Over the limit: the server closes without answering.
	big := dialRaw(t, wsURL)
	writeOversized(t, big, paddedFrame(t, "b", "connect", gatewayproto.MaxPreauthPayloadBytes+1))
	_, _, err := big.ReadMessage()
	if err == nil {
		t.Fatal("oversized pre-auth frame was accepted")
	}
	ce := new(websocket.CloseError)
	if errors.As(err, &ce) && ce.Code != websocket.CloseMessageTooBig {
		t.Fatalf("close code %d, want %d", ce.Code, websocket.CloseMessageTooBig)
	}
}

// TestPostauthReadLimit: once authenticated the limit is the advertised
// MaxPayloadBytes — a frame far above the pre-auth limit is served, one above
// MaxPayloadBytes closes the connection.
func TestPostauthReadLimit(t *testing.T) {
	_, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true})
	conn, r := newEmulator(t).open(t, wsURL, "")
	defer utils.CloseQuietly(conn)
	if !r.OK {
		t.Fatalf("handshake failed: %+v", r.Error)
	}

	if err := conn.WriteMessage(websocket.TextMessage, paddedFrame(t, "h1", "health", 4*gatewayproto.MaxPreauthPayloadBytes)); err != nil {
		t.Fatalf("write: %v", err)
	}
	var resp struct {
		ID string `json:"id"`
		OK bool   `json:"ok"`
	}
	for resp.ID != "h1" { // skip any tick
		if err := conn.ReadJSON(&resp); err != nil {
			t.Fatalf("large post-auth frame not answered: %v", err)
		}
	}
	if !resp.OK {
		t.Fatal("health after large frame not ok")
	}

	writeOversized(t, conn, paddedFrame(t, "h2", "health", gatewayproto.MaxPayloadBytes+1))
	for {
		var frame struct {
			ID string `json:"id"`
		}
		if err := conn.ReadJSON(&frame); err != nil {
			return // closed, as required
		}
		if frame.ID == "h2" {
			t.Fatal("frame over MaxPayloadBytes was served")
		}
	}
}

// TestAuthThrottleOnTheWire: after DeviceAuthFailThreshold wrong tokens from
// one client the next connect is refused AUTH_RATE_LIMITED with a retry hint
// before its credentials are looked at, and the first lockout raises one alert.
func TestAuthThrottleOnTheWire(t *testing.T) {
	srv, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true, SharedToken: "right"})
	var mu sync.Mutex
	var alerts []alerter.Alert
	srv.SetAlerter(func(a alerter.Alert) {
		mu.Lock()
		alerts = append(alerts, a)
		mu.Unlock()
	})
	em := newEmulator(t)

	for i := range channels.DeviceAuthFailThreshold {
		r := em.connect(t, wsURL, "wrong")
		if code := detailCode(t, r.Error); code != gatewayproto.DetailAuthTokenMismatch {
			t.Fatalf("attempt %d: detail %q, want %q", i+1, code, gatewayproto.DetailAuthTokenMismatch)
		}
	}

	// Locked out: even the right token is refused now.
	r := em.connect(t, wsURL, "right")
	if code := detailCode(t, r.Error); code != gatewayproto.DetailAuthRateLimited {
		t.Fatalf("detail %q, want %q (%+v)", code, gatewayproto.DetailAuthRateLimited, r.Error)
	}
	if !r.Error.Retryable || r.Error.RetryAfterMs <= 0 || r.Error.RetryAfterMs > int(channels.DeviceAuthLockoutMin.Milliseconds()) {
		t.Fatalf("retry hint: %+v", r.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 1 || alerts[0].Title != "Device authentication locked out" || alerts[0].Priority != alerter.Normal || alerts[0].EventID != "127.0.0.1" {
		t.Fatalf("expected one Normal lockout alert keyed by the client IP, got %+v", alerts)
	}
}

// TestPreauthConnectionCap: DeviceMaxPreauthConns sockets parked in the
// handshake make the next upgrade fail with 503; closing one frees a slot.
func TestPreauthConnectionCap(t *testing.T) {
	_, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1"})

	parked := make([]*websocket.Conn, 0, channels.DeviceMaxPreauthConns)
	for range channels.DeviceMaxPreauthConns {
		parked = append(parked, dialRaw(t, wsURL))
	}

	_, status, err := dial(t, wsURL)
	if err == nil || status != http.StatusServiceUnavailable {
		t.Fatalf("connection over the pre-auth cap: err=%v status=%d, want 503", err, status)
	}

	utils.CloseQuietly(parked[0])
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, dialErr := dial(t, wsURL)
		if dialErr == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot not released after a parked connection closed: %v", dialErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
