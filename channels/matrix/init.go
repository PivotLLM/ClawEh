package matrix

import (
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

func init() {
	channels.RegisterFactory("matrix", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		return NewMatrixChannel(cfg.Channels.Matrix, b)
	})
	// The channel reads nothing but its own section.
	channels.RegisterFactoryInputs("matrix", func(cfg *config.Config) any { return cfg.Channels.Matrix })
}
