package commands

import (
	"context"
	"fmt"

	"github.com/PivotLLM/ClawEh/app"
)

func versionCommand() Definition {
	return Definition{
		Name:        "version",
		Description: "Show version and copyright information",
		Usage:       "/version",
		Handler: func(_ context.Context, req Request, _ *Runtime) error {
			msg := fmt.Sprintf("%s %s\n%s\n%s",
				app.Name(),
				app.Version(),
				app.TagLine(),
				app.Copyright(),
			)
			return req.Reply(msg)
		},
	}
}
