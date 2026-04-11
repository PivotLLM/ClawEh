package skills

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/cmd/claw/internal"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/skills"
)

type deps struct {
	workspace    string
	installer    *skills.SkillInstaller
	skillsLoader *skills.SkillsLoader
}

func NewSkillsCommand() *cobra.Command {
	var d deps

	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage skills",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := internal.LoadConfig()
			if err != nil {
				return fmt.Errorf("error loading config: %w", err)
			}

			d.workspace = cfg.WorkspacePath()
			installer, err := skills.NewSkillInstaller(
				cfg.DataDir(),
				cfg.Tools.Skills.Github.Token,
				cfg.Tools.Skills.Github.Proxy,
			)
			if err != nil {
				return fmt.Errorf("error creating skills installer: %w", err)
			}
			d.installer = installer

			// get builtin skills directory
			builtinSkillsDir := filepath.Join(cfg.DataDir(), "claw", "skills")
			d.skillsLoader = skills.NewSkillsLoader(d.workspace, cfg.SkillsPath(), builtinSkillsDir)

			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	installerFn := func() (*skills.SkillInstaller, error) {
		if d.installer == nil {
			return nil, fmt.Errorf("skills installer is not initialized")
		}
		return d.installer, nil
	}

	loaderFn := func() (*skills.SkillsLoader, error) {
		if d.skillsLoader == nil {
			return nil, fmt.Errorf("skills loader is not initialized")
		}
		return d.skillsLoader, nil
	}

	cfgFn := func() (*config.Config, error) {
		return internal.LoadConfig()
	}

	cmd.AddCommand(
		newListCommand(loaderFn),
		newInstallCommand(installerFn),
		newInstallBuiltinCommand(cfgFn),
		newListBuiltinCommand(),
		newRemoveCommand(installerFn),
		newSearchCommand(),
		newShowCommand(loaderFn),
	)

	return cmd
}
