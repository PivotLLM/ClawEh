package cron

import (
	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/cmd/claw/internal"
)

func newDisableCommand(storePath func() string) *cobra.Command {
	return &cobra.Command{
		Use:     "disable",
		Short:   "Disable a job",
		Args:    cobra.ExactArgs(1),
		Example: internal.BinaryName + " cron disable 1",
		RunE: func(_ *cobra.Command, args []string) error {
			cronSetJobEnabled(storePath(), args[0], false)
			return nil
		},
	}
}
