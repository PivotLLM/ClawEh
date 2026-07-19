package commands

import (
	"context"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

func toolsCommand() Definition {
	return Definition{
		Name:        "tools",
		Description: "Show or toggle whether tool-call activity is shown to you",
		Usage:       "/tools [on|off]",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.GetShowToolActivity == nil || rt.SetShowToolActivity == nil {
				return req.Reply(unavailableMsg)
			}

			arg := nthToken(req.Text, 1)
			if arg == "" {
				return req.Reply("Tool activity is currently " + onOff(rt.GetShowToolActivity()) +
					".\n\nUse: /tools [on | off]")
			}

			on, err := global.YesNo(arg)
			if err != nil {
				return req.Reply("Usage: /tools [on|off]")
			}
			rt.SetShowToolActivity(on)
			if on {
				return req.Reply("Tool activity is now ON — I'll show a one-line note for each tool I use.")
			}
			return req.Reply("Tool activity is now OFF — I'll work quietly.")
		},
	}
}
