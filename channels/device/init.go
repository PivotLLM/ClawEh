package device

import (
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

func init() {
	channels.RegisterFactory("device", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		return NewDeviceChannel(cfg.Channels.Device, cfg.DataDir(), cfg.Logging.LogMessageContent, b)
	})
}
