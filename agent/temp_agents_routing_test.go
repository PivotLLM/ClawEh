// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
)

// A late async result is addressed when the callback is built: a clone's to
// its source's main conversation, every other agent's to its own.
func TestAsyncResultTarget(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al := newTestAgentLoop(t).al
	reg := al.GetRegistry()
	cloneID, err := reg.Create(config.AgentConfig{}, agentreg.CloneOf("main"))
	if err != nil {
		t.Fatalf("Create clone: %v", err)
	}
	freshID, err := reg.Create(config.AgentConfig{})
	if err != nil {
		t.Fatalf("Create fresh: %v", err)
	}
	for id, want := range map[string]string{"main": "main", cloneID: "main", freshID: freshID} {
		a, _ := reg.Get(id)
		gotAgent, gotSession := asyncResultTarget(a)
		if gotAgent != want || gotSession != routing.BuildAgentMainSessionKey(want) {
			t.Errorf("%s: result goes to %q / %q, want %q's main conversation", a.Label(), gotAgent, gotSession, want)
		}
	}
}

// A message addressed to a temporary agent that no longer exists — a
// session_clear handoff, a late async result — is dropped: it never resets or
// reaches another agent, and nothing is sent to the channel.
func TestMessageForDeletedTempAgentIsDropped(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	tl := newTestAgentLoop(t)
	al := tl.al
	reg := al.GetRegistry()
	main, _ := reg.Get("main")
	mainKey := routing.BuildAgentMainSessionKey("main")
	if err := main.Sessions.AddMessage(mainKey, "user", "keep me"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	cloneID, err := reg.Create(config.AgentConfig{}, agentreg.CloneOf("main"))
	if err != nil {
		t.Fatalf("Create clone: %v", err)
	}
	freshID, err := reg.Create(config.AgentConfig{})
	if err != nil {
		t.Fatalf("Create fresh: %v", err)
	}
	for _, id := range []string{cloneID, freshID} {
		if err := reg.Delete(id); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}

	for _, id := range []string{cloneID, freshID} {
		for name, msg := range map[string]bus.InboundMessage{
			"session_clear": {
				Channel: "telegram", ChatID: "1", SenderID: "system",
				SessionKey: routing.BuildAgentMainSessionKey(id), Content: "cleared",
				Metadata: map[string]string{metaSessionReset: "true", metadataKeyPreresolvedAgentID: id},
			},
			"async result": {
				Channel: "system", ChatID: "1", SenderID: "async:web_fetch", Content: "late result",
				SessionKey: routing.BuildAgentMainSessionKey(id),
				Metadata:   map[string]string{metadataKeyPreresolvedAgentID: id, bus.MetaOriginChannel: "telegram"},
			},
		} {
			if _, err := al.processMessage(context.Background(), msg); !errors.Is(err, errAgentGone) {
				t.Errorf("%s for a deleted agent: err = %v, want errAgentGone", name, err)
			}
			al.runTurn(context.Background(), context.Background(), msg)
		}
	}

	history := main.Sessions.GetHistory(mainKey)
	if len(history) != 1 || history[0].Content != "keep me" {
		t.Fatalf("main's conversation changed: %+v", history)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if out, ok := tl.msgBus.SubscribeOutbound(ctx); ok {
		t.Fatalf("a dropped message produced a reply: %+v", out)
	}
}

// A sender that requires a reply from an agent that no longer exists gets one
// plain sentence naming the agent, not the internal error.
func TestMessageForDeletedTempAgent_ReplyRequiredIsPlain(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	tl := newTestAgentLoop(t)
	reg := tl.al.GetRegistry()
	id, err := reg.Create(config.AgentConfig{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := reg.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	msg := bus.InboundMessage{
		Channel: "telegram", ChatID: "1", SenderID: "u1", Content: "hello",
		SessionKey: routing.BuildAgentMainSessionKey(id),
		Metadata:   map[string]string{metadataKeyPreresolvedAgentID: id, bus.MetaReplyRequired: "1"},
	}
	tl.al.runTurn(context.Background(), context.Background(), msg)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, ok := tl.msgBus.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("no reply to a sender that required one")
	}
	if want := "Agent " + id + " no longer exists."; out.Content != want || out.Outcome != bus.OutcomeError {
		t.Fatalf("reply = %q (%s), want %q (error)", out.Content, out.Outcome, want)
	}
}

// A clone's tools follow its source's current allowlist: a tool removed from
// the source is gone from an existing clone after the reload.
func TestReload_CloneFollowsSourceAllowlist(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	cfg := newTestConfig(t)
	cfg.Agents.List[0].Tools = []string{"*"}
	provider := &mockProvider{}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), provider, nil)
	reg := al.GetRegistry()
	cloneID, err := reg.Create(config.AgentConfig{}, agentreg.CloneOf("main"))
	if err != nil {
		t.Fatalf("Create clone: %v", err)
	}
	clone, _ := reg.Get(cloneID)
	if _, ok := clone.Tools.Get("file_write"); !ok {
		t.Fatalf("clone lacks file_write before the change (has %v)", clone.Tools.List())
	}

	next := newTestConfig(t)
	next.Agents.BaseDir = cfg.Agents.BaseDir
	next.Agents.List[0].Tools = []string{"file_read"}
	if err := al.ReloadProviderAndConfig(context.Background(), provider, next); err != nil {
		t.Fatalf("reload: %v", err)
	}
	clone, ok := reg.Get(cloneID)
	if !ok {
		t.Fatal("the clone did not survive the reload")
	}
	if _, has := clone.Tools.Get("file_write"); has {
		t.Fatal("a tool removed from the source is still on its clone")
	}
	if !slices.Contains(clone.Config.Tools, "file_read") || clone.Config.ID != cloneID {
		t.Fatalf("clone config = %+v, want the source's current one under the clone's id", clone.Config)
	}
}

// A reload whose context ends before it can commit returns at once and leaves
// the agents as they were.
func TestReload_HonoursContext(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	tl := newTestAgentLoop(t)
	al := tl.al
	before, _ := al.GetRegistry().Get("main")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := al.ReloadProviderAndConfig(ctx, tl.provider, newTestConfig(t))
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("reload with an ended context = %v, want a cancellation error", err)
	}
	if after, _ := al.GetRegistry().Get("main"); after != before || al.GetConfig() != tl.cfg {
		t.Fatal("an abandoned reload changed the agents or the config")
	}
}
