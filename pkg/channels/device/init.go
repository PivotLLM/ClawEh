package device

import (
	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

func init() {
	channels.RegisterFactory("device", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		return NewDeviceChannel(cfg.Channels.Device, cfg.DataDir(), b)
	})
}
