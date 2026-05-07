package commands

import (
	"context"
	"fmt"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

func versionCommand() Definition {
	return Definition{
		Name:        "version",
		Description: "Show version and copyright information",
		Usage:       "/version",
		Handler: func(_ context.Context, req Request, _ *Runtime) error {
			msg := fmt.Sprintf("%s %s\n%s\n%s",
				global.AppName,
				global.Version,
				global.AppTagLine,
				global.AppCopyright,
			)
			return req.Reply(msg)
		},
	}
}
