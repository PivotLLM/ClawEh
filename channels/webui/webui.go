package webui

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/identity"
	"github.com/PivotLLM/ClawEh/logger"
)

// webuiConn represents a single WebSocket connection.
type webuiConn struct {
	id        string
	conn      *websocket.Conn
	sessionID string
	writeMu   sync.Mutex
	closed    atomic.Bool
}

// writeJSON sends a JSON message to the connection with write locking.
func (pc *webuiConn) writeJSON(v any) error {
	if pc.closed.Load() {
		return fmt.Errorf("connection closed")
	}
	pc.writeMu.Lock()
	defer pc.writeMu.Unlock()
	return pc.conn.WriteJSON(v)
}

// close closes the connection.
func (pc *webuiConn) close() {
	if pc.closed.CompareAndSwap(false, true) {
		pc.conn.Close()
	}
}

// WebUIChannel implements the WebUI channel.
// It serves as the reference implementation for all optional capability interfaces.
type WebUIChannel struct {
	*channels.BaseChannel
	config      config.WebUIConfig
	upgrader    websocket.Upgrader
	connections sync.Map // connID → *webuiConn
	connCount   atomic.Int32
	ctx         context.Context
	cancel      context.CancelFunc
}

// TokenSubprotocol is the WebSocket subprotocol name that marks the second
// offered subprotocol as the channel token. A browser cannot set an
// Authorization header on a WebSocket handshake, so this is how the token
// reaches the server without going in the URL, where it would be captured by
// access logs, Referer headers and browser history.
//
// The client offers ["claw-token", "<token>"]; the server echoes only
// "claw-token", never the token.
const TokenSubprotocol = "claw-token"

// originAllowed implements the WebSocket origin policy, which is what stands
// between this socket and a cross-site WebSocket hijack: any page in the
// browser can open a WebSocket to localhost, and unlike fetch, it is not
// stopped by CORS.
//
// An empty allowOrigins means SAME ORIGIN — the Origin's host must match the
// Host the request was sent to. Same-origin rather than a fixed list because
// the operator may reach the UI as localhost, a LAN address, or a proxied
// hostname, and all of those are legitimately "this UI talking to itself".
// A request with no Origin header at all is not a browser, so it is allowed and
// left to token authentication.
//
// An explicit list is honoured verbatim, with "*" still meaning any origin —
// which is what a frontend dev server on another port needs.
func originAllowed(r *http.Request, allowOrigins []string) bool {
	origin := r.Header.Get("Origin")

	if len(allowOrigins) > 0 {
		for _, allowed := range allowOrigins {
			if allowed == "*" || allowed == origin {
				return true
			}
		}
		return false
	}

	if origin == "" {
		return true // not a browser; the token is the gate
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// NewWebUIChannel creates a new WebUI channel.
func NewWebUIChannel(cfg config.WebUIConfig, messageBus *bus.MessageBus) (*WebUIChannel, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("webui token is required")
	}

	base := channels.NewBaseChannel("webui", cfg, messageBus, cfg.AllowFrom)

	allowOrigins := cfg.AllowOrigins

	return &WebUIChannel{
		BaseChannel: base,
		config:      cfg,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return originAllowed(r, allowOrigins) },
			// The browser WebSocket API cannot set request headers, so the token
			// travels as a subprotocol instead of a query parameter (see
			// authenticate). Advertising the name here makes gorilla echo it in
			// the handshake response — required, or the browser aborts the
			// connection — while the token itself, offered second, is not echoed.
			Subprotocols:    []string{TokenSubprotocol},
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
	}, nil
}

// Start implements Channel.
func (c *WebUIChannel) Start(ctx context.Context) error {
	logger.InfoC("webui", "Starting WebUI channel")
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.SetRunning(true)
	logger.InfoC("webui", "WebUI channel started")
	return nil
}

// Stop implements Channel.
func (c *WebUIChannel) Stop(ctx context.Context) error {
	logger.InfoC("webui", "Stopping WebUI channel")
	c.SetRunning(false)

	// Close all connections
	c.connections.Range(func(key, value any) bool {
		if pc, ok := value.(*webuiConn); ok {
			pc.close()
		}
		c.connections.Delete(key)
		return true
	})

	if c.cancel != nil {
		c.cancel()
	}

	logger.InfoC("webui", "WebUI channel stopped")
	return nil
}

// WebhookPath implements channels.WebhookHandler.
func (c *WebUIChannel) WebhookPath() string { return "/webui/" }

// ServeHTTP implements http.Handler for the shared HTTP server.
func (c *WebUIChannel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/webui")

	switch path {
	case "/ws", "/ws/":
		c.handleWebSocket(w, r)
	default:
		http.NotFound(w, r)
	}
}

// Send implements Channel — sends a message to the appropriate WebSocket connection.
func (c *WebUIChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	outMsg := newMessage(TypeMessageCreate, map[string]any{
		"content": msg.Content,
	})

	return c.broadcastToSession(msg.ChatID, outMsg)
}

// EditMessage implements channels.MessageEditor.
func (c *WebUIChannel) EditMessage(ctx context.Context, chatID string, messageID string, content string) error {
	outMsg := newMessage(TypeMessageUpdate, map[string]any{
		"message_id": messageID,
		"content":    content,
	})
	return c.broadcastToSession(chatID, outMsg)
}

// StartTyping implements channels.TypingCapable.
func (c *WebUIChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	startMsg := newMessage(TypeTypingStart, nil)
	if err := c.broadcastToSession(chatID, startMsg); err != nil {
		return func() {}, err
	}
	return func() {
		stopMsg := newMessage(TypeTypingStop, nil)
		c.broadcastToSession(chatID, stopMsg)
	}, nil
}

// SendPlaceholder implements channels.PlaceholderCapable.
// It sends a placeholder message via the WebUI channel that will later be
// edited to the actual response via EditMessage (channels.MessageEditor).
func (c *WebUIChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	if !c.config.Placeholder.Enabled {
		return "", nil
	}

	text := c.config.Placeholder.Text
	if text == "" {
		text = "Thinking... 💭"
	}

	msgID := uuid.New().String()
	outMsg := newMessage(TypeMessageCreate, map[string]any{
		"content":    text,
		"message_id": msgID,
	})

	if err := c.broadcastToSession(chatID, outMsg); err != nil {
		return "", err
	}

	return msgID, nil
}

// broadcastToSession sends a message to all connections with a matching session.
func (c *WebUIChannel) broadcastToSession(chatID string, msg WebUIMessage) error {
	// chatID format: "webui:<sessionID>"
	sessionID := strings.TrimPrefix(chatID, "webui:")
	msg.SessionID = sessionID

	var sent bool
	c.connections.Range(func(key, value any) bool {
		pc, ok := value.(*webuiConn)
		if !ok {
			return true
		}
		if pc.sessionID == sessionID {
			if err := pc.writeJSON(msg); err != nil {
				logger.DebugCF("webui", "Write to connection failed", map[string]any{
					"conn_id": pc.id,
					"error":   err.Error(),
				})
			} else {
				sent = true
			}
		}
		return true
	})

	if !sent {
		return fmt.Errorf("no active connections for session %s: %w", sessionID, channels.ErrSendFailed)
	}
	return nil
}

// handleWebSocket upgrades the HTTP connection and manages the WebSocket lifecycle.
func (c *WebUIChannel) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}

	// Authenticate
	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Check connection limit
	maxConns := c.config.MaxConnections
	if maxConns <= 0 {
		maxConns = 100
	}
	if int(c.connCount.Load()) >= maxConns {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}

	conn, err := c.upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.ErrorCF("webui", "WebSocket upgrade failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	// Determine session ID from query param or generate one
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	pc := &webuiConn{
		id:        uuid.New().String(),
		conn:      conn,
		sessionID: sessionID,
	}

	c.connections.Store(pc.id, pc)
	c.connCount.Add(1)

	logger.InfoCF("webui", "WebSocket client connected", map[string]any{
		"conn_id":    pc.id,
		"session_id": sessionID,
	})

	go c.readLoop(pc)
}

// authenticate checks the Bearer token from the Authorization header.
// Query parameter authentication is only allowed when AllowTokenQuery is explicitly enabled.
func (c *WebUIChannel) authenticate(r *http.Request) bool {
	token := c.config.Token
	if token == "" {
		return false
	}

	// Check Authorization header
	auth := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
		if after == token {
			return true
		}
	}

	// Subprotocol form, used by the browser: ["claw-token", "<token>"].
	for _, proto := range websocket.Subprotocols(r) {
		if proto == TokenSubprotocol {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(proto), []byte(token)) == 1 {
			return true
		}
	}

	// Query parameter, only when explicitly allowed. Off by default: a token in
	// a URL is recorded by proxies, access logs and browser history.
	if c.config.AllowTokenQuery {
		if r.URL.Query().Get("token") == token {
			return true
		}
	}

	return false
}

// readLoop reads messages from a WebSocket connection.
func (c *WebUIChannel) readLoop(pc *webuiConn) {
	defer func() {
		pc.close()
		c.connections.Delete(pc.id)
		c.connCount.Add(-1)
		logger.InfoCF("webui", "WebSocket client disconnected", map[string]any{
			"conn_id":    pc.id,
			"session_id": pc.sessionID,
		})
	}()

	readTimeout := time.Duration(c.config.ReadTimeout) * time.Second
	if readTimeout <= 0 {
		readTimeout = 60 * time.Second
	}

	_ = pc.conn.SetReadDeadline(time.Now().Add(readTimeout))
	pc.conn.SetPongHandler(func(appData string) error {
		_ = pc.conn.SetReadDeadline(time.Now().Add(readTimeout))
		return nil
	})

	// Start ping ticker
	pingInterval := time.Duration(c.config.PingInterval) * time.Second
	if pingInterval <= 0 {
		pingInterval = 30 * time.Second
	}
	go c.pingLoop(pc, pingInterval)

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		_, rawMsg, err := pc.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				logger.DebugCF("webui", "WebSocket read error", map[string]any{
					"conn_id": pc.id,
					"error":   err.Error(),
				})
			}
			return
		}

		_ = pc.conn.SetReadDeadline(time.Now().Add(readTimeout))

		var msg WebUIMessage
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			errMsg := newError("invalid_message", "failed to parse message")
			pc.writeJSON(errMsg)
			continue
		}

		c.handleMessage(pc, msg)
	}
}

// pingLoop sends periodic ping frames to keep the connection alive.
func (c *WebUIChannel) pingLoop(pc *webuiConn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if pc.closed.Load() {
				return
			}
			pc.writeMu.Lock()
			err := pc.conn.WriteMessage(websocket.PingMessage, nil)
			pc.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// handleMessage processes an inbound WebUI channel message.
func (c *WebUIChannel) handleMessage(pc *webuiConn, msg WebUIMessage) {
	switch msg.Type {
	case TypePing:
		pong := newMessage(TypePong, nil)
		pong.ID = msg.ID
		pc.writeJSON(pong)

	case TypeMessageSend:
		c.handleMessageSend(pc, msg)

	default:
		errMsg := newError("unknown_type", fmt.Sprintf("unknown message type: %s", msg.Type))
		pc.writeJSON(errMsg)
	}
}

// handleMessageSend processes an inbound message.send from a client.
func (c *WebUIChannel) handleMessageSend(pc *webuiConn, msg WebUIMessage) {
	content, _ := msg.Payload["content"].(string)
	if strings.TrimSpace(content) == "" {
		errMsg := newError("empty_content", "message content is empty")
		pc.writeJSON(errMsg)
		return
	}

	sessionID := msg.SessionID
	if sessionID == "" {
		sessionID = pc.sessionID
	}

	chatID := "webui:" + sessionID
	senderID := "webui-user"

	peer := bus.Peer{Kind: "direct", ID: "webui:" + sessionID}

	metadata := map[string]string{
		"platform":   "webui",
		"session_id": sessionID,
		"conn_id":    pc.id,
	}

	logger.DebugCF("webui", "Received message", map[string]any{
		"session_id": sessionID,
		"preview":    truncate(content, 50),
	})

	sender := bus.SenderInfo{
		Platform:    "webui",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("webui", senderID),
	}

	if !c.IsAllowedSender(sender) {
		return
	}

	c.HandleMessage(c.ctx, peer, msg.ID, senderID, chatID, content, nil, metadata, sender)
}

// truncate truncates a string to maxLen runes.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
