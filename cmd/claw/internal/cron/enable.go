package cron

import (
	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/cmd/claw/internal"
)

func newEnableCommand(storePath func() string) *cobra.Command {
	return &cobra.Command{
		Use:     "enable",
		Short:   "Enable a job",
		Args:    cobra.ExactArgs(1),
		Example: internal.BinaryName + " cron enable 1",
		RunE: func(_ *cobra.Command, args []string) error {
			cronSetJobEnabled(storePath(), args[0], true)
			return nil
		},
	}
}
