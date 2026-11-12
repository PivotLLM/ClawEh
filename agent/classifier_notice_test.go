// ClawEh
// License: MIT

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
)

const classifierNoticeText = "Classifier inbox: the run failed: source mail_list: boom. " +
	"Check and fix the classifier's definition or its instructions files; classifier_test shows the result."

// aliceDefaultChat binds Alice's default chat to telegram/u1.
func aliceDefaultChat(cfg *config.Config) {
	cfg.Bindings = []config.AgentBinding{{
		AgentID: "alice", Default: true,
		Match: config.BindingMatch{Channel: "telegram", Peer: &config.PeerMatch{Kind: "direct", ID: "u1"}},
	}}
}

// The notice of a failed run is an internal system message for the agent's
// main conversation, naming its default chat as the chat the turn answers;
// with no default chat it names the ask channel (kept in the conversation).
func TestClassifierHost_RunFailedMessage(t *testing.T) {
	for _, tc := range []struct {
		name        string
		binding     bool
		wantChannel string
		wantChatID  string
	}{
		{"default chat", true, "telegram", "u1"},
		{"no default chat", false, constants.AgentMessageChannel, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := classifierHostConfig(t, "http://127.0.0.1:0")
			if tc.binding {
				aliceDefaultChat(cfg)
			}
			_, host, msgBus := newClassifierHostLoop(t, cfg)
			if err := host.RunFailed(context.Background(), "alice", classifierNoticeText); err != nil {
				t.Fatalf("RunFailed: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			msg, ok := msgBus.ConsumeInbound(ctx)
			if !ok {
				t.Fatal("no notice queued")
			}
			if msg.Channel != "system" || msg.SenderID != "classifier" || !msg.Internal || msg.Content != classifierNoticeText ||
				msg.ChatID != tc.wantChatID || msg.Metadata[bus.MetaOriginChannel] != tc.wantChannel ||
				msg.Metadata[metadataKeyPreresolvedAgentID] != "alice" || msg.SessionKey != routing.BuildAgentMainSessionKey("alice") {
				t.Errorf("notice = %+v", msg)
			}
			noOutbound(t, msgBus) // nothing is posted by the host itself
		})
	}
}

// Through a running loop: Alice's turn sees the notice, and its answer goes
// to her default chat; with none it is kept in the conversation.
func TestClassifierHost_RunFailedTurn(t *testing.T) {
	for _, binding := range []bool{true, false} {
		t.Run(map[bool]string{true: "default chat", false: "no default chat"}[binding], func(t *testing.T) {
			t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
			cfg := forumConfig(t, false, false)
			if binding {
				aliceDefaultChat(cfg)
			}
			model := &forumModel{}
			r := newForumRig(t, cfg, model)
			host := NewClassifierHost()
			host.Bind(r.al)
			if err := host.RunFailed(context.Background(), "alice", classifierNoticeText); err != nil {
				t.Fatalf("RunFailed: %v", err)
			}
			eventually(t, "the notice in Alice's conversation", func() bool {
				return model.sawUser("[System: classifier] " + classifierNoticeText)
			})
			if binding {
				if out := chatOutbound(t, r.bus); out.Channel != "telegram" || out.ChatID != "u1" {
					t.Fatalf("answer posted to %s/%s, want telegram/u1", out.Channel, out.ChatID)
				}
			}
			r.al.activeRequests.Wait() // the notice's turn has ended
			noChatOutbound(t, r.bus)
		})
	}
}
