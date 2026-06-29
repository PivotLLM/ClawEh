package device

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/PivotLLM/ClawEh/pkg/gatewayproto"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

const (
	handshakeTimeout = 15 * time.Second
	signatureSkewMs  = 5 * 60 * 1000 // accept signedAt within +/- 5 minutes
	// Keepalive: ping the device periodically; if no pong/data arrives within the
	// read timeout, the read fails and the (possibly half-open) connection is closed
	// so the device reconnects instead of black-holing its sends.
	devicePingInterval = 30 * time.Second
	deviceReadTimeout  = 90 * time.Second
)

// ServerOptions configures a gateway protocol Server.
type ServerOptions struct {
	SharedToken   string   // gateway.auth.token; "" disables token auth (loopback dev only)
	ServerVersion string   // reported in hello-ok.server.version
	AutoApprove   bool     // dev/LAN: auto-approve fresh pending pairings
	AllowOrigins  []string // WS Origin allowlist; empty allows all
	LogMessages   bool     // log full inbound/outbound message content (logging.log_message_content)
}

// InboundFunc submits a device utterance into the agent layer. The channel sets
// this to bridge into ClawEh's message bus; it is called per chat.send.
type InboundFunc func(deviceID, chatID, content, idempotencyKey string)

// Server speaks the OpenClaw Gateway WebSocket protocol to external devices.
// It owns the handshake (challenge -> connect -> auth -> signature -> pairing ->
// hello-ok) and the conversation surface (chat.send / chat.subscribe / reply).
type Server struct {
	store    *Store
	opts     ServerOptions
	upgrader websocket.Upgrader
	methods  []string
	events   []string

	inbound InboundFunc
	querier AgentQuerier // optional: serves agents.list / chat.history to operator clients
	conns   sync.Map     // chatID -> *liveConn
}

// liveConn is a post-handshake device connection used for conversation routing.
type liveConn struct {
	cw         *connWriter
	deviceID   string
	chatID     string
	mu         sync.Mutex
	seq        uint64
	sessionKey string
	currentRun string
}

// NewServer builds a gateway protocol server backed by the pairing store.
func NewServer(store *Store, opts ServerOptions) *Server {
	allow := opts.AllowOrigins
	return &Server{
		store: store,
		opts:  opts,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				if len(allow) == 0 {
					return true
				}
				origin := r.Header.Get("Origin")
				for _, a := range allow {
					if a == "*" || a == origin {
						return true
					}
				}
				return false
			},
		},
		// Advertised surface — kept honest to what is implemented. Expanded by phase.
		methods: []string{"connect", "health", "chat.send", "node.event"},
		events:  []string{gatewayproto.EventConnectChallenge, "tick", "chat"},
	}
}

// SetInbound installs the agent bridge invoked on each chat.send.
func (s *Server) SetInbound(fn InboundFunc) { s.inbound = fn }

// SetQuerier installs the read-only agent/session accessor used to answer
// operator-client RPCs (agents.list, chat.history). When set, those methods are
// advertised in hello-ok and handled in dispatch.
func (s *Server) SetQuerier(q AgentQuerier) {
	s.querier = q
	s.methods = append(s.methods, "agents.list", "chat.history")
}

// connWriter serializes writes to one websocket connection.
type connWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *connWriter) writeJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteJSON(v)
}

func (w *connWriter) closeWith(code int, reason string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(2*time.Second))
	_ = w.conn.Close()
}

func (w *connWriter) ping() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
}

// HandleWS upgrades an HTTP request to the gateway protocol and runs the handshake.
func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.DebugCF("device", "websocket upgrade failed", map[string]any{"error": err.Error()})
		return
	}
	cw := &connWriter{conn: conn}

	connID := randomToken(16)
	nonce := randomToken(16)

	// Send the pre-auth challenge before any client frame.
	challenge := gatewayproto.NewEvent(gatewayproto.EventConnectChallenge,
		gatewayproto.ChallengePayload{Nonce: nonce, Ts: time.Now().UnixMilli()}, nil)
	if err := cw.writeJSON(challenge); err != nil {
		_ = conn.Close()
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return
	}
	if s.opts.LogMessages {
		logger.InfoCF("device", "raw in (connect)", map[string]any{"raw": string(raw)})
	}

	hello, fail := s.handshake(r, connID, nonce, raw)
	if fail != nil {
		logger.WarnCF("device", "connect rejected", map[string]any{
			"reason": fail.reason, "code": fail.err.Code, "message": fail.err.Message,
		})
		_ = cw.writeJSON(gatewayproto.NewErrorResponse(fail.id, fail.err))
		cw.closeWith(fail.code, fail.reason)
		return
	}
	logger.InfoCF("device", "connect accepted", map[string]any{
		"deviceId": hello.deviceID, "role": hello.role,
	})

	helloRes := gatewayproto.NewOKResponse(hello.id, hello.payload)
	if s.opts.LogMessages {
		if b, mErr := json.Marshal(helloRes); mErr == nil {
			logger.InfoCF("device", "raw out (hello-ok)", map[string]any{"raw": string(b)})
		}
	}
	if err := cw.writeJSON(helloRes); err != nil {
		_ = conn.Close()
		return
	}

	lc := &liveConn{cw: cw, deviceID: hello.deviceID, chatID: hello.chatID}
	s.conns.Store(lc.chatID, lc)
	defer func() {
		if cur, ok := s.conns.Load(lc.chatID); ok && cur == lc {
			s.conns.Delete(lc.chatID)
		}
	}()

	_ = conn.SetReadDeadline(time.Time{})
	s.serveLoop(r.Context(), lc)
}

// handshakeFail describes a rejected handshake: the error to send then the close frame.
type handshakeFail struct {
	id     string
	err    *gatewayproto.ErrorShape
	code   int
	reason string
}

// handshakeOK carries a successful hello-ok plus resolved identity for the loop.
type handshakeOK struct {
	id       string
	payload  gatewayproto.HelloOk
	deviceID string
	chatID   string
	role     string
	scopes   []string
}

// handshake validates the first frame and returns either a hello-ok or a failure.
func (s *Server) handshake(r *http.Request, connID, nonce string, raw []byte) (*handshakeOK, *handshakeFail) {
	var req gatewayproto.RequestFrame
	if err := json.Unmarshal(raw, &req); err != nil || req.Type != gatewayproto.FrameReq {
		return nil, &handshakeFail{id: "", err: gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "first frame must be a connect request", nil), code: websocket.ClosePolicyViolation, reason: "invalid handshake"}
	}
	if req.Method != "connect" {
		return nil, &handshakeFail{id: req.ID, err: gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "first request must be connect", nil), code: websocket.ClosePolicyViolation, reason: "invalid handshake"}
	}
	var p gatewayproto.ConnectParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, &handshakeFail{id: req.ID, err: gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "invalid connect params", nil), code: websocket.ClosePolicyViolation, reason: "invalid handshake"}
	}

	// Diagnostic: record exactly what a device advertises at connect (no secrets).
	// Critical for characterizing third-party clients like the Rabbit R1.
	logger.InfoCF("device", "connect attempt", map[string]any{
		"clientId": p.Client.ID, "mode": p.Client.Mode, "role": p.Role,
		"platform": p.Client.Platform, "deviceFamily": p.Client.DeviceFamily,
		"version": p.Client.Version, "displayName": p.Client.DisplayName,
		"caps": p.Caps, "commands": p.Commands,
		"minProtocol": p.MinProtocol, "maxProtocol": p.MaxProtocol,
		"hasDevice": p.Device != nil, "hasToken": p.Auth != nil && p.Auth.Token != "",
		"remoteIp": clientIP(r),
	})

	// Protocol negotiation — accept the highest version both sides support.
	negotiatedProtocol := gatewayproto.NegotiateProtocol(p.MinProtocol, p.MaxProtocol, p.Client.Mode == "probe")
	if negotiatedProtocol == 0 {
		detail := map[string]any{"code": gatewayproto.DetailProtocolMismatch, "expectedProtocol": gatewayproto.ProtocolVersion}
		return nil, &handshakeFail{id: req.ID, err: gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "protocol mismatch", detail), code: websocket.CloseProtocolError, reason: "protocol mismatch"}
	}

	// Gateway authentication: accept the shared token OR a valid issued device
	// token. A paired device (e.g. the Rabbit R1) reconnects presenting its device
	// token rather than the shared secret, so shared-token-only auth would wrongly
	// reject it. When no shared token is configured (loopback dev), auth is open.
	if s.opts.SharedToken != "" && !s.authorizeGateway(r.Context(), &p) {
		if s.opts.LogMessages {
			logger.WarnCF("device", "gateway auth failed", map[string]any{
				"clientId":           p.Client.ID,
				"tokenPresent":       p.Auth != nil && p.Auth.Token != "",
				"deviceTokenPresent": p.Auth != nil && p.Auth.DeviceToken != "",
			})
		}
		detail := map[string]any{"code": gatewayproto.DetailAuthTokenMismatch}
		return nil, &handshakeFail{id: req.ID, err: gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "gateway authentication failed", detail), code: websocket.ClosePolicyViolation, reason: "unauthorized"}
	}

	// Device identity is required for a device/node client.
	if p.Device == nil {
		detail := map[string]any{"code": gatewayproto.DetailDeviceIdentityNeeded}
		return nil, &handshakeFail{id: req.ID, err: gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "device identity required", detail), code: websocket.ClosePolicyViolation, reason: "device identity required"}
	}
	if devFail := s.verifyDeviceIdentity(req.ID, nonce, &p); devFail != nil {
		return nil, devFail
	}

	role := p.Role
	if role == "" {
		role = gatewayproto.RoleNode
	}
	ctx := r.Context()

	// Pairing decision.
	paired, ok, err := s.store.GetPaired(ctx, p.Device.ID)
	if err != nil {
		return nil, &handshakeFail{id: req.ID, err: gatewayproto.NewError(gatewayproto.CodeUnavailable, "pairing store error", nil), code: websocket.CloseInternalServerErr, reason: "store error"}
	}
	pairedAndMatches := ok && paired.PublicKey == p.Device.PublicKey

	if !pairedAndMatches {
		// Create a pending pairing request. Auto-approve only when explicitly enabled.
		reqID, perr := s.store.CreatePending(ctx, PendingPairing{
			DeviceID: p.Device.ID, PublicKey: p.Device.PublicKey, DisplayName: p.Client.DisplayName,
			Platform: p.Client.Platform, DeviceFamily: p.Client.DeviceFamily, ClientID: p.Client.ID,
			ClientMode: p.Client.Mode, Role: role, Scopes: p.Scopes, RemoteIP: clientIP(r),
		})
		if perr != nil {
			return nil, &handshakeFail{id: req.ID, err: gatewayproto.NewError(gatewayproto.CodeUnavailable, "pairing store error", nil), code: websocket.CloseInternalServerErr, reason: "store error"}
		}
		if s.opts.AutoApprove {
			if dev, _, aerr := s.store.Approve(ctx, reqID, []string{role}, p.Scopes); aerr == nil {
				paired = dev
				pairedAndMatches = true
			}
		}
		if !pairedAndMatches {
			detail := map[string]any{"code": gatewayproto.DetailPairingRequired, "reason": "not-paired", "requestId": reqID}
			return nil, &handshakeFail{id: req.ID, err: gatewayproto.NewError(gatewayproto.CodeNotPaired, "device pairing required", detail), code: websocket.ClosePolicyViolation, reason: "pairing required"}
		}
	}

	_ = s.store.UpdateLastSeen(ctx, paired.DeviceID, time.Now().UnixMilli())

	hello := s.buildHelloOk(ctx, connID, paired, negotiatedProtocol)
	return &handshakeOK{id: req.ID, payload: hello, deviceID: paired.DeviceID, chatID: "device:" + paired.DeviceID, role: role, scopes: paired.Scopes}, nil
}

// verifyDeviceIdentity checks the nonce echo, key/id consistency, signed-at skew, and
// the Ed25519 signature over the canonical v3/v2 payload.
func (s *Server) verifyDeviceIdentity(reqID, nonce string, p *gatewayproto.ConnectParams) *handshakeFail {
	fail := func(msg string) *handshakeFail {
		detail := map[string]any{"code": gatewayproto.DetailDeviceAuthFailed}
		return &handshakeFail{id: reqID, err: gatewayproto.NewError(gatewayproto.CodeInvalidRequest, msg, detail), code: websocket.ClosePolicyViolation, reason: "device auth failed"}
	}
	d := p.Device
	if d.Nonce != nonce {
		return fail("device nonce mismatch")
	}
	now := time.Now().UnixMilli()
	if d.SignedAt < now-signatureSkewMs || d.SignedAt > now+signatureSkewMs {
		return fail("device signature timestamp out of range")
	}
	pub, ok := gatewayproto.DecodeKeyOrSig(d.PublicKey)
	if !ok {
		return fail("invalid device public key")
	}
	if gatewayproto.DeviceIDFromPublicKey(pub) != d.ID {
		return fail("device id does not match public key")
	}
	role := p.Role
	if role == "" {
		role = gatewayproto.RoleNode
	}
	token := ""
	if p.Auth != nil {
		switch {
		case p.Auth.Token != "":
			token = p.Auth.Token
		case p.Auth.DeviceToken != "":
			token = p.Auth.DeviceToken
		case p.Auth.BootstrapToken != "":
			token = p.Auth.BootstrapToken
		}
	}
	in := gatewayproto.DeviceAuthInput{
		DeviceID: d.ID, ClientID: p.Client.ID, ClientMode: p.Client.Mode, Role: role,
		Scopes: p.Scopes, SignedAtMs: d.SignedAt, Token: token, Nonce: d.Nonce,
		Platform: p.Client.Platform, DeviceFamily: p.Client.DeviceFamily,
	}
	if gatewayproto.VerifyConnectSignature(in, d.PublicKey, d.Signature) == "" {
		return fail("device signature verification failed")
	}
	return nil
}

// authorizeGateway reports whether the connection satisfies gateway auth: either
// the shared token, or a non-revoked device token we issued (presented in either
// the token or deviceToken field). Called only when a shared token is configured.
func (s *Server) authorizeGateway(ctx context.Context, p *gatewayproto.ConnectParams) bool {
	if p.Auth == nil {
		return false
	}
	if p.Auth.Token != "" && subtle.ConstantTimeCompare([]byte(p.Auth.Token), []byte(s.opts.SharedToken)) == 1 {
		return true
	}
	for _, t := range []string{p.Auth.DeviceToken, p.Auth.Token} {
		if t == "" {
			continue
		}
		if _, ok, err := s.store.TokenByValue(ctx, t); err == nil && ok {
			return true
		}
	}
	return false
}

// buildHelloOk assembles the hello-ok payload for a paired device, echoing the
// negotiated protocol version (which may be lower than ProtocolVersion).
func (s *Server) buildHelloOk(ctx context.Context, connID string, paired *PairedDevice, protocol int) gatewayproto.HelloOk {
	role := gatewayproto.RoleNode
	if len(paired.Roles) > 0 {
		role = paired.Roles[0]
		for _, rl := range paired.Roles {
			if rl == gatewayproto.RoleNode {
				role = gatewayproto.RoleNode
				break
			}
		}
	}
	auth := gatewayproto.HelloAuth{Role: role, Scopes: paired.Scopes, IssuedAtMs: time.Now().UnixMilli()}
	if toks, err := s.store.ListTokens(ctx, paired.DeviceID); err == nil {
		for _, t := range toks {
			auth.DeviceTokens = append(auth.DeviceTokens, gatewayproto.HelloDeviceToken{
				DeviceToken: t.Token, Role: t.Role, Scopes: t.Scopes, IssuedAtMs: t.CreatedAtMs,
			})
			if t.Role == role && auth.DeviceToken == "" {
				auth.DeviceToken = t.Token
			}
		}
	}
	return gatewayproto.HelloOk{
		Type:     "hello-ok",
		Protocol: protocol,
		Server:   gatewayproto.HelloServer{Version: s.opts.ServerVersion, ConnID: connID},
		Features: gatewayproto.HelloFeatures{Methods: s.methods, Events: s.events},
		Auth:     auth,
		Policy: gatewayproto.HelloPolicy{
			MaxPayload:       gatewayproto.MaxPayloadBytes,
			MaxBufferedBytes: gatewayproto.MaxBufferedBytes,
			TickIntervalMs:   gatewayproto.TickIntervalMs,
		},
	}
}

// serveLoop handles post-handshake frames: keepalive tick plus the conversation
// RPC surface (health, chat.send, node.event/chat.subscribe).
func (s *Server) serveLoop(ctx context.Context, lc *liveConn) {
	conn := lc.cw.conn
	_ = conn.SetReadDeadline(time.Now().Add(deviceReadTimeout))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(deviceReadTimeout))
		return nil
	})

	tick := time.NewTicker(gatewayproto.TickIntervalMs * time.Millisecond)
	defer tick.Stop()
	ping := time.NewTicker(devicePingInterval)
	defer ping.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(deviceReadTimeout))
			if s.opts.LogMessages {
				logger.InfoCF("device", "raw in", map[string]any{"deviceId": lc.deviceID, "raw": string(raw)})
			}
			var req gatewayproto.RequestFrame
			if json.Unmarshal(raw, &req) != nil || req.Type != gatewayproto.FrameReq {
				continue
			}
			s.dispatch(lc, req)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			lc.cw.closeWith(websocket.CloseGoingAway, "server shutdown")
			return
		case <-done:
			return
		case <-tick.C:
			if err := lc.cw.writeJSON(gatewayproto.NewEvent("tick", map[string]any{"ts": time.Now().UnixMilli()}, nil)); err != nil {
				return
			}
		case <-ping.C:
			if err := lc.cw.ping(); err != nil {
				return
			}
		}
	}
}

// dispatch routes a post-handshake request frame.
func (s *Server) dispatch(lc *liveConn, req gatewayproto.RequestFrame) {
	// Diagnostic: capture which methods a device actually calls post-handshake
	// (reveals the R1's Talk model — chat.send text loop vs. audio streaming).
	logger.InfoCF("device", "node request", map[string]any{"method": req.Method, "deviceId": lc.deviceID})
	switch req.Method {
	case "health":
		_ = lc.cw.writeJSON(gatewayproto.NewOKResponse(req.ID, map[string]any{"status": "ok"}))
	case "chat.send":
		s.handleChatSend(lc, req)
	case "node.event":
		s.handleNodeEvent(lc, req)
	case "agents.list":
		s.handleAgentsList(lc, req)
	case "chat.history":
		s.handleChatHistory(lc, req)
	default:
		_ = lc.cw.writeJSON(gatewayproto.NewErrorResponse(req.ID,
			gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "method not supported: "+req.Method, nil)))
	}
}

// handleChatSend acks the turn and bridges the transcript into the agent layer.
// The assistant reply arrives later via DeliverReply (ClawEh has no token streaming).
func (s *Server) handleChatSend(lc *liveConn, req gatewayproto.RequestFrame) {
	var p struct {
		Message        string `json:"message"`
		SessionKey     string `json:"sessionKey"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if json.Unmarshal(req.Params, &p) != nil {
		_ = lc.cw.writeJSON(gatewayproto.NewErrorResponse(req.ID,
			gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "invalid chat.send params", nil)))
		return
	}
	runID := p.IdempotencyKey
	if runID == "" {
		runID = randomToken(8)
	}
	lc.mu.Lock()
	if p.SessionKey != "" {
		lc.sessionKey = p.SessionKey
	}
	lc.currentRun = runID
	lc.mu.Unlock()

	if s.opts.LogMessages {
		logger.InfoCF("device", "chat.send in", map[string]any{
			"deviceId": lc.deviceID, "runId": runID, "sessionKey": p.SessionKey, "message": p.Message,
		})
	}

	// Acknowledge transport receipt immediately with a non-terminal status, exactly
	// as OpenClaw does (server-methods/chat.ts ackPayload: {runId, status:"started"}).
	// The assistant reply is delivered asynchronously via "chat" delta/final events
	// from DeliverReply; the res must NOT carry the result or block on the run, or a
	// strict client's transport times out waiting for this frame.
	s.writeRes(lc, gatewayproto.NewOKResponse(req.ID, map[string]any{"runId": runID, "status": "started"}), "chat.send ack")
	if s.inbound != nil {
		go s.inbound(lc.deviceID, lc.chatID, p.Message, runID)
	}
}

// handleNodeEvent handles the node ingress envelope (chat.subscribe / chat.unsubscribe).
func (s *Server) handleNodeEvent(lc *liveConn, req gatewayproto.RequestFrame) {
	var p struct {
		Event   string `json:"event"`
		Payload struct {
			SessionKey string `json:"sessionKey"`
		} `json:"payload"`
	}
	if json.Unmarshal(req.Params, &p) != nil {
		_ = lc.cw.writeJSON(gatewayproto.NewErrorResponse(req.ID,
			gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "invalid node.event params", nil)))
		return
	}
	if p.Event == "chat.subscribe" && p.Payload.SessionKey != "" {
		lc.mu.Lock()
		lc.sessionKey = p.Payload.SessionKey
		lc.mu.Unlock()
	}
	_ = lc.cw.writeJSON(gatewayproto.NewOKResponse(req.ID, map[string]any{"ok": true}))
}

// handleAgentsList answers an operator client's agents.list with the configured
// agents and the default agent's main session key (which the client echoes back
// as the chat.history/chat.send sessionKey).
func (s *Server) handleAgentsList(lc *liveConn, req gatewayproto.RequestFrame) {
	if s.querier == nil {
		_ = lc.cw.writeJSON(gatewayproto.NewErrorResponse(req.ID,
			gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "method not supported: agents.list", nil)))
		return
	}
	agents, defaultID, mainKey := s.querier.Agents()
	list := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		entry := map[string]any{"id": a.ID}
		if a.Name != "" {
			entry["name"] = a.Name
		}
		list = append(list, entry)
	}
	// Default the connection's session to the agent's main key so a subsequent
	// chat.send without an explicit sessionKey lands in the same conversation the
	// client just read history for.
	lc.mu.Lock()
	if lc.sessionKey == "" {
		lc.sessionKey = mainKey
	}
	lc.mu.Unlock()
	s.writeRes(lc, gatewayproto.NewOKResponse(req.ID, map[string]any{
		"defaultId": defaultID,
		"mainKey":   mainKey,
		"scope":     "global",
		"agents":    list,
	}), "agents.list res")
}

// writeRes writes a response frame, logging the exact bytes when content logging
// is enabled (debugging third-party client compatibility).
func (s *Server) writeRes(lc *liveConn, res gatewayproto.ResponseFrame, label string) {
	if s.opts.LogMessages {
		if b, err := json.Marshal(res); err == nil {
			logger.InfoCF("device", "raw out ("+label+")", map[string]any{"deviceId": lc.deviceID, "raw": string(b)})
		}
	}
	_ = lc.cw.writeJSON(res)
}

// handleChatHistory returns the stored transcript for the requested session key,
// shaped like the live "chat" event message so the client renders past turns the
// same way it renders incoming replies.
func (s *Server) handleChatHistory(lc *liveConn, req gatewayproto.RequestFrame) {
	if s.querier == nil {
		_ = lc.cw.writeJSON(gatewayproto.NewErrorResponse(req.ID,
			gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "method not supported: chat.history", nil)))
		return
	}
	var p struct {
		SessionKey string `json:"sessionKey"`
	}
	if json.Unmarshal(req.Params, &p) != nil {
		_ = lc.cw.writeJSON(gatewayproto.NewErrorResponse(req.ID,
			gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "invalid chat.history params", nil)))
		return
	}
	sessionKey := p.SessionKey
	if sessionKey == "" {
		lc.mu.Lock()
		sessionKey = lc.sessionKey
		lc.mu.Unlock()
	}
	history := s.querier.History(sessionKey)
	messages := make([]map[string]any, 0, len(history))
	for _, m := range history {
		messages = append(messages, map[string]any{
			"role":    m.Role,
			"content": []map[string]any{{"type": "text", "text": m.Content}},
		})
	}
	s.writeRes(lc, gatewayproto.NewOKResponse(req.ID, map[string]any{
		"sessionKey": sessionKey,
		"messages":   messages,
	}), "chat.history res")
}

// DeliverReply emits a terminal "chat" final event to the device for the given
// chatID, carrying the in-flight runId. Returns false if no connection matches.
func (s *Server) DeliverReply(chatID, content string) bool {
	v, ok := s.conns.Load(chatID)
	if !ok {
		return false
	}
	lc := v.(*liveConn)
	lc.mu.Lock()
	lc.seq++
	deltaSeq := lc.seq
	lc.seq++
	finalSeq := lc.seq
	runID := lc.currentRun
	sessionKey := lc.sessionKey
	lc.currentRun = ""
	lc.mu.Unlock()

	if s.opts.LogMessages {
		logger.InfoCF("device", "chat reply out", map[string]any{
			"deviceId": lc.deviceID, "runId": runID, "sessionKey": sessionKey, "content": content,
		})
	}

	sendEvent := func(p map[string]any, seq uint64, label string) bool {
		ev := gatewayproto.NewEvent("chat", p, &seq)
		if s.opts.LogMessages {
			if b, err := json.Marshal(ev); err == nil {
				logger.InfoCF("device", "raw out ("+label+")", map[string]any{"deviceId": lc.deviceID, "raw": string(b)})
			}
		}
		return lc.cw.writeJSON(ev) == nil
	}

	// OpenClaw streams chat events as one or more state:"delta" chunks (carrying
	// deltaText) and then a state:"final". Clients that show a thinking indicator
	// until the first token leave that state on the delta, so a lone final never
	// clears it. We emit the whole reply as a single delta, then the final.
	sendEvent(map[string]any{
		"runId":      runID,
		"sessionKey": sessionKey,
		"state":      "delta",
		"seq":        deltaSeq,
		"deltaText":  content,
	}, deltaSeq, "chat delta")

	// Match OpenClaw's broadcastChatFinal message shape exactly: a top-level "text"
	// alongside the content blocks (simple clients read message.text), plus the
	// terminal stopReason/usage fields. See server-methods/chat.ts broadcastChatFinal.
	message := map[string]any{
		"role":       "assistant",
		"content":    []map[string]any{{"type": "text", "text": content}},
		"text":       content,
		"timestamp":  time.Now().UnixMilli(),
		"stopReason": "stop",
		"usage":      map[string]any{"input": 0, "output": 0, "totalTokens": 0},
	}
	ok = sendEvent(map[string]any{
		"runId":      runID,
		"sessionKey": sessionKey,
		"state":      "final",
		"seq":        finalSeq,
		"message":    message,
	}, finalSeq, "chat final")
	return ok
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.RemoteAddr
}
