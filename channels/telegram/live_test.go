package telegram

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

// Against the real Bot API (opt-in: CLAW_TEST_TELEGRAM=1 and a bot token in
// CLAW_TEST_TELEGRAM_TOKEN), a send to a chat that cannot exist is reported
// as not found, with Telegram's reason kept and the token left out. Nothing
// is delivered to anyone.
func TestLive_SendToMissingChat(t *testing.T) {
	if os.Getenv("CLAW_TEST_TELEGRAM") != "1" {
		t.Skip("set CLAW_TEST_TELEGRAM=1 and CLAW_TEST_TELEGRAM_TOKEN to run against the real Telegram API")
	}
	token := os.Getenv("CLAW_TEST_TELEGRAM_TOKEN")
	if token == "" {
		t.Skip("CLAW_TEST_TELEGRAM_TOKEN is not set")
	}
	secret := token
	if _, after, ok := strings.Cut(token, ":"); ok {
		secret = after
	}

	ch, err := NewTelegramChannelFromConfig(config.TelegramBotConfig{Token: token}, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("channel: %v", err) // telego's errors do not carry the token
	}
	ch.SetRunning(true)
	t.Cleanup(func() { ch.SetRunning(false) })

	// Chat 1 is reserved; a supergroup id this far out has never been issued.
	missing := []string{
		"1",
		strconv.FormatInt(-1009_000_000_000_000-rand.Int64N(1_000_000_000), 10),
	}
	for _, chatID := range missing {
		t.Run(chatID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := ch.Send(ctx, bus.OutboundMessage{Channel: "telegram", ChatID: chatID, Content: "ClawEh delivery test"})
			if err == nil {
				t.Fatal("a send to a chat that cannot exist succeeded")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("the error carries the bot token")
			}
			if !errors.Is(err, channels.ErrRecipientNotFound) {
				t.Fatalf("Send = %v, want ErrRecipientNotFound", err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "chat not found") {
				t.Fatalf("Send = %v, want Telegram's reason kept", err)
			}
			t.Logf("Send = %v", err)
		})
	}
}
