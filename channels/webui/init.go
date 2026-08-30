package webui

import (
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

func init() {
	channels.RegisterFactory("webui", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		return NewWebUIChannel(cfg.Channels.WebUI, b)
	})
}
