package device

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
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
	// sessionKeyAgentPrefix marks an agent-scoped session key the agent loop honors
	// verbatim (mirrors pkg/agent's prefix).
	sessionKeyAgentPrefix = "agent:"
)

// ServerOptions configures a gateway protocol Server.
type ServerOptions struct {
	SharedToken   string   // gateway.auth.token; "" disables token auth (loopback dev only)
	WordToken     string   // human-typeable passphrase accepted alongside SharedToken
	ServerVersion string   // reported in hello-ok.server.version
	AutoApprove   bool     // dev/LAN: auto-approve fresh pending pairings
	AllowOrigins  []string // WS Origin allowlist; empty allows all
	LogMessages   bool     // log full inbound/outbound message content (logging.log_message_content)
}

// InboundFunc submits a device utterance into the agent layer. The channel sets
// this to bridge into ClawEh's message bus; it is called per chat.send.
type InboundFunc func(deviceID, chatID, content, idempotencyKey, agentID string)

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
			// Never negotiate permessage-deflate: keep frames plain text so third-party
			// clients/proxies that handle compression poorly aren't affected. (Gorilla
			// defaults this off; set explicitly to make the contract clear.)
			EnableCompression: false,
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
		events:  []string{gatewayproto.EventConnectChallenge, "tick", "chat", "agent"},
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
	// Belt-and-suspenders with EnableCompression:false — never compress writes.
	conn.EnableWriteCompression(false)
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

	if err := cw.writeJSON(gatewayproto.NewOKResponse(hello.id, hello.payload)); err != nil {
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

	logger.DebugCF("device", "connect attempt", map[string]any{
		"clientId": p.Client.ID, "mode": p.Client.Mode, "role": p.Role,
		"platform": p.Client.Platform, "version": p.Client.Version,
		"remoteIp": clientIP(r),
	})

	// Protocol negotiation — accept the highest version both sides support.
	negotiatedProtocol := gatewayproto.NegotiateProtocol(p.MinProtocol, p.MaxProtocol, p.Client.Mode == "probe")
	if negotiatedProtocol == 0 {
		detail := map[string]any{"code": gatewayproto.DetailProtocolMismatch, "expectedProtocol": gatewayproto.ProtocolVersion}
		return nil, &handshakeFail{id: req.ID, err: gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "protocol mismatch", detail), code: websocket.CloseProtocolError, reason: "protocol mismatch"}
	}

	// Gateway authentication: accept a configured shared secret (long QR token OR
	// word passphrase) OR a valid issued device token. A paired device (e.g. the
	// Rabbit R1) reconnects presenting its device token rather than the shared
	// secret, so shared-token-only auth would wrongly reject it. When no shared
	// secret is configured (loopback dev), auth is open.
	if (s.opts.SharedToken != "" || s.opts.WordToken != "") && !s.authorizeGateway(r.Context(), &p) {
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

// authorizeGateway reports whether the connection satisfies gateway auth: either a
// configured shared secret (the long QR token OR the typeable word passphrase), or a
// non-revoked device token we issued (presented in either the token or deviceToken
// field). Called only when token auth is configured.
func (s *Server) authorizeGateway(ctx context.Context, p *gatewayproto.ConnectParams) bool {
	if p.Auth == nil {
		return false
	}
	if p.Auth.Token != "" {
		for _, secret := range []string{s.opts.SharedToken, s.opts.WordToken} {
			if secret != "" && subtle.ConstantTimeCompare([]byte(p.Auth.Token), []byte(secret)) == 1 {
				return true
			}
		}
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
	// runId is the client's idempotencyKey, matching OpenClaw (clientRunId =
	// p.idempotencyKey): chat events carry this so the client correlates the reply
	// to the turn it sent.
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
	// The assistant reply is delivered asynchronously via "agent"/"chat" events from
	// DeliverReply; the res must NOT carry the result or block on the run, or a strict
	// client's transport times out waiting for this frame.
	_ = lc.cw.writeJSON(gatewayproto.NewOKResponse(req.ID, map[string]any{"runId": runID, "status": "started"}))

	// Device text commands (e.g. "/agent bob") are handled here and answered as a
	// reply event, without running the agent loop.
	if cmd, arg, ok := parseSlashCommand(p.Message); ok {
		switch cmd {
		case "agent":
			go s.handleAgentCommand(lc, runID, arg)
			return
		case "help":
			go s.emitChatReply(lc, runID, s.sessionScopeKey(lc), deviceHelpText)
			return
		}
	}

	if s.inbound != nil {
		go s.inbound(lc.deviceID, lc.chatID, p.Message, runID, s.sessionScopeKey(lc))
	}
}

// deviceHelpText lists the slash commands a device user can type.
const deviceHelpText = "Commands:\n" +
	"• /agent — list assistants and show the current one\n" +
	"• /agent <name> — switch to an assistant\n" +
	"• /agent default — reset to the default assistant\n" +
	"• /help — show this message\n" +
	"\nAnything else is sent to the current assistant."

// parseSlashCommand splits a leading "/cmd arg..." message. ok is false when the
// message is not a slash command; cmd is lowercased and stripped of the slash.
func parseSlashCommand(message string) (cmd, arg string, ok bool) {
	m := strings.TrimSpace(message)
	if !strings.HasPrefix(m, "/") {
		return "", "", false
	}
	m = m[1:]
	if i := strings.IndexAny(m, " \t"); i >= 0 {
		return strings.ToLower(m[:i]), strings.TrimSpace(m[i+1:]), true
	}
	return strings.ToLower(m), "", true
}

// handleAgentCommand answers the "/agent" device command. With no argument it
// lists the configured assistants (marking the current one); with a name or id it
// switches this device's assigned assistant via the pairing store so subsequent
// turns route there (sessionScopeKey reads the assignment). "default"/"reset"
// clears the assignment back to the gateway default. The device is a dedicated
// channel, so it may target any configured agent.
func (s *Server) handleAgentCommand(lc *liveConn, runID, arg string) {
	ctx := context.Background()

	var agents []DeviceAgentInfo
	defaultID := ""
	if s.querier != nil {
		agents, defaultID, _ = s.querier.Agents()
	}
	current := defaultID
	if dev, ok, err := s.store.GetPaired(ctx, lc.deviceID); err == nil && ok && dev.AgentID != "" {
		current = dev.AgentID
	}

	if len(agents) == 0 {
		s.emitChatReply(lc, runID, s.sessionScopeKey(lc), "No assistants are configured.")
		return
	}
	if arg == "" || strings.EqualFold(arg, "list") {
		s.emitChatReply(lc, runID, s.sessionScopeKey(lc), formatAgentList(agents, current))
		return
	}
	if strings.EqualFold(arg, "default") || strings.EqualFold(arg, "reset") {
		if err := s.store.SetDeviceAgent(ctx, lc.deviceID, ""); err != nil {
			s.emitChatReply(lc, runID, s.sessionScopeKey(lc), "Couldn't reset assistant: "+err.Error())
			return
		}
		s.emitChatReply(lc, runID, s.sessionScopeKey(lc), "Switched to the default assistant.")
		return
	}

	targetID, targetName := resolveDeviceAgent(agents, arg)
	if targetID == "" {
		s.emitChatReply(lc, runID, s.sessionScopeKey(lc),
			"No assistant matches \""+arg+"\".\n\n"+formatAgentList(agents, current))
		return
	}
	if err := s.store.SetDeviceAgent(ctx, lc.deviceID, targetID); err != nil {
		s.emitChatReply(lc, runID, s.sessionScopeKey(lc), "Couldn't switch assistant: "+err.Error())
		return
	}
	// Recompute the scope so the confirmation is tagged with the new assistant.
	s.emitChatReply(lc, runID, s.sessionScopeKey(lc),
		"Switched to "+targetName+". New messages will go to this assistant.")
}

// resolveDeviceAgent matches an argument against agent ids and names
// (case-insensitive) and returns the canonical id + display name, or "" if none.
func resolveDeviceAgent(agents []DeviceAgentInfo, arg string) (id, name string) {
	for _, a := range agents {
		if strings.EqualFold(a.ID, arg) || strings.EqualFold(a.Name, arg) {
			return a.ID, agentDisplayName(a)
		}
	}
	return "", ""
}

// formatAgentList renders the assistant picker text sent back to the device.
func formatAgentList(agents []DeviceAgentInfo, currentID string) string {
	var b strings.Builder
	b.WriteString("Assistants:\n")
	for _, a := range agents {
		b.WriteString("• ")
		b.WriteString(agentDisplayName(a))
		if a.ID == currentID {
			b.WriteString(" (current)")
		}
		b.WriteString("\n")
	}
	b.WriteString("\nSend \"/agent <name>\" to switch, or \"/agent default\" to reset.")
	return b.String()
}

func agentDisplayName(a DeviceAgentInfo) string {
	if a.Name != "" {
		return a.Name
	}
	return a.ID
}

// sessionScopeKey resolves the conversation session a turn runs in. Operator clients
// send an agent-scoped key (agent:<id>:<peer>:<profile>) — they pick their own agent,
// so it is honored verbatim (each profile isolated; chat.history reads the same key).
// Node clients (e.g. the R1) have no picker and send "main"; route them to their
// per-device assigned agent (set on the WebUI Devices page) or the gateway default,
// isolated per device so two devices don't share one conversation. The agent is
// selected via preresolved_agent_id (the key's 2nd segment).
func (s *Server) sessionScopeKey(lc *liveConn) string {
	lc.mu.Lock()
	key := lc.sessionKey
	lc.mu.Unlock()
	if strings.HasPrefix(key, sessionKeyAgentPrefix) {
		return key
	}
	agent := "main"
	if s.querier != nil {
		if d := s.querier.DefaultAgentID(); d != "" {
			agent = d
		}
	}
	// Per-device assignment overrides the default for node clients.
	if dev, ok, err := s.store.GetPaired(context.Background(), lc.deviceID); err == nil && ok && dev.AgentID != "" {
		agent = dev.AgentID
	}
	return sessionKeyAgentPrefix + agent + ":device:" + lc.deviceID
}

// agentIDFromSessionKey extracts the selected agent id from an agent-scoped session
// key of the form "agent:<id>:<peer>:<profile>". Returns "" for the no-selection
// sentinel "main" or any key that isn't agent-scoped, so the agent loop uses default
// routing.
func agentIDFromSessionKey(sessionKey string) string {
	parts := strings.Split(sessionKey, ":")
	if len(parts) < 2 || parts[0] != "agent" {
		return ""
	}
	id := strings.TrimSpace(parts[1])
	if id == "" || id == "main" {
		return ""
	}
	return id
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
	_ = lc.cw.writeJSON(gatewayproto.NewOKResponse(req.ID, map[string]any{
		"defaultId": defaultID,
		"mainKey":   mainKey,
		"scope":     "global",
		"agents":    list,
	}))
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
	_ = lc.cw.writeJSON(gatewayproto.NewOKResponse(req.ID, map[string]any{
		"sessionKey": sessionKey,
		"messages":   messages,
	}))
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
	runID := lc.currentRun
	sessionKey := lc.sessionKey
	lc.currentRun = ""
	lc.mu.Unlock()
	return s.emitChatReply(lc, runID, sessionKey, content)
}

// emitChatReply delivers a complete assistant reply to one connection. It emits
// both event families a real OpenClaw gateway produces for a turn:
//   - "agent" events: stream:"assistant" carrying the text, then stream:"lifecycle"
//     phase:"end" marking completion. Operator clients (e.g. the clawtotalk app)
//     accumulate data.text and complete the turn on lifecycle/end.
//   - "chat" final event: the rendered transcript message, used by node clients
//     (the Rabbit R1) and OpenClaw-UI-style clients.
//
// Frame-level seq is monotonic per connection; agent/chat payload seq is per-run.
func (s *Server) emitChatReply(lc *liveConn, runID, sessionKey, content string) bool {
	lc.mu.Lock()
	lc.seq++
	agentTextFrameSeq := lc.seq
	lc.seq++
	agentEndFrameSeq := lc.seq
	lc.seq++
	chatFrameSeq := lc.seq
	lc.mu.Unlock()

	if s.opts.LogMessages {
		logger.InfoCF("device", "chat reply out", map[string]any{
			"deviceId": lc.deviceID, "runId": runID, "sessionKey": sessionKey, "content": content,
		})
	}

	sendEvent := func(eventName string, p map[string]any, seq uint64) bool {
		return lc.cw.writeJSON(gatewayproto.NewEvent(eventName, p, &seq)) == nil
	}

	ts := time.Now().UnixMilli()

	// Emit the "agent" stream first (assistant text, then lifecycle end), then the
	// "chat" final. A real gateway streams the assistant events during the run and
	// finalizes after; sending "chat" final first can make a node client (the R1)
	// mark the turn complete and skip feeding the agent text to its speech pipeline.

	// "agent" assistant stream — operator/voice clients read data.text/delta. Matches
	// OpenClaw emitAcpAssistantDelta: {runId, seq, stream:"assistant", ts, data:{text, delta}}.
	sendEvent("agent", map[string]any{
		"runId":  runID,
		"seq":    1,
		"stream": "assistant",
		"ts":     ts,
		"data":   map[string]any{"text": content, "delta": content},
	}, agentTextFrameSeq)

	// "agent" lifecycle end — marks the run complete so voice/operator clients
	// resolve the turn. Matches OpenClaw emitAcpLifecycleEnd: stream:"lifecycle" data.phase:"end".
	sendEvent("agent", map[string]any{
		"runId":  runID,
		"seq":    2,
		"stream": "lifecycle",
		"ts":     ts,
		"data":   map[string]any{"phase": "end"},
	}, agentEndFrameSeq)

	// "chat" final — transcript message for node/UI clients. usage/stopReason are
	// payload-level per ChatFinalEventSchema; message carries role+content+flat text.
	return sendEvent("chat", map[string]any{
		"runId":      runID,
		"sessionKey": sessionKey,
		"state":      "final",
		"seq":        1,
		"message": map[string]any{
			"role":      "assistant",
			"content":   []map[string]any{{"type": "text", "text": content}},
			"text":      content,
			"timestamp": ts,
		},
		"usage":      map[string]any{"input": 0, "output": 0, "totalTokens": 0},
		"stopReason": "stop",
	}, chatFrameSeq)
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
