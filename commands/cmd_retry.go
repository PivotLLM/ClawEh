package commands

import "context"

func retryCommand() Definition {
	return Definition{
		Name:        "retry",
		Description: "Clear provider cooldowns and resend your last message",
		Usage:       "/retry",
		Handler: func(ctx context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.ResetCooldown == nil || rt.RetriggerLastMessage == nil {
				return req.Reply(unavailableMsg)
			}
			rt.ResetCooldown()
			if err := rt.RetriggerLastMessage(ctx); err != nil {
				return req.Reply("Cooldowns cleared, but could not retrigger: " + err.Error())
			}
			return nil
		},
	}
}
