package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	acplib "github.com/a3tai/openclaw-go/acp"
	"github.com/a3tai/openclaw-go/protocol"

	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// gatewaySender is the slice of the OpenClaw gateway client the bridge needs. It
// is an interface so the ACP-translation logic can be unit-tested without a live
// WebSocket connection.
type gatewaySender interface {
	ChatSend(ctx context.Context, params protocol.ChatSendParams) (*protocol.ChatEvent, error)
	ChatAbort(ctx context.Context, params protocol.ChatAbortParams) error
}

// acpNotifier is the slice of the ACP server the bridge needs to push streamed
// updates back to the ACP client (satisfied by *acplib.Server).
type acpNotifier interface {
	SessionUpdate(notification acplib.SessionNotification) error
}

// bridgeRun tracks one in-flight ACP session/prompt → gateway chat.send turn.
// It correlates the gateway's async chat/agent events (delivered on the client's
// event goroutine, keyed by runId) back to the blocking Prompt call.
type bridgeRun struct {
	sessionID string // the ACP sessionId to address session/update at
	mu        sync.Mutex
	done      chan struct{}
	closed    bool
	stop      string // resolved stopReason ("" until finished)
}

func (r *bridgeRun) finish(stop string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.stop = stop
		r.closed = true
		close(r.done)
	}
}

// acpBridge implements acplib.Handler by forwarding prompts to a running gateway
// over its WebSocket protocol (localhost), then translating the streamed chat /
// agent events back into ACP session/update notifications. It holds NO agent
// loop — the single running gateway does the work.
type acpBridge struct {
	client   gatewaySender
	notifier acpNotifier

	mu       sync.Mutex
	sessions map[string]struct{}   // known ACP sessionIds
	runs     map[string]*bridgeRun // keyed by runId (idempotencyKey)
}

func newACPBridge(client gatewaySender) *acpBridge {
	return &acpBridge{
		client:   client,
		sessions: make(map[string]struct{}),
		runs:     make(map[string]*bridgeRun),
	}
}

// setNotifier wires the ACP server used to emit session/update notifications.
func (br *acpBridge) setNotifier(n acpNotifier) { br.notifier = n }

func (br *acpBridge) Initialize(_ context.Context, _ acplib.InitializeRequest) (*acplib.InitializeResponse, error) {
	return &acplib.InitializeResponse{
		ProtocolVersion: acplib.ProtocolVersion,
		AgentInfo:       &acplib.Implementation{Name: strings.ToLower(global.AppName), Version: global.Version},
		AgentCapabilities: &acplib.AgentCapabilities{
			LoadSession:        false,
			PromptCapabilities: &acplib.PromptCapabilities{},
		},
		// No ACP-layer auth: the stdio pipe is the trust boundary. The bridge
		// authenticates to the gateway itself (device token + pairing).
		AuthMethods: []acplib.AuthMethod{},
	}, nil
}

func (br *acpBridge) Authenticate(context.Context, acplib.AuthenticateRequest) (*acplib.AuthenticateResponse, error) {
	return &acplib.AuthenticateResponse{}, nil
}

func (br *acpBridge) NewSession(_ context.Context, _ acplib.NewSessionRequest) (*acplib.NewSessionResponse, error) {
	sid := "acp-" + randomHex(8)
	br.mu.Lock()
	br.sessions[sid] = struct{}{}
	br.mu.Unlock()
	return &acplib.NewSessionResponse{SessionID: sid}, nil
}

// Prompt forwards the turn to the gateway and blocks until the gateway signals
// the turn is complete (agent lifecycle end or chat final), streaming assistant
// text back as ACP agent_message_chunk notifications in the meantime.
func (br *acpBridge) Prompt(ctx context.Context, req acplib.PromptRequest) (*acplib.PromptResponse, error) {
	sessionID := req.SessionID
	if sessionID == "" {
		return nil, errors.New("session/prompt: missing sessionId")
	}
	br.mu.Lock()
	br.sessions[sessionID] = struct{}{}
	br.mu.Unlock()

	text := acpPromptText(req.Prompt)
	// Always log what the ACP client actually sent, so voice/other non-text prompts
	// are visible (they'd otherwise vanish here in this text-only v1).
	logger.InfoCF("acp", "prompt received", map[string]any{
		"sessionId": sessionID,
		"blocks":    summarizeBlocks(req.Prompt),
		"textChars": len(text),
	})
	if strings.TrimSpace(text) == "" {
		logger.WarnCF("acp", "prompt has no text content — nothing forwarded (v1 forwards text blocks only)", map[string]any{
			"sessionId": sessionID,
			"blocks":    summarizeBlocks(req.Prompt),
		})
		return &acplib.PromptResponse{StopReason: acplib.StopReasonEndTurn}, nil
	}

	runID := randomHex(12)
	run := &bridgeRun{sessionID: sessionID, done: make(chan struct{})}
	br.mu.Lock()
	br.runs[runID] = run
	br.mu.Unlock()
	defer func() {
		br.mu.Lock()
		delete(br.runs, runID)
		br.mu.Unlock()
	}()

	logger.InfoCF("acp", "prompt → gateway", map[string]any{"sessionId": sessionID, "runId": runID, "chars": len(text)})

	// The gateway isolates a node client's conversation per device; sessionKey
	// "main" is what the R1 sends. The bridge is one device, so all ACP sessions
	// share that per-device conversation (matches the R1's own behavior today).
	ack, err := br.client.ChatSend(ctx, protocol.ChatSendParams{
		SessionKey:     "main",
		Message:        text,
		IdempotencyKey: runID,
	})
	if err != nil {
		logger.ErrorCF("acp", "gateway chat.send failed", map[string]any{"runId": runID, "error": err.Error()})
		return nil, fmt.Errorf("gateway chat.send: %w", err)
	}
	// The ack's runId MUST equal our idempotency key, else the reply events (keyed
	// by runId) will never correlate back to this prompt.
	if ack != nil {
		logger.InfoCF("acp", "chat.send ack", map[string]any{"runId": runID, "ackRunId": ack.RunID, "state": ack.State, "match": ack.RunID == runID})
	}

	select {
	case <-run.done:
		stop := run.stop
		if stop == "" {
			stop = acplib.StopReasonEndTurn
		}
		return &acplib.PromptResponse{StopReason: stop}, nil
	case <-ctx.Done():
		return &acplib.PromptResponse{StopReason: acplib.StopReasonCancelled}, ctx.Err()
	}
}

// Cancel aborts the gateway run. NOTE: the ACP library dispatches requests
// serially, so a session/cancel sent mid-turn is only read after Prompt returns;
// mid-turn abort is therefore not effective in v1 (documented).
func (br *acpBridge) Cancel(ctx context.Context, req acplib.CancelNotification) {
	br.mu.Lock()
	runs := make([]string, 0, len(br.runs))
	for id, r := range br.runs {
		if r.sessionID == req.SessionID {
			runs = append(runs, id)
		}
	}
	br.mu.Unlock()
	for _, id := range runs {
		_ = br.client.ChatAbort(ctx, protocol.ChatAbortParams{RunID: id})
		if r, ok := br.runByID(id); ok {
			r.finish(acplib.StopReasonCancelled)
		}
	}
}

func (br *acpBridge) runByID(runID string) (*bridgeRun, bool) {
	br.mu.Lock()
	r, ok := br.runs[runID]
	br.mu.Unlock()
	return r, ok
}

// handleGatewayEvent translates one gateway chat/agent event into ACP
// notifications and turn-completion. It is the client's WithOnEvent callback.
func (br *acpBridge) handleGatewayEvent(ev protocol.Event) {
	switch ev.EventName {
	case protocol.EventAgent:
		br.handleAgentEvent(ev.Payload)
	case protocol.EventChat:
		br.handleChatEvent(ev.Payload)
	case protocol.EventTick, protocol.EventHeartbeat, protocol.EventPresence:
		// High-frequency keepalive/no-op events — ignore quietly.
	default:
		// Log any other event so we can see what the gateway actually delivers to
		// the bridge connection (diagnosing the reply path).
		logger.DebugCF("acp", "gateway event (unhandled)", map[string]any{"event": string(ev.EventName)})
	}
}

// agentEventPayload is the subset of the "agent" event we consume: streamed
// assistant text increments and the lifecycle end that completes the turn.
type agentEventPayload struct {
	RunID  string `json:"runId"`
	Stream string `json:"stream"`
	Data   struct {
		Text  string `json:"text"`
		Delta string `json:"delta"`
		Phase string `json:"phase"`
	} `json:"data"`
}

func (br *acpBridge) handleAgentEvent(payload json.RawMessage) {
	var p agentEventPayload
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	run, ok := br.runByID(p.RunID)
	if !ok {
		// Reveals a runId that doesn't match any in-flight prompt (correlation bug),
		// or events for an already-finished run.
		logger.WarnCF("acp", "agent event for unknown run", map[string]any{
			"runId": p.RunID, "stream": p.Stream, "phase": p.Data.Phase,
		})
		return
	}
	switch p.Stream {
	case "assistant":
		// data.delta is the increment; fall back to data.text if delta is absent.
		text := p.Data.Delta
		if text == "" {
			text = p.Data.Text
		}
		logger.DebugCF("acp", "agent assistant delta", map[string]any{"runId": p.RunID, "chars": len(text)})
		if text != "" && br.notifier != nil {
			_ = br.notifier.SessionUpdate(acplib.SessionNotification{
				SessionID: run.sessionID,
				Update: acplib.SessionUpdate{
					SessionUpdate: "agent_message_chunk",
					Content:       &acplib.ContentBlock{Type: "text", Text: text},
				},
			})
		}
	case "lifecycle":
		if p.Data.Phase == "end" {
			logger.InfoCF("acp", "turn complete (agent lifecycle end)", map[string]any{"runId": p.RunID})
			run.finish(acplib.StopReasonEndTurn)
		}
	}
}

// chatEventPayload is the subset of the "chat" event we consume: the terminal
// "final" state (and "error") complete the turn. Delta text is delivered via the
// "agent" assistant stream, so chat deltas are ignored to avoid double text.
type chatEventPayload struct {
	RunID string `json:"runId"`
	State string `json:"state"`
}

func (br *acpBridge) handleChatEvent(payload json.RawMessage) {
	var p chatEventPayload
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	run, ok := br.runByID(p.RunID)
	if !ok {
		logger.DebugCF("acp", "chat event for unknown run", map[string]any{"runId": p.RunID, "state": p.State})
		return
	}
	switch p.State {
	case "final":
		logger.InfoCF("acp", "turn complete (chat final)", map[string]any{"runId": p.RunID})
		run.finish(acplib.StopReasonEndTurn)
	case "error":
		logger.WarnCF("acp", "turn errored (chat error)", map[string]any{"runId": p.RunID})
		run.finish(acplib.StopReasonRefusal)
	}
}

// --- Unsupported / no-op session management (v1) -------------------------------

// The session-management probes below must return benign results, never errors:
// clients (rabbit-agent) call session/list right after session/new and abort the
// whole turn if it errors. Our sessions are effectively stateless on the bridge
// (the gateway owns the per-device conversation), so we just track ids.

func (br *acpBridge) LoadSession(_ context.Context, req acplib.LoadSessionRequest) (*acplib.LoadSessionResponse, error) {
	br.rememberSession(req.SessionID)
	return &acplib.LoadSessionResponse{}, nil
}

func (br *acpBridge) ListSessions(context.Context, acplib.ListSessionsRequest) (*acplib.ListSessionsResponse, error) {
	// No prior sessions to advertise — each connection uses the session from
	// session/new. Returning an error here made rabbit-agent abort before prompting.
	return &acplib.ListSessionsResponse{Sessions: []acplib.SessionInfo{}}, nil
}

func (br *acpBridge) ForkSession(_ context.Context, _ acplib.ForkSessionRequest) (*acplib.ForkSessionResponse, error) {
	sid := "acp-" + randomHex(8)
	br.rememberSession(sid)
	return &acplib.ForkSessionResponse{SessionID: sid}, nil
}

func (br *acpBridge) ResumeSession(_ context.Context, req acplib.ResumeSessionRequest) (*acplib.ResumeSessionResponse, error) {
	br.rememberSession(req.SessionID)
	return &acplib.ResumeSessionResponse{}, nil
}

func (br *acpBridge) rememberSession(id string) {
	if id == "" {
		return
	}
	br.mu.Lock()
	br.sessions[id] = struct{}{}
	br.mu.Unlock()
}

func (br *acpBridge) SetSessionMode(context.Context, acplib.SetSessionModeRequest) (*acplib.SetSessionModeResponse, error) {
	return &acplib.SetSessionModeResponse{}, nil
}

func (br *acpBridge) SetSessionModel(context.Context, acplib.SetSessionModelRequest) (*acplib.SetSessionModelResponse, error) {
	return &acplib.SetSessionModelResponse{}, nil
}

func (br *acpBridge) SetSessionConfigOption(context.Context, acplib.SetSessionConfigOptionRequest) (*acplib.SetSessionConfigOptionResponse, error) {
	return &acplib.SetSessionConfigOptionResponse{ConfigOptions: []acplib.SessionConfigOption{}}, nil
}

// acpPromptText concatenates the text content blocks of a prompt (v1 handles text
// only; image/audio/resource blocks are ignored).
func acpPromptText(blocks []acplib.ContentBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" && blk.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// summarizeBlocks renders a compact description of a prompt's content blocks for
// logging: the type of each block plus a size hint (chars for text, bytes for
// image/audio). Used to see exactly what an ACP client (e.g. rabbit-agent for a
// voice message) sends, without logging the payload itself.
func summarizeBlocks(blocks []acplib.ContentBlock) []string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, fmt.Sprintf("text(%dc)", len(b.Text)))
		case "image", "audio":
			out = append(out, fmt.Sprintf("%s(%s,%db)", b.Type, b.MimeType, len(b.Data)))
		case "":
			out = append(out, "empty")
		default:
			out = append(out, b.Type)
		}
	}
	return out
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
