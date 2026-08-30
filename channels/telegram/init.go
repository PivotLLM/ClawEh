package telegram

import (
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

func init() {
	channels.RegisterTelegramBotFactory(func(botCfg config.TelegramBotConfig, b *bus.MessageBus) (channels.Channel, error) {
		return NewTelegramChannelFromConfig(botCfg, b)
	})
}
