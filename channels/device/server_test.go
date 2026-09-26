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

	"github.com/PivotLLM/ClawEh/gatewayproto"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/utils"
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
	conn, httpResp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if httpResp != nil && httpResp.Body != nil {
		if closeErr := httpResp.Body.Close(); closeErr != nil {
			t.Errorf("close dial response body: %v", closeErr)
		}
	}
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
	rawParams, marshalErr := json.Marshal(params)
	if marshalErr != nil {
		t.Fatalf("marshal connect params: %v", marshalErr)
	}
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
	t.Helper()
	conn, resp := e.open(t, wsURL, sharedToken)
	if err := conn.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	return resp
}

// writeReq sends a request frame on an open connection.
func (e *emulator) writeReq(t *testing.T, conn *websocket.Conn, id, method string, params any) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s params: %v", method, err)
	}
	if err := conn.WriteJSON(gatewayproto.RequestFrame{Type: gatewayproto.FrameReq, ID: id, Method: method, Params: raw}); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
}

func newTestServer(t *testing.T, opts ServerOptions) (*Server, *Store, string) {
	t.Helper()
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
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
	id, ok := m["requestId"].(string)
	if !ok || id == "" {
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
	pend, err := store.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
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

// helloOf decodes a successful connect response's hello-ok payload.
func helloOf(t *testing.T, r connectResp) gatewayproto.HelloOk {
	t.Helper()
	if !r.OK || r.Error != nil {
		t.Fatalf("expected hello-ok, got ok=%v err=%+v", r.OK, r.Error)
	}
	var hello gatewayproto.HelloOk
	if err := json.Unmarshal(r.Payload, &hello); err != nil {
		t.Fatalf("decode hello-ok: %v", err)
	}
	return hello
}

// TestHandshakeDeviceTokenReconnect pins the token lifecycle now that the store
// keeps hashes: a device that reconnects on its own token gets that token echoed
// back; one that reconnects on the shared secret is issued fresh tokens and its
// old ones stop authenticating.
func TestHandshakeDeviceTokenReconnect(t *testing.T) {
	_, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true, SharedToken: "secret-token"})
	em := newEmulator(t)

	first := helloOf(t, em.connect(t, wsURL, "secret-token"))
	tok1 := first.Auth.DeviceToken
	if tok1 == "" || tok1 == "secret-token" {
		t.Fatalf("first hello-ok should carry a device token, got %+v", first.Auth)
	}

	// Reconnect on the device token: accepted, and the same token is echoed.
	echoed := helloOf(t, em.connect(t, wsURL, tok1))
	if echoed.Auth.DeviceToken != tok1 {
		t.Fatalf("device-token reconnect echoed %q, want the presented token", echoed.Auth.DeviceToken)
	}
	if len(echoed.Auth.DeviceTokens) != 1 || echoed.Auth.DeviceTokens[0].DeviceToken != tok1 {
		t.Fatalf("deviceTokens = %+v, want just the presented token", echoed.Auth.DeviceTokens)
	}

	// Reconnect on the shared secret: tokens rotate.
	rotated := helloOf(t, em.connect(t, wsURL, "secret-token"))
	tok2 := rotated.Auth.DeviceToken
	if tok2 == "" || tok2 == tok1 {
		t.Fatalf("shared-secret reconnect should issue a new token, got %q (was %q)", tok2, tok1)
	}
	if stale := em.connect(t, wsURL, tok1); stale.OK || stale.Error == nil || stale.Error.Code != gatewayproto.CodeInvalidRequest {
		t.Fatalf("rotated-away token should be rejected, got ok=%v err=%+v", stale.OK, stale.Error)
	}
	if again := helloOf(t, em.connect(t, wsURL, tok2)); again.Auth.DeviceToken != tok2 {
		t.Fatalf("new token reconnect echoed %q, want %q", again.Auth.DeviceToken, tok2)
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
	srv.SetInbound(func(_, chatID, content, _, _ string, _ []InboundAttachment) {
		srv.DeliverReply(chatID, "echo: "+content)
	})

	em := newEmulator(t)
	conn, resp := em.open(t, wsURL, "")
	defer utils.CloseQuietly(conn)
	if !resp.OK {
		t.Fatalf("handshake failed: %+v", resp.Error)
	}

	em.writeReq(t, conn, "s1", "node.event", map[string]any{"event": "chat.subscribe", "payload": map[string]any{"sessionKey": "sess-1"}})
	em.writeReq(t, conn, "m1", "chat.send", map[string]any{"message": "hi there", "sessionKey": "sess-1", "idempotencyKey": "run-1"})

	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
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
				if err := json.Unmarshal(f.Payload, &ack); err != nil {
					t.Fatalf("decode ack: %v", err)
				}
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
				if err := json.Unmarshal(f.Payload, &ce); err != nil {
					t.Fatalf("decode chat event: %v", err)
				}
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

// collectedFrame is a decoded event/response frame captured by readFrames.
type collectedFrame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	OK      bool            `json:"ok"`
	Payload json.RawMessage `json:"payload"`
	Event   string          `json:"event"`
}

// readFramesUntilFinal reads frames until it sees a "chat" event with
// state:"final" (or the deadline elapses), returning every frame read.
func readFramesUntilFinal(t *testing.T, conn *websocket.Conn) []collectedFrame {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var frames []collectedFrame
	for range 30 {
		var f collectedFrame
		if err := conn.ReadJSON(&f); err != nil {
			t.Fatalf("read frame: %v", err)
		}
		frames = append(frames, f)
		if f.Type == gatewayproto.FrameEvent && f.Event == "chat" {
			var ce struct {
				State string `json:"state"`
			}
			if err := json.Unmarshal(f.Payload, &ce); err != nil {
				t.Fatalf("decode chat event: %v", err)
			}
			if ce.State == "final" {
				return frames
			}
		}
	}
	t.Fatal("never saw chat final event")
	return nil
}

// agentAssistantTexts extracts data.text from every "agent" stream:"assistant"
// event in frames, in order.
func agentAssistantTexts(frames []collectedFrame) []string {
	var texts []string
	for _, f := range frames {
		if f.Type != gatewayproto.FrameEvent || f.Event != "agent" {
			continue
		}
		var ae struct {
			Stream string `json:"stream"`
			Data   struct {
				Text string `json:"text"`
			} `json:"data"`
		}
		if json.Unmarshal(f.Payload, &ae) != nil {
			continue
		}
		if ae.Stream == "assistant" {
			texts = append(texts, ae.Data.Text)
		}
	}
	return texts
}

// agentAssistantSeqs extracts the per-run payload seq from every "agent"
// stream:"assistant" event, in order. Clients key events by (runId, seq), so
// these must strictly increment across streamed deltas or later ones are dropped.
func agentAssistantSeqs(frames []collectedFrame) []uint64 {
	var seqs []uint64
	for _, f := range frames {
		if f.Type != gatewayproto.FrameEvent || f.Event != "agent" {
			continue
		}
		var ae struct {
			Stream string `json:"stream"`
			Seq    uint64 `json:"seq"`
		}
		if json.Unmarshal(f.Payload, &ae) != nil {
			continue
		}
		if ae.Stream == "assistant" {
			seqs = append(seqs, ae.Seq)
		}
	}
	return seqs
}

// chatDeltaTexts extracts deltaText from every "chat" state:"delta" event.
func chatDeltaTexts(frames []collectedFrame) []string {
	var deltas []string
	for _, f := range frames {
		if f.Type != gatewayproto.FrameEvent || f.Event != "chat" {
			continue
		}
		var ce struct {
			State     string `json:"state"`
			DeltaText string `json:"deltaText"`
		}
		if json.Unmarshal(f.Payload, &ce) != nil {
			continue
		}
		if ce.State == "delta" {
			deltas = append(deltas, ce.DeltaText)
		}
	}
	return deltas
}

// TestStreamThenFinal verifies a run that streamed deltas emits chat delta events
// and per-delta agent assistant events, then a chat final WITHOUT re-sending the
// full assistant text as another agent assistant event.
func TestStreamThenFinal(t *testing.T) {
	srv, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true})
	srv.SetInbound(func(_, chatID, _, _, _ string, _ []InboundAttachment) {
		// Stream two partial deltas, then finalize with the full reply.
		srv.StreamDelta(chatID, "Hello ")
		srv.StreamDelta(chatID, "world.")
		srv.DeliverReply(chatID, "Hello world.")
	})

	em := newEmulator(t)
	conn, resp := em.open(t, wsURL, "")
	defer utils.CloseQuietly(conn)
	if !resp.OK {
		t.Fatalf("handshake failed: %+v", resp.Error)
	}

	em.writeReq(t, conn, "s1", "node.event", map[string]any{"event": "chat.subscribe", "payload": map[string]any{"sessionKey": "sess-1"}})
	em.writeReq(t, conn, "m1", "chat.send", map[string]any{"message": "hi", "sessionKey": "sess-1", "idempotencyKey": "run-1"})

	frames := readFramesUntilFinal(t, conn)

	// Two chat delta events with the raw fragments.
	deltas := chatDeltaTexts(frames)
	if len(deltas) != 2 || deltas[0] != "Hello " || deltas[1] != "world." {
		t.Fatalf("expected two chat deltas [Hello , world.], got %v", deltas)
	}

	// Agent assistant events come ONLY from the two streamed deltas, NOT a third
	// full-text event from emitChatReply. data.text carries the per-event increment
	// (voice clients accumulate across events), so it equals each raw delta.
	texts := agentAssistantTexts(frames)
	if len(texts) != 2 {
		t.Fatalf("expected exactly 2 agent assistant events (from deltas), got %d: %v", len(texts), texts)
	}
	if texts[0] != "Hello " || texts[1] != "world." {
		t.Fatalf("expected incremental agent texts [Hello , world.], got %v", texts)
	}

	// Payload seqs MUST strictly increment across the streamed deltas — reused
	// seqs make a client drop later events (the bug where voice only spoke the
	// first chunk). Two deltas → seq 1 then 2.
	seqs := agentAssistantSeqs(frames)
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Fatalf("expected agent assistant payload seqs [1 2], got %v", seqs)
	}

	// The chat final must still carry the full authoritative text.
	var sawFinalText bool
	for _, f := range frames {
		if f.Type == gatewayproto.FrameEvent && f.Event == "chat" {
			var ce struct {
				State   string `json:"state"`
				Message struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			if err := json.Unmarshal(f.Payload, &ce); err != nil {
				t.Fatalf("decode chat event: %v", err)
			}
			if ce.State == "final" && len(ce.Message.Content) > 0 && ce.Message.Content[0].Text == "Hello world." {
				sawFinalText = true
			}
		}
	}
	if !sawFinalText {
		t.Fatal("chat final did not carry the full reply text")
	}
}

// TestNonStreamedRunUnchanged verifies a run that did NOT stream keeps the
// original emitChatReply behavior: exactly one agent assistant full-text event
// plus the chat final.
func TestNonStreamedRunUnchanged(t *testing.T) {
	srv, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true})
	srv.SetInbound(func(_, chatID, content, _, _ string, _ []InboundAttachment) {
		srv.DeliverReply(chatID, "echo: "+content)
	})

	em := newEmulator(t)
	conn, resp := em.open(t, wsURL, "")
	defer utils.CloseQuietly(conn)
	if !resp.OK {
		t.Fatalf("handshake failed: %+v", resp.Error)
	}

	em.writeReq(t, conn, "s1", "node.event", map[string]any{"event": "chat.subscribe", "payload": map[string]any{"sessionKey": "sess-1"}})
	em.writeReq(t, conn, "m1", "chat.send", map[string]any{"message": "ping", "sessionKey": "sess-1", "idempotencyKey": "run-1"})

	frames := readFramesUntilFinal(t, conn)

	if deltas := chatDeltaTexts(frames); len(deltas) != 0 {
		t.Fatalf("non-streamed run should emit no chat deltas, got %v", deltas)
	}
	texts := agentAssistantTexts(frames)
	if len(texts) != 1 || texts[0] != "echo: ping" {
		t.Fatalf("expected one agent assistant full-text event [echo: ping], got %v", texts)
	}
}

// The agent selection carried by real client keys (the Android app sends
// agent:<id>:clawtotalk:<profile>; the R1 sends the bare "main" sentinel) is
// parsed by routing.AgentIDFromSessionKey. Kept here with device-shaped inputs
// because these are the keys this gateway actually receives.
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
		if got := routing.AgentIDFromSessionKey(in); got != want {
			t.Errorf("AgentIDFromSessionKey(%q) = %q, want %q", in, got, want)
		}
	}
}
