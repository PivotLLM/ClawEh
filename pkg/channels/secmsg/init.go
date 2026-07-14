package secmsg

import (
	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

func init() {
	channels.RegisterSecMsgFactory(func(daemon config.SecMsgConfig, account config.SecMsgAccountConfig, b *bus.MessageBus) (channels.Channel, error) {
		return NewFromConfig(daemon, account, b)
	})
}
