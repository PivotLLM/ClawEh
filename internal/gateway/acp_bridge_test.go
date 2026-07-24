package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	acplib "github.com/a3tai/openclaw-go/acp"
	"github.com/a3tai/openclaw-go/protocol"
)

// fakeGateway records chat.send calls and lets a test drive the reply via the
// bridge's event callback, standing in for a live gateway WebSocket client.
type fakeGateway struct {
	mu    sync.Mutex
	sends []protocol.ChatSendParams
	abort []protocol.ChatAbortParams
}

func (f *fakeGateway) ChatSend(_ context.Context, p protocol.ChatSendParams) (*protocol.ChatEvent, error) {
	f.mu.Lock()
	f.sends = append(f.sends, p)
	f.mu.Unlock()
	return &protocol.ChatEvent{RunID: p.IdempotencyKey, State: "started"}, nil
}

func (f *fakeGateway) ChatAbort(_ context.Context, p protocol.ChatAbortParams) error {
	f.mu.Lock()
	f.abort = append(f.abort, p)
	f.mu.Unlock()
	return nil
}

func (f *fakeGateway) lastRunID(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.sends)
		var id string
		if n > 0 {
			id = f.sends[n-1].IdempotencyKey
		}
		f.mu.Unlock()
		if id != "" {
			return id
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no chat.send recorded")
	return ""
}

// captureNotifier records the ACP session/update notifications the bridge emits.
type captureNotifier struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *captureNotifier) SessionUpdate(n acplib.SessionNotification) error {
	data, _ := json.Marshal(n)
	c.mu.Lock()
	c.buf.Write(data)
	c.buf.WriteByte('\n')
	c.mu.Unlock()
	return nil
}

func (c *captureNotifier) chunks(t *testing.T) []string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var texts []string
	for _, line := range strings.Split(strings.TrimSpace(c.buf.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var n acplib.SessionNotification
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			t.Fatalf("unmarshal notification: %v", err)
		}
		if n.Update.SessionUpdate == "agent_message_chunk" && n.Update.Content != nil {
			texts = append(texts, n.Update.Content.Text)
		}
	}
	return texts
}

// agentEvent builds an "agent" gateway event payload.
func agentEvent(runID, stream, text, phase string) protocol.Event {
	data := map[string]any{}
	if text != "" {
		data["delta"] = text
		data["text"] = text
	}
	if phase != "" {
		data["phase"] = phase
	}
	payload, _ := json.Marshal(map[string]any{"runId": runID, "stream": stream, "data": data})
	return protocol.Event{EventName: protocol.EventAgent, Payload: payload}
}

func chatEvent(runID, state string) protocol.Event {
	payload, _ := json.Marshal(map[string]any{"runId": runID, "state": state})
	return protocol.Event{EventName: protocol.EventChat, Payload: payload}
}

func TestACPPromptText(t *testing.T) {
	got := acpPromptText([]acplib.ContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "image", Data: "…"},
		{Type: "text", Text: "world"},
	})
	if got != "hello\nworld" {
		t.Fatalf("acpPromptText = %q, want %q", got, "hello\nworld")
	}
	if acpPromptText(nil) != "" {
		t.Fatalf("acpPromptText(nil) should be empty")
	}
}

func TestACPBridgeInitializeAndNewSession(t *testing.T) {
	br := newACPBridge(&fakeGateway{})
	init, err := br.Initialize(context.Background(), acplib.InitializeRequest{})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if init.ProtocolVersion != acplib.ProtocolVersion || init.AgentInfo == nil || init.AgentInfo.Name == "" {
		t.Fatalf("unexpected initialize response: %+v", init)
	}
	ns, err := br.NewSession(context.Background(), acplib.NewSessionRequest{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if !strings.HasPrefix(ns.SessionID, "acp-") {
		t.Fatalf("sessionId = %q, want acp- prefix", ns.SessionID)
	}
}

// TestACPBridgePromptStreamed streams assistant deltas then completes via the
// agent lifecycle end event.
func TestACPBridgePromptStreamed(t *testing.T) {
	fg := &fakeGateway{}
	note := &captureNotifier{}
	br := newACPBridge(fg)
	br.setNotifier(note)
	const sid = "acp-1"

	done := make(chan *acplib.PromptResponse, 1)
	go func() {
		resp, err := br.Prompt(context.Background(), acplib.PromptRequest{
			SessionID: sid, Prompt: []acplib.ContentBlock{{Type: "text", Text: "hi"}},
		})
		if err != nil {
			t.Errorf("Prompt: %v", err)
		}
		done <- resp
	}()

	runID := fg.lastRunID(t)
	br.handleGatewayEvent(agentEvent(runID, "assistant", "Hello", ""))
	br.handleGatewayEvent(agentEvent(runID, "assistant", " world", ""))
	br.handleGatewayEvent(agentEvent(runID, "lifecycle", "", "end"))

	resp := waitResp(t, done)
	if resp.StopReason != acplib.StopReasonEndTurn {
		t.Fatalf("stopReason = %q, want end_turn", resp.StopReason)
	}
	if texts := note.chunks(t); len(texts) != 2 || texts[0] != "Hello" || texts[1] != " world" {
		t.Fatalf("chunks = %v, want [Hello, ' world']", texts)
	}
	// The prompt must forward to the gateway with the node "main" session.
	if len(fg.sends) != 1 || fg.sends[0].SessionKey != "main" || fg.sends[0].Message != "hi" {
		t.Fatalf("unexpected chat.send: %+v", fg.sends)
	}
}

// TestACPBridgePromptChatFinal completes the turn via the chat "final" event
// (the path node clients like the R1 rely on).
func TestACPBridgePromptChatFinal(t *testing.T) {
	fg := &fakeGateway{}
	note := &captureNotifier{}
	br := newACPBridge(fg)
	br.setNotifier(note)

	done := make(chan *acplib.PromptResponse, 1)
	go func() {
		resp, _ := br.Prompt(context.Background(), acplib.PromptRequest{
			SessionID: "acp-2", Prompt: []acplib.ContentBlock{{Type: "text", Text: "q"}},
		})
		done <- resp
	}()

	runID := fg.lastRunID(t)
	br.handleGatewayEvent(agentEvent(runID, "assistant", "answer", ""))
	br.handleGatewayEvent(chatEvent(runID, "final"))

	resp := waitResp(t, done)
	if resp.StopReason != acplib.StopReasonEndTurn {
		t.Fatalf("stopReason = %q, want end_turn", resp.StopReason)
	}
	if texts := note.chunks(t); len(texts) != 1 || texts[0] != "answer" {
		t.Fatalf("chunks = %v, want [answer]", texts)
	}
}

// TestACPBridgeStrayEventIgnored ensures events for an unknown run are dropped
// (no panic, no spurious notifications).
func TestACPBridgeStrayEventIgnored(t *testing.T) {
	br := newACPBridge(&fakeGateway{})
	note := &captureNotifier{}
	br.setNotifier(note)
	br.handleGatewayEvent(agentEvent("nope", "assistant", "x", ""))
	if got := note.chunks(t); len(got) != 0 {
		t.Fatalf("expected no chunks for stray event, got %v", got)
	}
}

func TestACPBridgePromptEmptyText(t *testing.T) {
	br := newACPBridge(&fakeGateway{})
	resp, err := br.Prompt(context.Background(), acplib.PromptRequest{
		SessionID: "acp-3", Prompt: []acplib.ContentBlock{{Type: "image", Data: "x"}},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if resp.StopReason != acplib.StopReasonEndTurn {
		t.Fatalf("stopReason = %q, want end_turn", resp.StopReason)
	}
}

func waitResp(t *testing.T, done <-chan *acplib.PromptResponse) *acplib.PromptResponse {
	t.Helper()
	select {
	case resp := <-done:
		return resp
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not return")
		return nil
	}
}
