package device

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/PivotLLM/ClawEh/pkg/gatewayproto"
)

// emulator is a minimal OpenClaw-protocol device client used to exercise the
// server handshake end to end (stands in for the Rabbit R1 until on-wire capture).
type emulator struct {
	t    *testing.T
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newEmulator(t *testing.T) *emulator {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &emulator{t: t, pub: pub, priv: priv}
}

func (e *emulator) deviceID() string { return gatewayproto.DeviceIDFromPublicKey(e.pub) }

type connectResp struct {
	OK      bool
	Payload json.RawMessage
	Error   *gatewayproto.ErrorShape
}

// open dials the gateway, completes the challenge, and sends a signed connect
// request, returning the LIVE connection plus the parsed response. Caller closes.
func (e *emulator) open(t *testing.T, wsURL, sharedToken string) (*websocket.Conn, connectResp) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Read the pre-auth challenge.
	var ch struct {
		Type    string                        `json:"type"`
		Event   string                        `json:"event"`
		Payload gatewayproto.ChallengePayload `json:"payload"`
	}
	if err := conn.ReadJSON(&ch); err != nil {
		t.Fatalf("read challenge: %v", err)
	}
	if ch.Type != gatewayproto.FrameEvent || ch.Event != gatewayproto.EventConnectChallenge || ch.Payload.Nonce == "" {
		t.Fatalf("unexpected challenge: %+v", ch)
	}

	signedAt := time.Now().UnixMilli()
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	in := gatewayproto.DeviceAuthInput{
		DeviceID: e.deviceID(), ClientID: "rabbit-r1", ClientMode: gatewayproto.ModeNode,
		Role: gatewayproto.RoleNode, SignedAtMs: signedAt, Token: sharedToken,
		Nonce: ch.Payload.Nonce, Platform: "rabbit", DeviceFamily: "r1",
	}
	sig := b64(ed25519.Sign(e.priv, []byte(gatewayproto.BuildDeviceAuthPayloadV3(in))))

	params := gatewayproto.ConnectParams{
		MinProtocol: 4, MaxProtocol: 4,
		Client: gatewayproto.ClientInfo{ID: "rabbit-r1", Version: "1.0", Platform: "rabbit", DeviceFamily: "r1", Mode: gatewayproto.ModeNode},
		Role:   gatewayproto.RoleNode,
		Device: &gatewayproto.DeviceIdentity{ID: e.deviceID(), PublicKey: b64(e.pub), Signature: sig, SignedAt: signedAt, Nonce: ch.Payload.Nonce},
	}
	if sharedToken != "" {
		params.Auth = &gatewayproto.ConnectAuth{Token: sharedToken}
	}
	rawParams, _ := json.Marshal(params)
	if err := conn.WriteJSON(gatewayproto.RequestFrame{Type: gatewayproto.FrameReq, ID: "c1", Method: "connect", Params: rawParams}); err != nil {
		t.Fatalf("write connect: %v", err)
	}

	var resp struct {
		Type    string                   `json:"type"`
		ID      string                   `json:"id"`
		OK      bool                     `json:"ok"`
		Payload json.RawMessage          `json:"payload"`
		Error   *gatewayproto.ErrorShape `json:"error"`
	}
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("read connect response: %v", err)
	}
	if resp.Type != gatewayproto.FrameRes || resp.ID != "c1" {
		t.Fatalf("unexpected response frame: %+v", resp)
	}
	return conn, connectResp{resp.OK, resp.Payload, resp.Error}
}

// connect runs a one-shot handshake and closes the connection.
func (e *emulator) connect(t *testing.T, wsURL, sharedToken string) connectResp {
	conn, resp := e.open(t, wsURL, sharedToken)
	_ = conn.Close()
	return resp
}

// writeReq sends a request frame on an open connection.
func (e *emulator) writeReq(t *testing.T, conn *websocket.Conn, id, method string, params any) {
	t.Helper()
	raw, _ := json.Marshal(params)
	if err := conn.WriteJSON(gatewayproto.RequestFrame{Type: gatewayproto.FrameReq, ID: id, Method: method, Params: raw}); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
}

func newTestServer(t *testing.T, opts ServerOptions) (*Server, *Store, string) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := NewServer(store, opts)
	hs := httptest.NewServer(http.HandlerFunc(srv.HandleWS))
	t.Cleanup(hs.Close)
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http")
	return srv, store, wsURL
}

func detailRequestID(t *testing.T, err *gatewayproto.ErrorShape) string {
	t.Helper()
	m, ok := err.Details.(map[string]any)
	if !ok {
		t.Fatalf("error details not an object: %#v", err.Details)
	}
	id, _ := m["requestId"].(string)
	if id == "" {
		t.Fatalf("no requestId in details: %#v", m)
	}
	return id
}

// TestHandshakePairingFlow exercises the full Tier-1 loop: first connect creates a
// pending pairing and is rejected NOT_PAIRED; after approval, reconnect yields hello-ok.
func TestHandshakePairingFlow(t *testing.T) {
	ctx := context.Background()
	_, store, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1"})
	em := newEmulator(t)

	// First connect: unknown device -> pending + NOT_PAIRED.
	r1 := em.connect(t, wsURL, "")
	if r1.OK || r1.Error == nil || r1.Error.Code != gatewayproto.CodeNotPaired {
		t.Fatalf("expected NOT_PAIRED, got ok=%v err=%+v", r1.OK, r1.Error)
	}
	reqID := detailRequestID(t, r1.Error)

	// The pending request is recorded with the device id.
	pend, _ := store.ListPending(ctx)
	if len(pend) != 1 || pend[0].DeviceID != em.deviceID() {
		t.Fatalf("pending not recorded: %+v", pend)
	}

	// Operator approves.
	if _, _, err := store.Approve(ctx, reqID, []string{gatewayproto.RoleNode}, nil); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Reconnect: now paired -> hello-ok.
	r2 := em.connect(t, wsURL, "")
	if !r2.OK || r2.Error != nil {
		t.Fatalf("expected hello-ok, got ok=%v err=%+v", r2.OK, r2.Error)
	}
	var hello gatewayproto.HelloOk
	if err := json.Unmarshal(r2.Payload, &hello); err != nil {
		t.Fatalf("decode hello-ok: %v", err)
	}
	if hello.Type != "hello-ok" || hello.Protocol != gatewayproto.ProtocolVersion {
		t.Fatalf("bad hello-ok: %+v", hello)
	}
	if hello.Auth.Role != gatewayproto.RoleNode || hello.Auth.DeviceToken == "" {
		t.Fatalf("hello-ok auth missing role/token: %+v", hello.Auth)
	}
}

// TestHandshakeAutoApprove verifies the dev/LAN auto-approve path yields hello-ok on first connect.
func TestHandshakeAutoApprove(t *testing.T) {
	_, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true})
	em := newEmulator(t)
	r := em.connect(t, wsURL, "")
	if !r.OK || r.Error != nil {
		t.Fatalf("expected hello-ok with auto-approve, got ok=%v err=%+v", r.OK, r.Error)
	}
}

// TestHandshakeSharedToken verifies shared-token auth: wrong/missing token is rejected.
func TestHandshakeSharedToken(t *testing.T) {
	_, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true, SharedToken: "secret-token"})
	em := newEmulator(t)

	// Wrong token (empty) -> rejected before pairing.
	bad := em.connect(t, wsURL, "")
	if bad.OK || bad.Error == nil || bad.Error.Code != gatewayproto.CodeInvalidRequest {
		t.Fatalf("expected auth failure, got ok=%v err=%+v", bad.OK, bad.Error)
	}

	// Correct token -> hello-ok (auto-approve on).
	good := em.connect(t, wsURL, "secret-token")
	if !good.OK || good.Error != nil {
		t.Fatalf("expected hello-ok with correct token, got ok=%v err=%+v", good.OK, good.Error)
	}
}

// TestConversationEcho exercises the post-handshake conversation surface: subscribe,
// chat.send -> ack, and the agent reply delivered as a terminal chat event. A fake
// inbound (echo) stands in for the agent loop.
func TestConversationEcho(t *testing.T) {
	srv, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true})
	srv.SetInbound(func(_, chatID, content, _, _ string) {
		srv.DeliverReply(chatID, "echo: "+content)
	})

	em := newEmulator(t)
	conn, resp := em.open(t, wsURL, "")
	defer func() { _ = conn.Close() }()
	if !resp.OK {
		t.Fatalf("handshake failed: %+v", resp.Error)
	}

	em.writeReq(t, conn, "s1", "node.event", map[string]any{"event": "chat.subscribe", "payload": map[string]any{"sessionKey": "sess-1"}})
	em.writeReq(t, conn, "m1", "chat.send", map[string]any{"message": "hi there", "sessionKey": "sess-1", "idempotencyKey": "run-1"})

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var sawAck, sawFinal bool
	for i := 0; i < 10 && !sawFinal; i++ {
		var f struct {
			Type    string          `json:"type"`
			ID      string          `json:"id"`
			OK      bool            `json:"ok"`
			Payload json.RawMessage `json:"payload"`
			Event   string          `json:"event"`
		}
		if err := conn.ReadJSON(&f); err != nil {
			t.Fatalf("read frame: %v", err)
		}
		switch f.Type {
		case gatewayproto.FrameRes:
			if f.ID == "m1" && f.OK {
				var ack struct {
					RunID  string `json:"runId"`
					Status string `json:"status"`
				}
				_ = json.Unmarshal(f.Payload, &ack)
				if ack.Status == "started" && ack.RunID == "run-1" {
					sawAck = true
				}
			}
		case gatewayproto.FrameEvent:
			if f.Event == "chat" {
				var ce struct {
					RunID      string `json:"runId"`
					State      string `json:"state"`
					SessionKey string `json:"sessionKey"`
					Message    struct {
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"message"`
				}
				_ = json.Unmarshal(f.Payload, &ce)
				if ce.State != "final" {
					continue
				}
				if ce.RunID != "run-1" || ce.SessionKey != "sess-1" {
					t.Fatalf("bad chat event identity: %+v", ce)
				}
				if len(ce.Message.Content) == 0 || ce.Message.Content[0].Text != "echo: hi there" {
					t.Fatalf("bad reply text: %+v", ce.Message)
				}
				sawFinal = true
			}
		}
	}
	if !sawAck {
		t.Fatal("never saw chat.send ack")
	}
	if !sawFinal {
		t.Fatal("never saw chat final event")
	}
}

func TestAgentIDFromSessionKey(t *testing.T) {
	cases := map[string]string{
		"agent:claw:clawtotalk:primary": "claw",
		"agent:bob:clawtotalk:primary":  "bob",
		"agent:main:clawtotalk:primary": "", // no-selection sentinel -> default routing
		"agent:claw:main":               "claw",
		"main":                          "", // R1-style key, not agent-scoped
		"":                              "",
		"agent:":                        "",
	}
	for in, want := range cases {
		if got := agentIDFromSessionKey(in); got != want {
			t.Errorf("agentIDFromSessionKey(%q) = %q, want %q", in, got, want)
		}
	}
}
