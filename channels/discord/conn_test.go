package discord

import (
	"testing"

	"github.com/PivotLLM/ClawEh/channels"
)

// A gateway disconnect starts an outage and the next connect ends it.
func TestConnectionEventsFeedOutageTracker(t *testing.T) {
	c := &DiscordChannel{BaseChannel: channels.NewBaseChannel("discord", nil, nil, nil)}

	c.onDisconnect(nil, nil)
	if c.ConnDownSince().IsZero() {
		t.Fatal("disconnect must start an outage")
	}
	c.onConnect(nil, nil)
	if !c.ConnDownSince().IsZero() {
		t.Fatal("connect must end the outage")
	}
}
