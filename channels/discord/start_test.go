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
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read request: %v", err)
		}
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

// Start waits for the gateway session to open only as long as its context:
// with Open blocked, cancelling the context makes Start return its error, and
// a session that opens afterwards is closed.
func TestStart_OpenHonoursContextAndLateSessionIsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"id":"42","username":"bot"}`); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	orig := discordgo.EndpointUsers
	discordgo.EndpointUsers = srv.URL + "/users/"
	t.Cleanup(func() { discordgo.EndpointUsers = orig })

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	opening := make(chan struct{}, 1)
	openResult := make(chan error)
	closed := make(chan struct{}, 1)
	c := &DiscordChannel{
		BaseChannel: channels.NewBaseChannel("discord", config.DiscordConfig{}, nil, []string{"*"}),
		session:     session,
		openSession: func() error {
			opening <- struct{}{}
			return <-openResult
		},
		closeLate: func() error {
			closed <- struct{}{}
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	select {
	case <-opening:
	case <-time.After(5 * time.Second):
		t.Fatal("the session was not opened")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start waited for Open past its context")
	}
	if c.IsRunning() {
		t.Fatal("a cancelled start left the channel running")
	}

	openResult <- nil // the session opens after all
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("a session that opened after its start was abandoned was not closed")
	}
}
