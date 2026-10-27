package device

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/gatewayproto"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/utils"
)

const (
	handshakeTimeout = 15 * time.Second
	signatureSkewMs  = 5 * 60 * 1000 // accept signedAt within +/- 5 minutes
	// Keepalive: ping the device periodically; if no pong/data arrives within the
	// read timeout, the read fails and the (possibly half-open) connection is closed
	// so the device reconnects instead of black-holing its sends.
	devicePingInterval = 30 * time.Second
	deviceReadTimeout  = 90 * time.Second
	// deviceWriteTimeout bounds every write to a device socket. A write that
	// misses it closes that connection, so a device that stops reading is
	// dropped instead of stalling whoever writes to it.
	deviceWriteTimeout = 5 * time.Second
	// deviceSendQueueSize is how many frames may wait for a connection's
	// writer. Senders never block: a connection whose queue is full is closed.
	deviceSendQueueSize = 256
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

// InboundAttachment is a decoded binary attachment (e.g. a photo) sent with a
// chat.send. The channel saves it to the media store and forwards a ref so the
// agent loop (and its vision-describe path) can consume it.
type InboundAttachment struct {
	MimeType string
	Name     string
	Data     []byte
}

// InboundFunc submits a device utterance into the agent layer. The channel sets
// this to bridge into ClawEh's message bus; it is called per chat.send.
type InboundFunc func(deviceID, chatID, content, idempotencyKey, agentID string, attachments []InboundAttachment)

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
	// staleWarned holds "deviceID|agentID" for each stale assignment already
	// logged, so a device that keeps talking logs it once.
	staleWarned sync.Map
	conns       sync.Map // chatID -> *liveConn (the newest connection per device)

	// live is every post-handshake connection, so a stop or a device removal
	// can close all of them, not only the newest per device. stopped refuses
	// connections that finish their handshake after CloseAll.
	liveMu  sync.Mutex
	live    map[*liveConn]struct{}
	stopped bool

	throttle *authThrottle
	preauth  atomic.Int32        // connections upgraded but not yet authenticated
	alert    func(alerter.Alert) // optional: the channel's operator alerter
}

// liveConn is a post-handshake device connection used for conversation routing.
type liveConn struct {
	cw       *connWriter
	deviceID string
	chatID   string
	// tokens are the device tokens this connection holds (echoed or issued in
	// hello-ok). Every inbound request re-checks that the device is still
	// paired and one of them is still valid.
	tokens     []string
	mu         sync.Mutex
	seq        uint64
	sessionKey string
	currentRun string
	// Partial-text streaming state (guarded by mu). streamedRun names the run for
	// which deltas have been emitted; streamedText accumulates the assistant text
	// sent so far so each delta event can carry the full running content. Both are
	// reset when a new chat.send starts a run. emitChatReply consults streamedRun
	// to avoid re-sending the assistant text a streamed run already delivered.
	streamedRun  string
	streamedText strings.Builder
	// Per-run payload seq counters. The chat and agent event streams are numbered
	// independently within a run; clients key events by (runId, seq), so these MUST
	// increment across streamed deltas — reusing a seq makes the client drop the
	// later events as duplicates. Reset when a new chat.send starts a run.
	chatSeq  uint64
	agentSeq uint64
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
		methods:  []string{"connect", "health", "chat.send", "node.event"},
		events:   []string{gatewayproto.EventConnectChallenge, "tick", "chat", "agent"},
		throttle: newAuthThrottle(),
	}
}

// SetLockoutExempt sets the client IPs the auth-failure lockout never applies
// to (gateway.lockout_exempt). Loopback is always exempt.
func (s *Server) SetLockoutExempt(set *config.LockoutExemptSet) { s.throttle.setExempt(set) }

// SetInbound installs the agent bridge invoked on each chat.send.
func (s *Server) SetInbound(fn InboundFunc) { s.inbound = fn }

// SetAlerter installs the function operator alerts are raised through.
func (s *Server) SetAlerter(fn func(alerter.Alert)) { s.alert = fn }

// SetQuerier installs the read-only agent/session accessor used to answer
// operator-client RPCs (agents.list, chat.history). When set, those methods are
// advertised in hello-ok and handled in dispatch.
func (s *Server) SetQuerier(q AgentQuerier) {
	s.querier = q
	s.methods = append(s.methods, "agents.list", "chat.history")
}

// connWriter owns the write side of one websocket connection. Before the
// handshake completes, frames are written synchronously (writeJSON); after it,
// run drains a bounded queue that send fills without ever blocking, so nothing
// upstream (a turn, the channel worker) waits on a slow device. Every data
// write carries deviceWriteTimeout, and a failed or late write closes the
// connection. Control frames (ping, close) use WriteControl, which gorilla
// allows concurrently with the writer.
type connWriter struct {
	conn      *websocket.Conn
	queue     chan any
	done      chan struct{}
	closeOnce sync.Once
}

func newConnWriter(conn *websocket.Conn) *connWriter {
	return &connWriter{conn: conn, queue: make(chan any, deviceSendQueueSize), done: make(chan struct{})}
}

// queuedClose is a queue entry that closes the connection once the frames
// queued before it are written.
type queuedClose struct {
	code   int
	reason string
}

// writeJSON writes v now, under deviceWriteTimeout, and closes the connection
// when the write fails. Only the handshake (before run starts) and run call
// it, so data writes never overlap.
func (w *connWriter) writeJSON(v any) error {
	if err := w.conn.SetWriteDeadline(time.Now().Add(deviceWriteTimeout)); err != nil {
		w.close()
		return err
	}
	if err := w.conn.WriteJSON(v); err != nil {
		logger.DebugCF("device", "websocket write failed; closing connection", map[string]any{"error": err.Error()})
		w.close()
		return err
	}
	return nil
}

// run writes queued frames until the connection closes.
func (w *connWriter) run() {
	for {
		select {
		case <-w.done:
			return
		case v := <-w.queue:
			if c, ok := v.(queuedClose); ok {
				w.closeWith(c.code, c.reason)
				return
			}
			if w.writeJSON(v) != nil {
				return
			}
		}
	}
}

// send queues v for the writer. It never blocks: it reports false when the
// connection is closed, and closes a connection whose queue is full (the
// device has stopped reading).
func (w *connWriter) send(v any) bool {
	select {
	case <-w.done:
		return false
	default:
	}
	select {
	case w.queue <- v:
		return true
	default:
		logger.WarnC("device", "device is not reading; closing its connection")
		w.close()
		return false
	}
}

// sendClose queues a close frame behind the frames already queued.
func (w *connWriter) sendClose(code int, reason string) {
	if !w.send(queuedClose{code: code, reason: reason}) {
		w.close()
	}
}

// close ends the writer and closes the socket; safe to call more than once.
func (w *connWriter) close() {
	w.closeOnce.Do(func() {
		close(w.done)
		utils.CloseQuietly(w.conn)
	})
}

// closeWith sends a close frame now and closes the connection.
func (w *connWriter) closeWith(code int, reason string) {
	if err := w.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(2*time.Second)); err != nil {
		logger.DebugCF("device", "close frame write failed", map[string]any{"error": err.Error()})
	}
	w.close()
}

// setReadDeadline applies a read deadline and logs at debug when the conn
// refuses it; the next read fails in that case, which ends the loop anyway.
func setReadDeadline(conn *websocket.Conn, t time.Time) {
	if err := conn.SetReadDeadline(t); err != nil {
		logger.DebugCF("device", "set read deadline failed", map[string]any{"error": err.Error()})
	}
}

func (w *connWriter) ping() error {
	return w.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(deviceWriteTimeout))
}

// register records a post-handshake connection; false once CloseAll has run.
func (s *Server) register(lc *liveConn) bool {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if s.stopped {
		return false
	}
	if s.live == nil {
		s.live = map[*liveConn]struct{}{}
	}
	s.live[lc] = struct{}{}
	return true
}

func (s *Server) unregister(lc *liveConn) {
	s.liveMu.Lock()
	delete(s.live, lc)
	s.liveMu.Unlock()
}

// liveConns returns the open connections that match keep.
func (s *Server) liveConns(keep func(*liveConn) bool) []*liveConn {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	var out []*liveConn
	for lc := range s.live {
		if keep(lc) {
			out = append(out, lc)
		}
	}
	return out
}

// CloseAll closes every open device connection with a going-away close frame
// and refuses connections that complete their handshake afterwards. The
// channel calls it when it stops, so devices reconnect to its replacement at
// once instead of staying attached to a dead channel.
func (s *Server) CloseAll() {
	s.liveMu.Lock()
	s.stopped = true
	s.liveMu.Unlock()
	closeAll(s.liveConns(func(*liveConn) bool { return true }), websocket.CloseGoingAway, "server shutdown")
}

// closeAll closes conns in parallel, so a stuck socket's close-frame timeout
// does not delay the others, and returns when all are closed.
func closeAll(conns []*liveConn, code int, reason string) {
	var wg sync.WaitGroup
	for _, lc := range conns {
		wg.Go(func() { lc.cw.closeWith(code, reason) })
	}
	wg.Wait()
}

// DisconnectDevice closes every open connection of deviceID. The WebUI calls
// it when the device is removed.
func (s *Server) DisconnectDevice(deviceID string) {
	closeAll(s.liveConns(func(lc *liveConn) bool { return lc.deviceID == deviceID }), websocket.ClosePolicyViolation, "device removed")
}

// stillAuthorized reports whether lc's device is still paired and still holds
// a valid device token. A store error counts as no.
func (s *Server) stillAuthorized(ctx context.Context, lc *liveConn) bool {
	if _, ok, err := s.store.GetPaired(ctx, lc.deviceID); err != nil || !ok {
		return false
	}
	for _, tok := range lc.tokens {
		if dt, ok, err := s.store.TokenByValue(ctx, tok); err == nil && ok && dt.DeviceID == lc.deviceID {
			return true
		}
	}
	return false
}

// HandleWS upgrades an HTTP request to the gateway protocol and runs the handshake.
func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	if s.preauth.Add(1) > channels.DeviceMaxPreauthConns {
		s.preauth.Add(-1)
		logger.WarnCF("device", "too many connections awaiting authentication", map[string]any{"remoteIp": clientIP(r)})
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	release := sync.OnceFunc(func() { s.preauth.Add(-1) })
	defer release()

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.DebugCF("device", "websocket upgrade failed", map[string]any{"error": err.Error()})
		return
	}
	// Belt-and-suspenders with EnableCompression:false — never compress writes.
	conn.EnableWriteCompression(false)
	// The first frame is unauthenticated: cap it well below the advertised
	// payload limit, which applies only once the handshake has succeeded.
	conn.SetReadLimit(gatewayproto.MaxPreauthPayloadBytes)
	cw := newConnWriter(conn)

	connID := randomToken(16)
	nonce := randomToken(16)

	// Send the pre-auth challenge before any client frame.
	challenge := gatewayproto.NewEvent(gatewayproto.EventConnectChallenge,
		gatewayproto.ChallengePayload{Nonce: nonce, Ts: time.Now().UnixMilli()}, nil)
	if writeErr := cw.writeJSON(challenge); writeErr != nil {
		return
	}

	setReadDeadline(conn, time.Now().Add(handshakeTimeout))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		utils.CloseQuietly(conn)
		return
	}

	hello, fail := s.handshake(r, connID, nonce, raw)
	if fail != nil {
		logger.WarnCF("device", "connect rejected", map[string]any{
			"reason": fail.reason, "code": fail.err.Code, "message": fail.err.Message,
		})
		if cw.writeJSON(gatewayproto.NewErrorResponse(fail.id, fail.err)) == nil {
			cw.closeWith(fail.code, fail.reason)
		}
		return
	}
	logger.InfoCF("device", "connect accepted", map[string]any{
		"deviceId": hello.deviceID, "role": hello.role,
	})
	release()
	conn.SetReadLimit(gatewayproto.MaxPayloadBytes)

	if err := cw.writeJSON(gatewayproto.NewOKResponse(hello.id, hello.payload)); err != nil {
		return
	}

	lc := &liveConn{cw: cw, deviceID: hello.deviceID, chatID: hello.chatID, tokens: hello.tokens}
	if !s.register(lc) {
		cw.closeWith(websocket.CloseGoingAway, "server shutdown")
		return
	}
	defer s.unregister(lc)
	defer cw.close()
	go cw.run()
	s.conns.Store(lc.chatID, lc)
	defer s.conns.CompareAndDelete(lc.chatID, lc)

	setReadDeadline(conn, time.Time{})
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
	tokens   []string // device tokens handed out in hello-ok
}

// handshake validates the first frame and returns either a hello-ok or a failure.
func (s *Server) handshake(r *http.Request, connID, nonce string, raw []byte) (*handshakeOK, *handshakeFail) {
	var req gatewayproto.RequestFrame
	if err := json.Unmarshal(raw, &req); err != nil || req.Type != gatewayproto.FrameReq {
		return nil, &handshakeFail{id: "", err: gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "first frame must be a connect request", nil), code: websocket.ClosePolicyViolation, reason: "invalid handshake"}
	}
	if req.Method != "connect" { //nolint:usestdlibvars // gateway protocol method name, not the HTTP verb
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
	if s.opts.SharedToken != "" || s.opts.WordToken != "" {
		ip := remoteHost(r)
		if wait := s.throttle.retryAfter(ip); wait > 0 {
			detail := map[string]any{"code": gatewayproto.DetailAuthRateLimited}
			e := gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "too many failed authentication attempts", detail)
			e.Retryable = true
			e.RetryAfterMs = int(wait.Milliseconds())
			return nil, &handshakeFail{id: req.ID, err: e, code: websocket.ClosePolicyViolation, reason: "rate limited"}
		}
		if !s.authorizeGateway(r.Context(), &p) {
			if s.opts.LogMessages {
				logger.WarnCF("device", "device auth failed", map[string]any{
					"clientId":           p.Client.ID,
					"tokenPresent":       p.Auth != nil && p.Auth.Token != "",
					"deviceTokenPresent": p.Auth != nil && p.Auth.DeviceToken != "",
				})
			}
			s.recordAuthFailure(ip)
			detail := map[string]any{"code": gatewayproto.DetailAuthTokenMismatch}
			return nil, &handshakeFail{id: req.ID, err: gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "authentication failed", detail), code: websocket.ClosePolicyViolation, reason: "unauthorized"}
		}
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
		return nil, storeFail(req.ID, "read paired device", p.Device.ID, err)
	}
	pairedAndMatches := ok && paired.PublicKey == p.Device.PublicKey

	if !pairedAndMatches {
		// Create a pending pairing request. Auto-approve only when explicitly enabled.
		reqID, perr := s.store.CreatePending(ctx, PendingPairing{
			DeviceID: p.Device.ID, PublicKey: p.Device.PublicKey, DisplayName: p.Client.DisplayName,
			Platform: p.Client.Platform, DeviceFamily: p.Client.DeviceFamily, ClientID: p.Client.ID,
			ClientMode: p.Client.Mode, Role: role, Scopes: p.Scopes, RemoteIP: remoteHost(r),
		})
		if perr != nil {
			return nil, storeFail(req.ID, "create pending pairing", p.Device.ID, perr)
		}
		if s.opts.AutoApprove {
			if dev, _, aerr := s.store.Approve(ctx, reqID, []string{role}, p.Scopes); aerr == nil {
				paired = dev
				pairedAndMatches = true
			} else {
				logger.WarnCF("device", "Auto-approve failed; pairing stays pending", map[string]any{
					"deviceId": p.Device.ID, "requestId": reqID, "error": aerr.Error(),
				})
			}
		}
		if !pairedAndMatches {
			detail := map[string]any{"code": gatewayproto.DetailPairingRequired, "reason": "not-paired", "requestId": reqID}
			return nil, &handshakeFail{id: req.ID, err: gatewayproto.NewError(gatewayproto.CodeNotPaired, "device pairing required", detail), code: websocket.ClosePolicyViolation, reason: "pairing required"}
		}
	}

	if err := s.store.UpdateLastSeen(ctx, paired.DeviceID, time.Now().UnixMilli()); err != nil {
		logger.WarnCF("device", "failed to update last seen", map[string]any{
			"deviceId": paired.DeviceID, "error": err.Error(),
		})
	}

	hello := s.buildHelloOk(ctx, connID, paired, &p, negotiatedProtocol)
	tokens := make([]string, 0, len(hello.Auth.DeviceTokens))
	for _, t := range hello.Auth.DeviceTokens {
		tokens = append(tokens, t.DeviceToken)
	}
	return &handshakeOK{id: req.ID, payload: hello, deviceID: paired.DeviceID, chatID: "device:" + paired.DeviceID, role: role, scopes: paired.Scopes, tokens: tokens}, nil
}

// storeFail logs a pairing store failure during a handshake with its cause
// and returns the UNAVAILABLE refusal the client gets (close 1011).
func storeFail(reqID, op, deviceID string, err error) *handshakeFail {
	logger.ErrorCF("device", "Pairing store error", map[string]any{
		"op": op, "deviceId": deviceID, "error": err.Error(),
	})
	return &handshakeFail{id: reqID, err: gatewayproto.NewError(gatewayproto.CodeUnavailable, "pairing store error", nil), code: websocket.CloseInternalServerErr, reason: "store error"}
}

// presentedDeviceTokens returns the device tokens the connect request carried
// that belong to deviceID, with their plaintext, so hello-ok can echo them. A
// token that verifies but belongs to another device is not echoed.
func (s *Server) presentedDeviceTokens(ctx context.Context, deviceID string, p *gatewayproto.ConnectParams) []DeviceToken {
	if p == nil || p.Auth == nil {
		return nil
	}
	var out []DeviceToken
	for _, tok := range []string{p.Auth.DeviceToken, p.Auth.Token} {
		if tok == "" {
			continue
		}
		dt, ok, err := s.store.TokenByValue(ctx, tok)
		if err != nil || !ok || dt.DeviceID != deviceID {
			continue
		}
		out = append(out, *dt)
	}
	return out
}

// recordAuthFailure counts a failed gateway authentication against ip. The
// lockout that ends the run of failures is logged, and the first lockout for an
// IP raises an operator alert (later ones for the same IP repeat the log only).
func (s *Server) recordAuthFailure(ip string) {
	lockout, first := s.throttle.fail(ip)
	if lockout == 0 {
		return
	}
	logger.WarnCF("device", "client locked out after repeated authentication failures", map[string]any{
		"remoteIp": ip, "lockout": lockout.String(),
	})
	if first && s.alert != nil {
		s.alert(alerter.Alert{
			EventID: ip,
			Title:   "Device authentication locked out",
			Description: fmt.Sprintf("%s failed device authentication %d times within %s and is locked out for %s; each further lockout doubles, up to %s",
				ip, channels.DeviceAuthFailThreshold, channels.DeviceAuthFailWindow, lockout, channels.DeviceAuthLockoutMax),
		})
	}
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
		// Compare digests, not the raw strings. subtle.ConstantTimeCompare returns
		// early when the lengths differ, so feeding it the secrets directly makes
		// the call's duration depend on the secret's length. Hashing first pins
		// both operands to 32 bytes, so the length branch never fires and the
		// comparison is constant-time with respect to the whole input, not just
		// its contents.
		got := sha256.Sum256([]byte(p.Auth.Token))
		for _, secret := range []string{s.opts.SharedToken, s.opts.WordToken} {
			// An unset secret must never authenticate. The outer Token != ""
			// check already makes this unreachable (no non-empty token hashes to
			// sha256("")), but hashing removes the length mismatch that used to
			// reject it implicitly, so keep the intent explicit here.
			if secret == "" {
				continue
			}
			want := sha256.Sum256([]byte(secret))
			if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
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
//
// The device tokens it carries come from one of two places. When the device
// authenticated with one of its own tokens, that token is echoed back: it is
// the only plaintext the gateway has, since the store keeps hashes. Otherwise
// (shared secret, word token, open auth — the first connect after approval
// among them) the device's tokens are rotated and the new ones sent, so the
// device always leaves hello-ok holding a token it can reconnect with.
func (s *Server) buildHelloOk(ctx context.Context, connID string, paired *PairedDevice, p *gatewayproto.ConnectParams, protocol int) gatewayproto.HelloOk {
	role := gatewayproto.RoleNode
	if len(paired.Roles) > 0 {
		role = paired.Roles[0]
		if slices.Contains(paired.Roles, gatewayproto.RoleNode) {
			role = gatewayproto.RoleNode
		}
	}
	auth := gatewayproto.HelloAuth{Role: role, Scopes: paired.Scopes, IssuedAtMs: time.Now().UnixMilli()}
	toks := s.presentedDeviceTokens(ctx, paired.DeviceID, p)
	if len(toks) == 0 {
		issued, err := s.store.IssueTokens(ctx, paired.DeviceID, paired.Roles, paired.Scopes)
		if err != nil {
			logger.WarnCF("device", "failed to issue device tokens", map[string]any{
				"deviceId": paired.DeviceID, "error": err.Error(),
			})
		}
		toks = issued
	}
	for _, t := range toks {
		auth.DeviceTokens = append(auth.DeviceTokens, gatewayproto.HelloDeviceToken{
			DeviceToken: t.Token, Role: t.Role, Scopes: t.Scopes, IssuedAtMs: t.CreatedAtMs,
		})
		if t.Role == role && auth.DeviceToken == "" {
			auth.DeviceToken = t.Token
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
	setReadDeadline(conn, time.Now().Add(deviceReadTimeout))
	conn.SetPongHandler(func(string) error {
		setReadDeadline(conn, time.Now().Add(deviceReadTimeout))
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
			setReadDeadline(conn, time.Now().Add(deviceReadTimeout))
			var req gatewayproto.RequestFrame
			if json.Unmarshal(raw, &req) != nil || req.Type != gatewayproto.FrameReq {
				continue
			}
			// Pairing is checked on every request, not only at the handshake:
			// a device removed (or whose tokens were revoked) while connected
			// can send nothing more.
			if !s.stillAuthorized(ctx, lc) {
				logger.WarnCF("device", "request refused: device no longer paired", map[string]any{"deviceId": lc.deviceID})
				lc.cw.send(gatewayproto.NewErrorResponse(req.ID,
					gatewayproto.NewError(gatewayproto.CodeNotPaired, "device not paired", nil)))
				lc.cw.sendClose(websocket.ClosePolicyViolation, "device not paired")
				// Let the writer deliver the error and the close frame; its
				// write deadline bounds the wait.
				<-lc.cw.done
				return
			}
			s.dispatch(ctx, lc, req)
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
			if !lc.cw.send(gatewayproto.NewEvent("tick", map[string]any{"ts": time.Now().UnixMilli()}, nil)) {
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
func (s *Server) dispatch(ctx context.Context, lc *liveConn, req gatewayproto.RequestFrame) {
	switch req.Method {
	case "health":
		lc.cw.send(gatewayproto.NewOKResponse(req.ID, map[string]any{"status": "ok"}))
	case "chat.send":
		s.handleChatSend(ctx, lc, req)
	case "node.event":
		s.handleNodeEvent(lc, req)
	case "agents.list":
		s.handleAgentsList(ctx, lc, req)
	case "chat.history":
		s.handleChatHistory(ctx, lc, req)
	default:
		lc.cw.send(gatewayproto.NewErrorResponse(req.ID,
			gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "method not supported: "+req.Method, nil)))
	}
}

// handleChatSend acks the turn and bridges the transcript into the agent layer.
// Partial assistant text may arrive via StreamDelta as the model generates; the
// terminal reply always arrives via DeliverReply.
func (s *Server) handleChatSend(ctx context.Context, lc *liveConn, req gatewayproto.RequestFrame) {
	var p struct {
		Message        string `json:"message"`
		SessionKey     string `json:"sessionKey"`
		IdempotencyKey string `json:"idempotencyKey"`
		Attachments    []struct {
			MimeType string `json:"mimeType"`
			Name     string `json:"name"`
			Data     string `json:"data"` // base64
		} `json:"attachments"`
	}
	if json.Unmarshal(req.Params, &p) != nil {
		lc.cw.send(gatewayproto.NewErrorResponse(req.ID,
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
	// Device text commands (e.g. "/agent bob") are answered without resolving
	// the session first, so a stored key naming an agent the device may no
	// longer reach cannot block the command that fixes it.
	cmd, cmdArg, isCmd := parseSlashCommand(p.Message)
	isCmd = isCmd && (cmd == "agent" || cmd == "help")
	scopeKey := ""
	if !isCmd {
		requested := p.SessionKey
		if requested == "" {
			lc.mu.Lock()
			requested = lc.sessionKey
			lc.mu.Unlock()
		}
		var err error
		scopeKey, err = s.sessionScopeKeyFor(ctx, lc, requested)
		if err != nil {
			s.refuseSessionKey(lc, req.ID, err)
			return
		}
	}
	lc.mu.Lock()
	if p.SessionKey != "" && !isCmd {
		lc.sessionKey = p.SessionKey
	}
	lc.currentRun = runID
	// New run: clear any streaming accumulator + per-run seq counters.
	lc.streamedRun = ""
	lc.streamedText.Reset()
	lc.chatSeq = 0
	lc.agentSeq = 0
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
	lc.cw.send(gatewayproto.NewOKResponse(req.ID, map[string]any{"runId": runID, "status": "started"}))

	// Device text commands are answered as a reply event, without running the
	// agent loop.
	if isCmd {
		if cmd == "agent" {
			go s.handleAgentCommand(ctx, lc, runID, cmdArg)
		} else {
			go s.emitChatReply(lc, runID, s.replyScopeKey(ctx, lc), deviceHelpText)
		}
		return
	}

	// Decode inline attachments (e.g. photos). Oversized or undecodable entries are
	// skipped with a warning rather than failing the turn.
	var attachments []InboundAttachment
	for i, a := range p.Attachments {
		raw, derr := base64.StdEncoding.DecodeString(a.Data)
		if derr != nil || len(raw) == 0 {
			logger.WarnCF("device", "chat.send: undecodable attachment; skipping",
				map[string]any{"deviceId": lc.deviceID, "index": i, "mime": a.MimeType})
			continue
		}
		if len(raw) > maxDeviceAttachmentBytes {
			logger.WarnCF("device", "chat.send: attachment too large; skipping",
				map[string]any{"deviceId": lc.deviceID, "index": i, "bytes": len(raw), "max": maxDeviceAttachmentBytes})
			continue
		}
		attachments = append(attachments, InboundAttachment{MimeType: a.MimeType, Name: a.Name, Data: raw})
	}

	if s.inbound != nil {
		go s.inbound(lc.deviceID, lc.chatID, p.Message, runID, scopeKey, attachments)
	}
}

// maxDeviceAttachmentBytes caps a single decoded inbound attachment to keep a
// malicious/oversized chat.send from ballooning memory. Photos are well under it.
const maxDeviceAttachmentBytes = 25 << 20 // 25 MiB

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
// clears the assignment back to the gateway default. The device may target any
// configured agent its allowed set (PairedDevice.AllowedAgents) admits; the list
// shows only those, and any other name is answered as unknown.
func (s *Server) handleAgentCommand(ctx context.Context, lc *liveConn, runID, arg string) {
	var all []DeviceAgentInfo
	if s.querier != nil {
		all, _, _ = s.querier.Agents()
	}
	dev, err := s.pairedRecord(ctx, lc)
	if err != nil {
		s.emitChatReply(lc, runID, "", "This device's settings could not be read.")
		return
	}
	agents := all
	if dev != nil {
		agents = allowedAgentInfos(all, *dev)
	}

	if len(all) == 0 {
		s.emitChatReply(lc, runID, s.replyScopeKey(ctx, lc), "No assistants are configured.")
		return
	}
	current, err := s.deviceAgentFor(lc, dev)
	if err != nil || len(agents) == 0 {
		s.emitChatReply(lc, runID, s.replyScopeKey(ctx, lc), "No assistants are available on this device.")
		return
	}
	if arg == "" || strings.EqualFold(arg, "list") {
		s.emitChatReply(lc, runID, s.replyScopeKey(ctx, lc), formatAgentList(agents, current))
		return
	}
	if strings.EqualFold(arg, "default") || strings.EqualFold(arg, "reset") {
		if err := s.store.SetDeviceAgent(ctx, lc.deviceID, ""); err != nil {
			s.emitChatReply(lc, runID, s.replyScopeKey(ctx, lc), "Couldn't reset assistant: "+err.Error())
			return
		}
		// Name the agent the device now reaches, which is the gateway default
		// only when the device's allowed set admits it.
		var reset PairedDevice
		if dev != nil {
			reset = *dev
		}
		reset.AgentID = ""
		name := "the default assistant"
		if id, err := s.deviceAgentFor(lc, &reset); err == nil {
			name = agentNameFor(agents, id)
		}
		s.emitChatReply(lc, runID, s.replyScopeKey(ctx, lc),
			"Switched to "+name+". New messages will go to this assistant.")
		return
	}

	// Only the agents the device may reach are matched, so a hidden agent
	// gets the same reply as one that does not exist.
	targetID, targetName := resolveDeviceAgent(agents, arg)
	if targetID == "" {
		s.emitChatReply(lc, runID, s.replyScopeKey(ctx, lc),
			"No assistant matches \""+arg+"\".\n\n"+formatAgentList(agents, current))
		return
	}
	if err := s.store.SetDeviceAgent(ctx, lc.deviceID, targetID); err != nil {
		s.emitChatReply(lc, runID, s.replyScopeKey(ctx, lc), "Couldn't switch assistant: "+err.Error())
		return
	}
	// Recompute the scope so the confirmation is tagged with the new assistant.
	s.emitChatReply(lc, runID, s.replyScopeKey(ctx, lc),
		"Switched to "+targetName+". New messages will go to this assistant.")
}

// resolveDeviceAgent matches an argument against agent ids and names
// (case-insensitive) and returns the canonical id + display name, or "" if none.
func resolveDeviceAgent(agents []DeviceAgentInfo, arg string) (id, name string) {
	for _, a := range agents {
		if config.SameAgentID(a.ID, arg) || strings.EqualFold(a.Name, arg) {
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
		if config.SameAgentID(a.ID, currentID) {
			b.WriteString(" (current)")
		}
		b.WriteString("\n")
	}
	b.WriteString("\nSend \"/agent <name>\" to switch, or \"/agent default\" to reset.")
	return b.String()
}

// agentNameFor is the display name of the agent id among agents, or the id
// itself when it is not listed.
func agentNameFor(agents []DeviceAgentInfo, id string) string {
	for _, a := range agents {
		if config.SameAgentID(a.ID, id) {
			return agentDisplayName(a)
		}
	}
	return id
}

func agentDisplayName(a DeviceAgentInfo) string {
	if a.Name != "" {
		return a.Name
	}
	return a.ID
}

// replyScopeKey is the session key a device-command reply is tagged with: the
// connection's resolved session, or "" when its key names an unknown agent.
func (s *Server) replyScopeKey(ctx context.Context, lc *liveConn) string {
	lc.mu.Lock()
	key := lc.sessionKey
	lc.mu.Unlock()
	resolved, err := s.sessionScopeKeyFor(ctx, lc, key)
	if err != nil {
		return ""
	}
	return resolved
}

// errUnknownAgent refuses a session key naming an agent that does not exist.
var errUnknownAgent = errors.New("unknown agent")

// errAgentNotAllowed refuses a session key naming an agent outside the
// device's allowed set.
var errAgentNotAllowed = errors.New("agent not allowed for this device")

// errNoAllowedAgent refuses a turn that names no agent when none of the
// agents the device is allowed runs.
var errNoAllowedAgent = errors.New("no allowed assistant is available on this device")

// errDeviceRecordUnavailable refuses a request whose device record cannot be
// read: without it the device's allowed set is unknown, so the request fails
// closed.
var errDeviceRecordUnavailable = errors.New("device record unavailable")

// refuseSessionKey answers a request whose agent cannot be resolved
// (errUnknownAgent, errAgentNotAllowed, errNoAllowedAgent,
// errDeviceRecordUnavailable); the
// error's text is the refusal's message and refusalCode its code. A record
// failure was already logged once by pairedRecord.
func (s *Server) refuseSessionKey(lc *liveConn, reqID string, err error) {
	if !errors.Is(err, errDeviceRecordUnavailable) && !errors.Is(err, errNoAllowedAgent) {
		logger.WarnCF("device", "request refused: session key not allowed", map[string]any{
			"deviceId": lc.deviceID, "reason": err.Error(),
		})
	}
	lc.cw.send(gatewayproto.NewErrorResponse(reqID, gatewayproto.NewError(refusalCode(err), err.Error(), nil)))
}

// refusalCode is the protocol error code for a refusal: UNAVAILABLE for a
// server-side failure to read the device record or when no allowed agent
// runs, INVALID_REQUEST otherwise.
func refusalCode(err error) gatewayproto.ErrorCode {
	if errors.Is(err, errDeviceRecordUnavailable) || errors.Is(err, errNoAllowedAgent) {
		return gatewayproto.CodeUnavailable
	}
	return gatewayproto.CodeInvalidRequest
}

// agentExists reports whether id names a configured agent. Without a querier
// no agent is known.
func (s *Server) agentExists(id string) bool {
	if s.querier == nil {
		return false
	}
	agents, _, _ := s.querier.Agents()
	id = routing.NormalizeAgentID(id)
	for _, a := range agents {
		if routing.NormalizeAgentID(a.ID) == id {
			return true
		}
	}
	return false
}

// assignedAgentRuns reports whether a per-device assignment names an agent the
// loop runs. Unlike agentExists it admits human agents (the turn reaches them
// and they refuse it themselves). Without a querier nothing can be checked and
// the assignment is honoured.
func (s *Server) assignedAgentRuns(id string) bool {
	if s.querier == nil {
		return true
	}
	return s.querier.HasAgent(id)
}

// sessionScopeKeyFor resolves the conversation session for a client-supplied key.
// Both chat.send and chat.history go through it, so a device always reads the
// transcript it writes.
//
// Every device joins the selected agent's main conversation: the R1, the phone
// app, Slack and Telegram are one assistant with one history, one tool surface,
// and one memory. Tell her something on the R1 and she knows it in Slack. To
// keep a device separate, give it its own agent.
//
// An operator client picks the agent via the key's 2nd segment, which must name
// a configured agent the device is allowed (an unknown or disallowed id is
// refused before anything touches a session store); a node client (e.g. the
// R1) has no picker, so it gets deviceAgentFor's choice. The choice reaches
// the loop as preresolved_agent_id.
func (s *Server) sessionScopeKeyFor(ctx context.Context, lc *liveConn, requested string) (string, error) {
	dev, err := s.pairedRecord(ctx, lc)
	if err != nil {
		return "", err
	}
	agentID := routing.AgentIDFromSessionKey(requested)
	if agentID != "" {
		if !s.agentExists(agentID) {
			return "", errUnknownAgent
		}
		if dev != nil && !dev.Allows(agentID) {
			return "", errAgentNotAllowed
		}
		return routing.BuildAgentMainSessionKey(agentID), nil
	}
	agentID, err = s.deviceAgentFor(lc, dev)
	if err != nil {
		return "", err
	}
	return routing.BuildAgentMainSessionKey(agentID), nil
}

// pairedRecord is the connection's paired-device record, or nil when the
// device is not paired (no assignment, no restriction). A store error is
// logged at WARN once per device and returned as errDeviceRecordUnavailable,
// so the caller refuses the request rather than ignore the device's allowed
// set.
func (s *Server) pairedRecord(ctx context.Context, lc *liveConn) (*PairedDevice, error) {
	dev, ok, err := s.store.GetPaired(ctx, lc.deviceID)
	if err != nil {
		s.warnOnce(lc.deviceID+"|record unavailable", "paired device record unreadable; refusing the request", map[string]any{
			"deviceId": lc.deviceID, "error": err.Error(),
		})
		return nil, errDeviceRecordUnavailable
	}
	if !ok {
		return nil, nil
	}
	return dev, nil
}

// deviceAgentFor is the agent a turn reaches when the client names none: the
// device's assignment (WebUI Devices page / "/agent") while that agent runs
// and is allowed, else the gateway default when allowed, else the first
// allowed agent the device can list (allowedFallback). When none is, the turn
// is refused with errNoAllowedAgent and a WARN is logged once: falling back
// to the default would reach an agent outside the set. A stale or disallowed
// assignment is skipped with a WARN once: the loop drops a turn preresolved
// to a missing agent, so honouring it would leave the device unanswered. dev
// may be nil (no record).
func (s *Server) deviceAgentFor(lc *liveConn, dev *PairedDevice) (string, error) {
	defaultID := "main"
	if s.querier != nil {
		if d := s.querier.DefaultAgentID(); d != "" {
			defaultID = d
		}
	}
	if dev == nil {
		return defaultID, nil
	}
	assignedReason := ""
	if dev.AgentID != "" {
		switch {
		case !s.assignedAgentRuns(dev.AgentID):
			assignedReason = "assigned agent not found"
		case !dev.Allows(dev.AgentID):
			assignedReason = "assigned agent not allowed for this device"
		default:
			return dev.AgentID, nil
		}
	}
	agentID, found := defaultID, dev.Allows(defaultID)
	if !found {
		agentID, found = s.allowedFallback(*dev)
	}
	if assignedReason != "" {
		s.warnOnce(lc.deviceID+"|"+dev.AgentID+"|"+assignedReason, assignedReason+"; using another agent", map[string]any{
			"deviceId": lc.deviceID, "device": dev.DisplayName, "assignedAgent": dev.AgentID, "agent": agentID,
		})
	}
	if !found {
		s.warnOnce(lc.deviceID+"|no allowed agent", "no allowed agent runs; refusing the device's turns", map[string]any{
			"deviceId": lc.deviceID, "device": dev.DisplayName, "allowedAgents": dev.AllowedAgents,
		})
		return "", errNoAllowedAgent
	}
	return agentID, nil
}

// allowedFallback is the first of dev's allowed agents, in their stored
// order, that agents.list offers (a configured agent the loop runs, not a
// human agent), so the fallback is always one the device can see. Without a
// querier nothing can be checked and the first allowed id is used.
func (s *Server) allowedFallback(dev PairedDevice) (string, bool) {
	if s.querier == nil {
		if len(dev.AllowedAgents) > 0 {
			return dev.AllowedAgents[0], true
		}
		return "", false
	}
	listed, _, _ := s.querier.Agents()
	for _, id := range dev.AllowedAgents {
		for _, a := range listed {
			if config.SameAgentID(a.ID, id) {
				return a.ID, true
			}
		}
	}
	return "", false
}

// warnOnce logs msg at WARN the first time key is seen, so a device that
// keeps talking logs a misconfiguration once.
func (s *Server) warnOnce(key, msg string, fields map[string]any) {
	if _, seen := s.staleWarned.LoadOrStore(key, true); !seen {
		logger.WarnCF("device", msg, fields)
	}
}

// allowedAgentInfos is the subset of agents the device dev may talk to.
func allowedAgentInfos(agents []DeviceAgentInfo, dev PairedDevice) []DeviceAgentInfo {
	if len(dev.AllowedAgents) == 0 {
		return agents
	}
	out := make([]DeviceAgentInfo, 0, len(agents))
	for _, a := range agents {
		if dev.Allows(a.ID) {
			out = append(out, a)
		}
	}
	return out
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
		lc.cw.send(gatewayproto.NewErrorResponse(req.ID,
			gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "invalid node.event params", nil)))
		return
	}
	if p.Event == "chat.subscribe" && p.Payload.SessionKey != "" {
		lc.mu.Lock()
		lc.sessionKey = p.Payload.SessionKey
		lc.mu.Unlock()
	}
	lc.cw.send(gatewayproto.NewOKResponse(req.ID, map[string]any{"ok": true}))
}

// handleAgentsList answers an operator client's agents.list with the configured
// agents and the default agent's main session key (which the client echoes back
// as the chat.history/chat.send sessionKey). A device limited to some agents
// gets only those, with defaultId/mainKey naming the agent an unqualified
// chat.send reaches (deviceAgentFor), or empty when that is not one of them.
func (s *Server) handleAgentsList(ctx context.Context, lc *liveConn, req gatewayproto.RequestFrame) {
	if s.querier == nil {
		lc.cw.send(gatewayproto.NewErrorResponse(req.ID,
			gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "method not supported: agents.list", nil)))
		return
	}
	dev, err := s.pairedRecord(ctx, lc)
	if err != nil {
		s.refuseSessionKey(lc, req.ID, err)
		return
	}
	agents, defaultID, mainKey := s.querier.Agents()
	if dev != nil && len(dev.AllowedAgents) > 0 {
		agents = allowedAgentInfos(agents, *dev)
		// defaultId is always one of the listed agents: an agent outside
		// them (none runs, or a human agent assigned) leaves both empty.
		defaultID, mainKey = "", ""
		if id, err := s.deviceAgentFor(lc, dev); err == nil {
			for _, a := range agents {
				if config.SameAgentID(a.ID, id) {
					defaultID, mainKey = a.ID, routing.BuildAgentMainSessionKey(a.ID)
					break
				}
			}
		}
	}
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
	lc.cw.send(gatewayproto.NewOKResponse(req.ID, map[string]any{
		"defaultId": defaultID,
		"mainKey":   mainKey,
		"scope":     "global",
		"agents":    list,
	}))
}

// handleChatHistory returns the stored transcript for the requested session key,
// shaped like the live "chat" event message so the client renders past turns the
// same way it renders incoming replies.
func (s *Server) handleChatHistory(ctx context.Context, lc *liveConn, req gatewayproto.RequestFrame) {
	if s.querier == nil {
		lc.cw.send(gatewayproto.NewErrorResponse(req.ID,
			gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "method not supported: chat.history", nil)))
		return
	}
	var p struct {
		SessionKey string `json:"sessionKey"`
		historyPageParams
	}
	if json.Unmarshal(req.Params, &p) != nil {
		lc.cw.send(gatewayproto.NewErrorResponse(req.ID,
			gatewayproto.NewError(gatewayproto.CodeInvalidRequest, "invalid chat.history params", nil)))
		return
	}
	sessionKey := p.SessionKey
	if sessionKey == "" {
		lc.mu.Lock()
		sessionKey = lc.sessionKey
		lc.mu.Unlock()
	}
	// Resolve through the same rule chat.send uses, so a client reads the
	// transcript its turns are written to — the agent's main conversation, not
	// the key the client happens to have asked for.
	sessionKey, err := s.sessionScopeKeyFor(ctx, lc, sessionKey)
	if err != nil {
		s.refuseSessionKey(lc, req.ID, err)
		return
	}
	page, err := pageHistory(s.querier.History(sessionKey), p.historyPageParams)
	if err != nil {
		lc.cw.send(gatewayproto.NewErrorResponse(req.ID,
			gatewayproto.NewError(gatewayproto.CodeInvalidRequest, err.Error(), nil)))
		return
	}
	messages := make([]map[string]any, 0, len(page.Messages))
	for _, m := range page.Messages {
		entry := map[string]any{
			"role":    m.Role,
			"content": []map[string]any{{"type": "text", "text": m.Content}},
		}
		if !m.Timestamp.IsZero() {
			entry["timestamp"] = m.Timestamp.UnixMilli()
		}
		messages = append(messages, entry)
	}
	payload := map[string]any{
		"sessionKey":    sessionKey,
		"messages":      messages,
		"offset":        page.Offset,
		"totalMessages": page.TotalMessages,
		"hasMore":       page.HasMore,
	}
	if page.HasMore {
		payload["nextOffset"] = page.NextOffset
	}
	lc.cw.send(gatewayproto.NewOKResponse(req.ID, payload))
}

// StreamDelta emits an incremental assistant-text update to the device for the
// given chatID, keyed to the in-flight runId. It accumulates the running text on
// the connection and emits two events per delta mirroring emitChatReply's shapes:
//   - a "chat" event state:"delta" carrying deltaText and the accumulated message
//     content (so a node client can render progressively);
//   - an "agent" event stream:"assistant" carrying the accumulated text and this
//     delta (operator/voice clients accumulate data.text).
//
// It marks the run as streamed so the terminal emitChatReply does not re-send the
// assistant text. Returns false when no connection matches or there is no active
// run (nothing to correlate the delta to).
func (s *Server) StreamDelta(chatID, delta string) bool {
	if delta == "" {
		return false
	}
	v, _ := s.conns.Load(chatID)
	lc, ok := v.(*liveConn)
	if !ok {
		return false
	}

	lc.mu.Lock()
	runID := lc.currentRun
	if runID == "" {
		lc.mu.Unlock()
		return false
	}
	// Adopt the current run for streaming (accumulator was reset on chat.send).
	lc.streamedRun = runID
	lc.streamedText.WriteString(delta)
	accumulated := lc.streamedText.String()
	lc.chatSeq++
	chatPayloadSeq := lc.chatSeq
	lc.agentSeq++
	agentPayloadSeq := lc.agentSeq
	lc.seq++
	chatFrameSeq := lc.seq
	lc.seq++
	agentFrameSeq := lc.seq
	lc.mu.Unlock()

	// Per-delta logging is intentionally disabled — it logs conversation content
	// and is very chatty (one line per streamed chunk). Uncomment to watch the
	// stream during testing; newlines/tabs are escaped so a multi-line delta stays
	// on one grep-visible log line.
	//
	// if s.opts.LogMessages {
	// 	logDelta := strings.NewReplacer("\n", "\\n", "\r", "\\r", "\t", "\\t").Replace(delta)
	// 	logger.InfoCF("device", "chat delta out", map[string]any{
	// 		"deviceId": lc.deviceID, "runId": runID, "delta": logDelta,
	// 	})
	// }

	ts := time.Now().UnixMilli()

	sendEvent := func(eventName string, p map[string]any, seq uint64) bool {
		return lc.cw.send(gatewayproto.NewEvent(eventName, p, &seq))
	}

	// "chat" delta — progressive transcript update. Mirrors the final message shape
	// (role+content+text) but state:"delta" with the new fragment in deltaText.
	sendEvent("chat", map[string]any{
		"runId":     runID,
		"state":     "delta",
		"seq":       chatPayloadSeq,
		"deltaText": delta,
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": accumulated}},
			"text":    accumulated,
		},
	}, chatFrameSeq)

	// "agent" assistant delta — operator/voice clients accumulate data.text across
	// events, so data.text carries only this increment (not the running total).
	return sendEvent("agent", map[string]any{
		"runId":  runID,
		"seq":    agentPayloadSeq,
		"stream": "assistant",
		"ts":     ts,
		"data":   map[string]any{"text": delta, "delta": delta},
	}, agentFrameSeq)
}

// DeliverReply emits a terminal "chat" final event to the device for the given
// chatID ("device:<deviceID>"), carrying the in-flight runId. It says why a
// reply was not delivered: channels.ErrRecipientNotFound when chatID names no
// paired device, channels.ErrRecipientOffline when the device is paired but
// not connected or its connection is closing (closed, or closed because it
// stopped reading), and channels.ErrSendFailed for a fault of the server
// itself (the pairing store failed).
func (s *Server) DeliverReply(ctx context.Context, chatID, content string) error {
	v, _ := s.conns.Load(chatID)
	lc, ok := v.(*liveConn)
	if !ok {
		return s.undeliverable(ctx, chatID)
	}
	lc.mu.Lock()
	runID := lc.currentRun
	sessionKey := lc.sessionKey
	lc.currentRun = ""
	lc.mu.Unlock()
	if !s.emitChatReply(lc, runID, sessionKey, content) {
		return fmt.Errorf("%s: the connection is closing: %w", chatID, channels.ErrRecipientOffline)
	}
	return nil
}

// undeliverable says why chatID has no live connection: no paired device by
// that id, or a paired device that is not connected.
func (s *Server) undeliverable(ctx context.Context, chatID string) error {
	deviceID, ok := strings.CutPrefix(chatID, "device:")
	if !ok || deviceID == "" {
		return fmt.Errorf("%s is not a device chat: %w", chatID, channels.ErrRecipientNotFound)
	}
	_, paired, err := s.store.GetPaired(ctx, deviceID)
	if err != nil {
		return fmt.Errorf("%s: look up the paired device: %w: %w", chatID, channels.ErrSendFailed, err)
	}
	if !paired {
		return fmt.Errorf("%s: no paired device: %w", chatID, channels.ErrRecipientNotFound)
	}
	return fmt.Errorf("%s: device not connected: %w", chatID, channels.ErrRecipientOffline)
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
	// A run that already streamed delivered its assistant text via StreamDelta's
	// "agent" assistant events; re-sending the full text here would make voice
	// clients speak the reply twice. Skip only the agent assistant full-text event
	// for such runs — the lifecycle end and the authoritative "chat" final still
	// fire. Reset the per-run streaming state now that the run is finalized.
	lc.mu.Lock()
	streamed := runID != "" && lc.streamedRun == runID
	lc.streamedRun = ""
	lc.streamedText.Reset()
	var agentTextFrameSeq, agentTextPayloadSeq uint64
	if !streamed {
		lc.seq++
		agentTextFrameSeq = lc.seq
		lc.agentSeq++
		agentTextPayloadSeq = lc.agentSeq
	}
	lc.seq++
	agentEndFrameSeq := lc.seq
	lc.agentSeq++
	agentEndPayloadSeq := lc.agentSeq
	lc.seq++
	chatFrameSeq := lc.seq
	lc.chatSeq++
	chatPayloadSeq := lc.chatSeq
	lc.mu.Unlock()

	if s.opts.LogMessages {
		logger.InfoCF("device", "chat reply out", map[string]any{
			"deviceId": lc.deviceID, "runId": runID, "sessionKey": sessionKey, "content": content,
		})
	}

	sendEvent := func(eventName string, p map[string]any, seq uint64) bool {
		return lc.cw.send(gatewayproto.NewEvent(eventName, p, &seq))
	}

	ts := time.Now().UnixMilli()

	// Emit the "agent" stream first (assistant text, then lifecycle end), then the
	// "chat" final. A real gateway streams the assistant events during the run and
	// finalizes after; sending "chat" final first can make a node client (the R1)
	// mark the turn complete and skip feeding the agent text to its speech pipeline.

	// "agent" assistant stream — operator/voice clients read data.text/delta. Matches
	// OpenClaw emitAcpAssistantDelta: {runId, seq, stream:"assistant", ts, data:{text, delta}}.
	// Skipped for runs that already streamed the text via StreamDelta.
	if !streamed {
		sendEvent("agent", map[string]any{
			"runId":  runID,
			"seq":    agentTextPayloadSeq,
			"stream": "assistant",
			"ts":     ts,
			"data":   map[string]any{"text": content, "delta": content},
		}, agentTextFrameSeq)
	}

	// "agent" lifecycle end — marks the run complete so voice/operator clients
	// resolve the turn. Matches OpenClaw emitAcpLifecycleEnd: stream:"lifecycle" data.phase:"end".
	sendEvent("agent", map[string]any{
		"runId":  runID,
		"seq":    agentEndPayloadSeq,
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
		"seq":        chatPayloadSeq,
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

// remoteHost is clientIP without the port: the key the auth throttle counts by.
func remoteHost(r *http.Request) string {
	addr := clientIP(r)
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}
