package webui

import (
	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

func init() {
	channels.RegisterFactory("webui", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		return NewWebUIChannel(cfg.Channels.WebUI, b)
	})
}
