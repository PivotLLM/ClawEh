// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
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
