package line

import (
	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

func init() {
	channels.RegisterFactory("line", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		return NewLINEChannel(cfg.Channels.LINE, b)
	})
}
