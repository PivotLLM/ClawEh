package skills

import (
	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/cmd/claw/internal"
)

func newListBuiltinCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list-builtin",
		Short:   "List available builtin skills",
		Example: internal.BinaryName + " skills list-builtin",
		Run: func(_ *cobra.Command, _ []string) {
			skillsListBuiltinCmd()
		},
	}

	return cmd
}
