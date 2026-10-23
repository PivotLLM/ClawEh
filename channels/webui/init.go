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
	// The channel reads nothing but its own section.
	channels.RegisterFactoryInputs("webui", func(cfg *config.Config) any { return cfg.Channels.WebUI })
}
