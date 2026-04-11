package onboard

import (
	"embed"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

//go:generate cp -r ../../../../workspace .
//go:embed workspace
var embeddedFiles embed.FS

func NewOnboardCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "onboard",
		Aliases: []string{"o"},
		Short:   "Initialize " + global.AppName + " configuration and workspace",
		Run: func(cmd *cobra.Command, args []string) {
			onboard()
		},
	}

	return cmd
}
