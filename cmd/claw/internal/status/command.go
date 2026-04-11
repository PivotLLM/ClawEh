package status

import (
	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

func NewStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"s"},
		Short:   "Show " + global.AppName + " status",
		Run: func(cmd *cobra.Command, args []string) {
			statusCmd()
		},
	}

	return cmd
}
