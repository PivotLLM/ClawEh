package secmsg

import (
	"github.com/PivotLLM/ClawEh/channels"
)

func init() {
	channels.RegisterSecMsgFactory(NewFromConfig)
	channels.RegisterSecMsgDiscovery(DiscoverAccounts)
}
