package discord

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/media"
)

// A Discord API error naming an unknown channel or user, or a user who does
// not accept the bot's messages, is ErrRecipientNotFound; anything else stays
// ErrTemporary.
func TestClassifySendErr(t *testing.T) {
	rest := func(code int) error {
		return &discordgo.RESTError{
			Response:     &http.Response{Status: "400 Bad Request"},
			ResponseBody: []byte(`{"message":"x"}`),
			Message:      &discordgo.APIErrorMessage{Code: code, Message: "x"},
		}
	}
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"unknown channel", rest(discordgo.ErrCodeUnknownChannel), channels.ErrRecipientNotFound},
		{"unknown user", rest(discordgo.ErrCodeUnknownUser), channels.ErrRecipientNotFound},
		{"cannot message user", rest(discordgo.ErrCodeCannotSendMessagesToThisUser), channels.ErrRecipientNotFound},
		{"other API error", rest(discordgo.ErrCodeMissingAccess), channels.ErrTemporary},
		{"no API message", &discordgo.RESTError{Response: &http.Response{Status: "502 Bad Gateway"}}, channels.ErrTemporary},
		{"network error", errors.New("connection reset"), channels.ErrTemporary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifySendErr("discord send", tt.err)
			if !errors.Is(got, tt.want) {
				t.Fatalf("classifySendErr = %v, want %v", got, tt.want)
			}
			// The cause stays in the text that is logged and alerted.
			if !strings.Contains(got.Error(), tt.err.Error()) {
				t.Errorf("classifySendErr = %q, want it to keep %q", got, tt.err)
			}
		})
	}
}

// SendMedia says why Discord refused an attachment: an unknown channel is
// ErrRecipientNotFound.
func TestSendMedia_UnknownChannelIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		if _, err := io.WriteString(w, `{"code":10003,"message":"Unknown Channel"}`); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	orig := discordgo.EndpointChannels
	discordgo.EndpointChannels = srv.URL + "/channels/"
	t.Cleanup(func() { discordgo.EndpointChannels = orig })

	session, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatal(err)
	}
	store := media.NewFileMediaStore()
	path := filepath.Join(t.TempDir(), "a.txt")
	if err = os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{Filename: "a.txt", ContentType: "text/plain"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	c := &DiscordChannel{BaseChannel: channels.NewBaseChannel("discord", nil, nil, nil), session: session}
	c.SetMediaStore(store)
	c.SetRunning(true)

	err = c.SendMedia(context.Background(), bus.OutboundMediaMessage{
		Channel: "discord", ChatID: "123",
		Parts: []bus.MediaPart{{Type: "file", Ref: ref, Filename: "a.txt", ContentType: "text/plain"}},
	})
	if !errors.Is(err, channels.ErrRecipientNotFound) {
		t.Fatalf("SendMedia = %v, want ErrRecipientNotFound", err)
	}
}
