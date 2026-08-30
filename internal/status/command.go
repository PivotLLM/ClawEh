package status

import (
	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/app"
)

func NewStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"s"},
		Short:   "Show " + app.Name() + " status",
		Run: func(cmd *cobra.Command, args []string) {
			statusCmd()
		},
	}

	return cmd
}
