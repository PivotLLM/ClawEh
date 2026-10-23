// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/utils"
)

// toAgent is an inbound message from chat c1 addressed to agent id.
func toAgent(id, msgID, content string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel: "test", ChatID: "c1", SenderID: "u1", MessageID: msgID, Content: content,
		Metadata: map[string]string{metadataKeyPreresolvedAgentID: id},
	}
}

// gatedFreshLoop is an owner loop on a gated recording model.
func gatedFreshLoop(t *testing.T) (*AgentLoop, *bus.MessageBus, *recordingProvider) {
	t.Helper()
	msgBus := bus.NewMessageBus()
	model := newGatedRecorder()
	return mustNewAgentLoop(t, ownerTestConfig(t), msgBus, model, nil, OwnsDataDir()), msgBus, model
}

// TestInbound_SingleShotNeverMerges: messages queued behind a single-shot
// agent's running turn each get a blank turn and a reply of their own; for an
// agent that keeps its conversation they are merged into one follow-up turn.
func TestInbound_SingleShotNeverMerges(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	for _, tc := range []struct {
		name      string
		opts      []agentreg.FreshOption
		wantCalls int
	}{
		{"single shot: one turn each", []agentreg.FreshOption{agentreg.SingleShot()}, 3},
		{"without memory: queued messages merged", []agentreg.FreshOption{agentreg.WithoutMemory()}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			al, msgBus, model := gatedFreshLoop(t)
			id, err := al.GetRegistry().CreateFresh(config.AgentConfig{}, tc.opts...)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			go dispatch(al, toAgent(id, "id1", "m1"))
			model.waitStarted(t)
			dispatch(al, toAgent(id, "id2", "m2")) // queued: the session is busy
			dispatch(al, toAgent(id, "id3", "m3"))

			var replies []string
			for i := range tc.wantCalls {
				if i > 0 {
					model.waitStarted(t)
				}
				model.gate <- struct{}{}
				replies = append(replies, nextOutbound(t, msgBus).OriginalMessageID)
			}
			al.activeRequests.Wait()
			if n := model.count(); n != tc.wantCalls {
				t.Fatalf("model calls = %d, want %d", n, tc.wantCalls)
			}
			if tc.wantCalls == 3 {
				if !slices.Equal(replies, []string{"id1", "id2", "id3"}) {
					t.Fatalf("replies to %v, want one to each message", replies)
				}
				for i, want := range []string{"m1", "m2", "m3"} {
					if got := model.call(i).conversation(); !slices.Equal(got, []string{"user: " + want}) {
						t.Fatalf("turn %d sent %v, want only %q", i+1, got, want)
					}
				}
				return
			}
			if got := model.call(1).conversation(); !slices.Contains(got, "user: m2\nm3") {
				t.Fatalf("merged turn sent %v, want m2 and m3 joined", got)
			}
		})
	}
}

// TestSingleShot_InterruptedTurnIsDiscarded: a turn shutdown interrupts is
// not kept for a replay (recovery never replays a temporary agent's turn).
func TestSingleShot_InterruptedTurnIsDiscarded(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, _, model := gatedFreshLoop(t)
	id, err := al.GetRegistry().CreateFresh(config.AgentConfig{}, agentreg.SingleShot())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _ := al.GetRegistry().Get(id)

	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() {
		key := routing.BuildAgentMainSessionKey(id)
		_, err := al.runAgentLoop(ctx, fresh, processOptions{SessionKey: key, Channel: "test", ChatID: "c1", UserMessage: "interrupted"})
		done <- err
	}()
	model.waitStarted(t)
	if files := archiveFiles(t, fresh); len(files) == 0 {
		t.Fatal("fixture: the running turn has no conversation on disk")
	}
	cancel(errShuttingDown)
	if err := <-done; err == nil {
		t.Fatal("the interrupted turn reported success")
	}
	if files := archiveFiles(t, fresh); len(files) != 0 {
		t.Fatalf("an interrupted single-shot turn left %v on disk", files)
	}
	if sessions, _ := memorySessions(al, id); sessions != 0 {
		t.Fatal("an interrupted single-shot turn left its session cached")
	}
}

// TestSingleShot_RetryStartsBlankWithTheMessage: a retried message is the
// turn's only message, never skipped as "already in history".
func TestSingleShot_RetryStartsBlankWithTheMessage(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, model, _ := freshLoop(t)
	id, err := al.GetRegistry().CreateFresh(config.AgentConfig{}, agentreg.SingleShot())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _ := al.GetRegistry().Get(id)
	key := routing.BuildAgentMainSessionKey(id)
	if _, err := fresh.Sessions.AddFullMessage(key, providers.Message{Role: "user", Content: "left over"}); err != nil {
		t.Fatal(err)
	}
	if _, err := al.runAgentLoop(context.Background(), fresh, processOptions{
		SessionKey: key, Channel: "test", ChatID: "c1", UserMessage: "again", IsRetry: true,
	}); err != nil {
		t.Fatalf("retry turn: %v", err)
	}
	if got := model.last(t).conversation(); !slices.Equal(got, []string{"user: again"}) {
		t.Fatalf("retry sent %v, want only the retried message", got)
	}
}

// TestSingleShot_CommandLeavesNothing: a command run on a single-shot agent
// keeps none of the session state it opened.
func TestSingleShot_CommandLeavesNothing(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, _, _ := freshLoop(t)
	id, err := al.GetRegistry().CreateFresh(config.AgentConfig{}, agentreg.SingleShot())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _ := al.GetRegistry().Get(id)
	for _, cmd := range []string{"/compact", "/clear"} {
		if _, err := al.processMessage(context.Background(), toAgent(id, "m-"+cmd, cmd)); err != nil {
			t.Logf("%s: %v", cmd, err)
		}
		if files := archiveFiles(t, fresh); len(files) != 0 {
			t.Fatalf("%s left %v on disk", cmd, files)
		}
		if sessions, _ := memorySessions(al, id); sessions != 0 {
			t.Fatalf("%s left the session cached", cmd)
		}
	}
}

// TestSingleShot_SkippedDiscard: while something else holds the session at
// the end of a turn, its conversation stays; the next turn still starts blank
// and its own end deletes it.
func TestSingleShot_SkippedDiscard(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, model, _ := freshLoop(t)
	id, err := al.GetRegistry().CreateFresh(config.AgentConfig{}, agentreg.SingleShot())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _ := al.GetRegistry().Get(id)
	key := routing.BuildAgentMainSessionKey(id)

	_, _, release := al.getSessionContext(fresh, key) // held across the turn's end
	turn(t, al, id, "first")
	if files := archiveFiles(t, fresh); len(files) == 0 {
		t.Fatal("the conversation was deleted while the session was held")
	}
	release()

	turn(t, al, id, "second")
	if got := model.last(t).conversation(); !slices.Equal(got, []string{"user: second"}) {
		t.Fatalf("turn after a skipped discard sent %v, want only its own message", got)
	}
	if files := archiveFiles(t, fresh); len(files) != 0 {
		t.Fatalf("the next turn's end left %v on disk", files)
	}
}

// TestDiscardConversation_ConcurrentWithSessionUse: discards racing session
// opens and turns never corrupt state (run under -race), and a final discard
// leaves nothing.
func TestDiscardConversation_ConcurrentWithSessionUse(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, model, _ := freshLoop(t)
	id, err := al.GetRegistry().CreateFresh(config.AgentConfig{}, agentreg.SingleShot())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fresh, _ := al.GetRegistry().Get(id)
	key := routing.BuildAgentMainSessionKey(id)

	var wg sync.WaitGroup
	for w := range 4 {
		wg.Go(func() {
			for i := range 25 {
				switch (w + i) % 3 {
				case 0:
					al.discardConversation(context.Background(), fresh, key)
				case 1:
					cm, _, release := al.getSessionContext(fresh, key)
					_ = cm.Stats()
					release()
				default:
					cm, _, release := al.getSessionContext(fresh, key)
					if _, err := cm.AddUserMessage(context.Background(), providers.Message{Role: "user", Content: fmt.Sprint(w, i)}); err != nil &&
						!errors.Is(err, os.ErrNotExist) {
						t.Logf("AddUserMessage: %v", err)
					}
					release()
				}
			}
		})
	}
	wg.Wait()

	al.discardConversation(context.Background(), fresh, key)
	if files := archiveFiles(t, fresh); len(files) != 0 {
		t.Fatalf("a final discard left %v", files)
	}
	turn(t, al, id, "after the storm")
	if got := model.last(t).conversation(); !slices.Equal(got, []string{"user: after the storm"}) {
		t.Fatalf("turn after the stress sent %v", got)
	}
}

// TestFreshAgent_InboundMediaNotCopied: an attachment sent to a fresh agent
// is not copied into its workspace and no "saved in your workspace" note is
// added; the model still gets the attachment ref. A clone of a config agent
// gets the copy and the note.
func TestFreshAgent_InboundMediaNotCopied(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, model, _ := freshLoop(t)
	store := media.NewFileMediaStore()
	al.SetMediaStore(store)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	id, err := al.GetRegistry().CreateFresh(config.AgentConfig{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cloneID, err := al.GetRegistry().CreateClone("main")
	if err != nil {
		t.Fatalf("Create clone: %v", err)
	}
	for _, target := range []string{id, cloneID} {
		ref, err := store.Store(src, media.MediaMeta{Filename: "notes.txt", Source: "test"}, "s")
		if err != nil {
			t.Fatal(err)
		}
		msg := toAgent(target, "m-"+target, "see attached")
		msg.Media = []string{ref}
		if _, err := al.processMessage(context.Background(), msg); err != nil {
			t.Fatalf("processMessage: %v", err)
		}
		agent, _ := al.GetRegistry().Get(target)
		var user string
		for _, m := range model.last(t).messages {
			if m.Role == "user" {
				user = m.Content
			}
		}
		if !strings.Contains(user, ref) {
			t.Fatalf("%s: the model did not get the attachment ref: %q", agent.Label(), user)
		}
		noted := strings.Contains(user, "Received attachment")
		copies, readErr := os.ReadDir(filepath.Join(agent.Workspace, "tmp"))
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		if target == id {
			if noted || len(copies) != 0 {
				t.Fatalf("fresh agent: note %v, copies %d; want neither", noted, len(copies))
			}
			assertEmptyWorkspace(t, agent)
		} else if !noted || len(copies) == 0 {
			t.Fatalf("clone: note %v, copies %d; want both", noted, len(copies))
		}
	}
}

// TestFreshSpec_EmptyPromptIsDefault: a fresh spec without a prompt gets
// DefaultSystemPrompt, never the host prompt.
func TestFreshSpec_EmptyPromptIsDefault(t *testing.T) {
	cfg := newTestConfig(t)
	dir := t.TempDir()
	inst, err := newAgentInstance(agentreg.Spec{
		ID: "f", Config: &config.AgentConfig{ID: "f"}, Origin: agentreg.OriginTemp, Fresh: true,
		Mode: agentreg.ModeNoMemory, Workspace: filepath.Join(dir, "workspace"), StateDir: dir,
	}, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if err != nil {
		t.Fatal(err)
	}
	defer utils.CloseQuietly(inst)
	layers := inst.ContextBuilder.PromptLayers("telegram", "1")
	if len(layers) != 1 || layers[0].Text != agentreg.DefaultSystemPrompt {
		t.Fatalf("layers = %+v, want only the default prompt", layers)
	}
}
