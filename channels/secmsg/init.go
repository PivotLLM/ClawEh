package secmsg

import (
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
)

func init() {
	channels.RegisterSecMsgFactory(func(daemon config.SecMsgConfig, account config.SecMsgAccountConfig, b *bus.MessageBus) (channels.Channel, error) {
		return NewFromConfig(daemon, account, b)
	})
	channels.RegisterSecMsgDiscovery(DiscoverAccounts)
}
