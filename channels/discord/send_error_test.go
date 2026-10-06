package discord

import (
	"errors"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/PivotLLM/ClawEh/channels"
)

// A Discord API error naming an unknown channel or user, or a user who does
// not accept the bot's messages, is ErrRecipientNotFound; anything else stays
// ErrTemporary.
func TestClassifySendErr(t *testing.T) {
	rest := func(code int) error {
		return &discordgo.RESTError{Message: &discordgo.APIErrorMessage{Code: code, Message: "x"}}
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
		{"no API message", &discordgo.RESTError{}, channels.ErrTemporary},
		{"network error", errors.New("connection reset"), channels.ErrTemporary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifySendErr("discord send", tt.err); !errors.Is(got, tt.want) {
				t.Fatalf("classifySendErr = %v, want %v", got, tt.want)
			}
		})
	}
}
