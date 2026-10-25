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

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/voice"
)

// stubTranscriber returns text, or err when set.
type stubTranscriber struct {
	text string
	err  error
}

func (s stubTranscriber) Name() string { return "stub" }

func (s stubTranscriber) Transcribe(context.Context, string) (*voice.TranscriptionResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &voice.TranscriptionResponse{Text: s.text}, nil
}

// voiceMessage stores a voice note in store and returns a message to the main
// agent carrying it, as the Telegram channel builds one.
func voiceMessage(t *testing.T, store media.MediaStore) bus.InboundMessage {
	t.Helper()
	src := filepath.Join(t.TempDir(), "voice.ogg")
	if err := os.WriteFile(src, []byte("OggS"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(src, media.MediaMeta{Filename: "voice.ogg", ContentType: "audio/ogg", Source: "test"}, "s")
	if err != nil {
		t.Fatal(err)
	}
	msg := toAgent("main", "m1", "[voice]")
	msg.Media = []string{ref}
	return msg
}

// TestPrepareInbound_Voice: a transcribed voice message reaches the turn as
// its transcript; one whose transcription failed reaches it as a plain note
// that it could not be transcribed, never as an empty voice message, and the
// reason is logged. Either way the audio is not passed on as an attachment.
func TestPrepareInbound_Voice(t *testing.T) {
	tests := []struct {
		name        string
		transcriber stubTranscriber
		want        string
		wantLog     string
	}{
		{name: "transcribed", transcriber: stubTranscriber{text: "hello there"}, want: "[voice: hello there]"},
		{
			name:        "failed",
			transcriber: stubTranscriber{err: errors.New("upstream 503")},
			want:        untranscribedVoiceNote,
			wantLog:     "upstream 503",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &safeBufLoop{}
			restore := logger.RedirectForTest(logs)
			defer restore()
			al, _, _ := freshLoop(t)
			store := media.NewFileMediaStore()
			al.SetMediaStore(store)
			al.SetTranscriber(tt.transcriber)

			_, got := al.prepareInbound(context.Background(), voiceMessage(t, store))
			if got.Content != tt.want {
				t.Errorf("content = %q, want %q", got.Content, tt.want)
			}
			if len(got.Media) != 0 {
				t.Errorf("media = %v, want none", got.Media)
			}
			if tt.wantLog != "" && !strings.Contains(logs.String(), tt.wantLog) {
				t.Errorf("log does not carry the reason %q:\n%s", tt.wantLog, logs.String())
			}
		})
	}
}

// TestPrepareInbound_AlertsWhenVoiceNotTranscribed: a voice message that no
// backend could transcribe raises an urgent alert keyed by the channel and
// carrying the error.
func TestPrepareInbound_AlertsWhenVoiceNotTranscribed(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, _, _ := freshLoop(t)
	store := media.NewFileMediaStore()
	al.SetMediaStore(store)
	al.SetTranscriber(stubTranscriber{err: errors.New("upstream 503")})
	rec := &alertRecorder{}
	al.SetAlerter(rec)

	al.prepareInbound(context.Background(), voiceMessage(t, store))

	if len(rec.alerts) != 1 {
		t.Fatalf("want one alert, got %+v", rec.alerts)
	}
	a := rec.alerts[0]
	if a.Priority != alerter.Urgent || a.Title != "Voice message not transcribed" ||
		a.EventID != "voice:failed:test" || a.Details != "upstream 503" ||
		a.Description != "test: every speech backend failed; the agent received the audio untranscribed" {
		t.Fatalf("unexpected alert %+v", a)
	}
}

// TestProcessMessage_UntranscribedVoiceReachesModel: the model is told that
// a voice message arrived and could not be transcribed.
func TestProcessMessage_UntranscribedVoiceReachesModel(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, model, _ := freshLoop(t)
	store := media.NewFileMediaStore()
	al.SetMediaStore(store)
	al.SetTranscriber(stubTranscriber{err: errors.New("upstream 503")})

	if _, err := al.processMessage(context.Background(), voiceMessage(t, store)); err != nil {
		t.Fatalf("processMessage: %v", err)
	}
	var user string
	for _, m := range model.last(t).messages {
		if m.Role == "user" {
			user = m.Content
		}
	}
	if !strings.Contains(user, untranscribedVoiceNote) {
		t.Fatalf("model got %q, want it to contain %q", user, untranscribedVoiceNote)
	}
}

func TestTranscriptionFeedback(t *testing.T) {
	alice := &AgentInstance{ID: "alice", Name: "Alice"}

	tests := []struct {
		name   string
		target *AgentInstance
		in     []voiceTranscript
		want   string
	}{
		{name: "heard", target: alice, in: []voiceTranscript{{text: "hi"}}, want: "Transcript: hi"},
		{name: "silence", target: alice, in: []voiceTranscript{{}}, want: "No voice detected in the audio"},
		{
			name: "failed", target: alice, in: []voiceTranscript{{failed: true}},
			want: "Alice couldn't understand your voice message.",
		},
		{
			name: "failed, agent unknown", in: []voiceTranscript{{failed: true}},
			want: "The assistant couldn't understand your voice message.",
		},
		{
			name: "mixed", target: alice, in: []voiceTranscript{{text: "hi"}, {failed: true}},
			want: "Transcript: hi\nAlice couldn't understand your voice message.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := transcriptionFeedback(tt.target, tt.in); got != tt.want {
				t.Errorf("transcriptionFeedback = %q, want %q", got, tt.want)
			}
		})
	}
}

// echoChannel is a running channel that records what it is sent.
type echoChannel struct {
	mu   sync.Mutex
	sent []bus.OutboundMessage
}

func (*echoChannel) Name() string                        { return "test" }
func (*echoChannel) Start(context.Context) error         { return nil }
func (*echoChannel) Stop(context.Context) error          { return nil }
func (*echoChannel) IsRunning() bool                     { return true }
func (*echoChannel) IsAllowed(string) bool               { return true }
func (*echoChannel) IsAllowedSender(bus.SenderInfo) bool { return true }
func (*echoChannel) ReasoningChannelID() string          { return "" }

func (c *echoChannel) Send(_ context.Context, msg bus.OutboundMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, msg)
	return nil
}

func (c *echoChannel) messages() []bus.OutboundMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.sent)
}

// withEchoChannel gives al a started channel manager whose only channel,
// "test", records what it is sent.
func withEchoChannel(t *testing.T, al *AgentLoop, msgBus *bus.MessageBus) *echoChannel {
	t.Helper()
	cm, err := channels.NewManager(&config.Config{}, msgBus, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ch := &echoChannel{}
	cm.RegisterChannel("test", ch)
	if err := cm.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll: %v", err)
	}
	t.Cleanup(func() {
		if err := cm.StopAll(context.Background()); err != nil {
			t.Errorf("StopAll: %v", err)
		}
	})
	al.SetChannelManager(cm)
	return ch
}

// TestPrepareInbound_EchoTranscription: with voice.echo_transcription on, the
// sender is told what was heard, and a voice message that could not be
// understood is answered with a sentence naming the agent, as a reply to it;
// with it off, nothing is sent.
func TestPrepareInbound_EchoTranscription(t *testing.T) {
	for _, echo := range []bool{true, false} {
		t.Run(fmt.Sprintf("echo %v", echo), func(t *testing.T) {
			restore := logger.RedirectForTest(&safeBufLoop{})
			defer restore()
			cfg := ownerTestConfig(t)
			cfg.Agents.List = []config.AgentConfig{{ID: "alice", Name: "Alice", Default: true}}
			cfg.Voice.EchoTranscription = echo
			msgBus := bus.NewMessageBus()
			al := mustNewAgentLoop(t, cfg, msgBus, &recordingProvider{}, nil, OwnsDataDir())
			store := media.NewFileMediaStore()
			al.SetMediaStore(store)
			al.SetTranscriber(stubTranscriber{err: errors.New("upstream 503")})
			ch := withEchoChannel(t, al, msgBus)

			msg := voiceMessage(t, store)
			msg.Metadata[metadataKeyPreresolvedAgentID] = "alice"
			al.prepareInbound(context.Background(), msg)

			sent := ch.messages()
			if !echo {
				if len(sent) != 0 {
					t.Fatalf("echo off: sent %+v, want nothing", sent)
				}
				return
			}
			if len(sent) != 1 {
				t.Fatalf("echo on: sent %+v, want one echo", sent)
			}
			want := "Alice couldn't understand your voice message."
			if got := sent[0]; got.Content != want || got.ChatID != "c1" || got.ReplyToMessageID != "m1" {
				t.Fatalf("echo = %+v, want %q in reply to m1 in chat c1", got, want)
			}
		})
	}
}

// failTranscriber fails the test when anything is transcribed.
type failTranscriber struct{ t *testing.T }

func (failTranscriber) Name() string { return "fail" }

func (f failTranscriber) Transcribe(context.Context, string) (*voice.TranscriptionResponse, error) {
	f.t.Error("a voice message to a human agent was transcribed")
	return &voice.TranscriptionResponse{Text: "heard"}, nil
}

// TestPrepareInbound_HumanAgentNeverTranscribed: no transcription model
// hears a request to a person or the person's own voice note in their chat;
// the message keeps its audio and its text as written.
func TestPrepareInbound_HumanAgentNeverTranscribed(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	cfg := humanLoopConfig(t, 60)
	cfg.Voice.EchoTranscription = true
	al, _, model := newHumanLoopWith(t, cfg)
	store := media.NewFileMediaStore()
	al.SetMediaStore(store)
	al.SetTranscriber(failTranscriber{t: t})

	ref := voiceMessage(t, store).Media
	ask := askBob("a1", "[voice]")
	ask.Media = ref
	fromPerson := fromBob("b1", "[voice]")
	fromPerson.Media = ref

	for name, msg := range map[string]bus.InboundMessage{"ask to Bob": ask, "Bob in his chat": fromPerson} {
		t.Run(name, func(t *testing.T) {
			if target := al.inboundTarget(msg); target == nil || target.ID != "bob" {
				t.Fatalf("message is not addressed to Bob: %v", target)
			}
			_, got := al.prepareInbound(context.Background(), msg)
			if got.Content != msg.Content {
				t.Errorf("content = %q, want %q", got.Content, msg.Content)
			}
			if !slices.Equal(got.Media, ref) {
				t.Errorf("media = %v, want %v", got.Media, ref)
			}
		})
	}
	if n := model.count(); n != 0 {
		t.Fatalf("a model was called %d times", n)
	}
}
