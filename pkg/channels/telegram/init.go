package telegram

import (
	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

func init() {
	channels.RegisterTelegramBotFactory(func(botCfg config.TelegramBotConfig, b *bus.MessageBus) (channels.Channel, error) {
		return NewTelegramChannelFromConfig(botCfg, b)
	})
}
