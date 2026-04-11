package skills

import (
	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/cmd/claw/internal"
	"github.com/PivotLLM/ClawEh/pkg/config"
)

func newInstallBuiltinCommand(cfgFn func() (*config.Config, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "install-builtin",
		Short:   "Install all builtin skills to workspace",
		Example: internal.BinaryName + " skills install-builtin",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := cfgFn()
			if err != nil {
				return err
			}
			skillsInstallBuiltinCmd(cfg)
			return nil
		},
	}

	return cmd
}
