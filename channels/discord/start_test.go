package discord

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

// Start honours its context: a bot-user lookup Discord never answers ends
// when the context is cancelled, and Start returns the context's error.
func TestStart_BotUserLookupHonoursContext(t *testing.T) {
	asked := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case asked <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	orig := discordgo.EndpointUsers
	discordgo.EndpointUsers = srv.URL + "/users/"
	t.Cleanup(func() { discordgo.EndpointUsers = orig })

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	c := &DiscordChannel{
		BaseChannel: channels.NewBaseChannel("discord", config.DiscordConfig{}, nil, []string{"*"}),
		session:     session,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the bot user was not looked up")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return once its context was cancelled")
	}
	if c.IsRunning() {
		t.Fatal("a cancelled start left the channel running")
	}
}
