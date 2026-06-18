package commands

import (
	"context"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

func reasoningCommand() Definition {
	return Definition{
		Name:        "reasoning",
		Description: "Show or toggle whether the model's reasoning is delivered to you",
		Usage:       "/reasoning [on|off]",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.GetExposeReasoning == nil || rt.SetExposeReasoning == nil {
				return req.Reply(unavailableMsg)
			}

			arg := nthToken(req.Text, 1)
			if arg == "" {
				return req.Reply("Reasoning is currently " + onOff(rt.GetExposeReasoning()) +
					".\n\nUse /reasoning on to show the model's thinking, or /reasoning off to hide it.")
			}

			on, err := global.YesNo(arg)
			if err != nil {
				return req.Reply("Usage: /reasoning [on|off]")
			}
			rt.SetExposeReasoning(on)
			if on {
				return req.Reply("Reasoning is now ON — I'll share my thinking for this conversation.")
			}
			return req.Reply("Reasoning is now OFF — I'll keep my thinking to myself.")
		},
	}
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}
