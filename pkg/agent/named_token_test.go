// ClawEh
// License: MIT

package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// setDefaultBinding gives the agent a concrete default-channel binding so
// CronTarget (and therefore HandleExternalMessage) resolves a delivery target.
func setDefaultBinding(cfg *config.Config, agentID, channel, peerKind, peerID string) {
	cfg.Bindings = []config.AgentBinding{
		{
			AgentID: agentID,
			Default: true,
			Match: config.BindingMatch{
				Channel: channel,
				Peer:    &config.PeerMatch{Kind: peerKind, ID: peerID},
			},
		},
	}
}

// TestValidateMessageToken_NamedAndRotating verifies a named token resolves to its
// agent AND that the rotating-token path is still consulted (both mechanisms live
// side by side).
func TestValidateMessageToken_NamedAndRotating(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	tok, err := al.CreateMessageToken("main", "webhook")
	if err != nil {
		t.Fatalf("CreateMessageToken: %v", err)
	}

	agentID, ok := al.ValidateMessageToken(tok.Token)
	if !ok || agentID != "main" {
		t.Fatalf("ValidateMessageToken(named) = (%q,%v), want (main,true)", agentID, ok)
	}

	if got := al.ListMessageTokens("main"); len(got) != 1 || got[0].ID != tok.ID {
		t.Fatalf("ListMessageTokens = %+v, want the one created token", got)
	}
	if !al.DeleteMessageToken("main", tok.ID) {
		t.Fatal("DeleteMessageToken = false, want true")
	}
	if _, ok := al.ValidateMessageToken(tok.Token); ok {
		t.Fatal("revoked named token still validates")
	}
}

func TestHandleExternalMessage_DeliversToCronTarget(t *testing.T) {
	al, cfg, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	setDefaultBinding(cfg, "main", "telegram", "direct", "chat-42")

	body := "temp alarm: 85C"
	if err := al.HandleExternalMessage(context.Background(), "main", body); err != nil {
		t.Fatalf("HandleExternalMessage: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, ok := msgBus.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("no inbound message published")
	}

	// Delivery mirrors cron: resolved via CronTarget, with a fixed webhook SenderID.
	if msg.Channel != "telegram" {
		t.Errorf("Channel = %q, want telegram", msg.Channel)
	}
	if msg.ChatID != "chat-42" {
		t.Errorf("ChatID = %q, want chat-42", msg.ChatID)
	}
	if msg.SenderID != "webhook" {
		t.Errorf("SenderID = %q, want webhook", msg.SenderID)
	}
	if msg.Peer.Kind != "direct" || msg.Peer.ID != "chat-42" {
		t.Errorf("Peer = %+v, want {direct chat-42}", msg.Peer)
	}
	// Raw body is preserved (wrapped with the security prefix, not mangled).
	if !strings.Contains(msg.Content, body) {
		t.Errorf("Content %q does not contain raw body %q", msg.Content, body)
	}
}

func TestHandleExternalMessage_NoDefaultChannel(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	// No bindings configured → CronTarget !ok → error naming the agent.
	err := al.HandleExternalMessage(context.Background(), "main", "hello")
	if err == nil {
		t.Fatal("expected error when agent has no default channel")
	}
	if !strings.Contains(err.Error(), "no default channel") {
		t.Errorf("error = %q, want it to mention 'no default channel'", err.Error())
	}
}
