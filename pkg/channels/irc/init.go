package irc

import (
	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

func init() {
	channels.RegisterFactory("irc", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		if !cfg.Channels.IRC.Enabled {
			return nil, nil
		}
		return NewIRCChannel(cfg.Channels.IRC, b)
	})
}
