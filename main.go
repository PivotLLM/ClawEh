// ClawEh - Personal AI Assistant
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors
// Copyright (c) 2026 Tenebris Technologies Inc.

package main

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/internal/agent"
	"github.com/PivotLLM/ClawEh/internal/cron"
	"github.com/PivotLLM/ClawEh/internal/devicegw"
	"github.com/PivotLLM/ClawEh/internal/gateway"
	"github.com/PivotLLM/ClawEh/internal/install"
	"github.com/PivotLLM/ClawEh/internal/memory"
	"github.com/PivotLLM/ClawEh/internal/model"
	"github.com/PivotLLM/ClawEh/internal/skills"
	"github.com/PivotLLM/ClawEh/internal/status"
	"github.com/PivotLLM/ClawEh/internal/test"
	"github.com/PivotLLM/ClawEh/internal/token"
	"github.com/PivotLLM/ClawEh/internal/version"
	"github.com/PivotLLM/ClawEh/pkg/global"
)

func NewClawCommand(binaryName string) *cobra.Command {
	// Default subcommand: run the merged gateway + WebUI + session API on the
	// single port from cfg.Gateway. `claw` with no arguments is the supported
	// way to launch the server; the `claw gateway` alias is preserved for
	// existing systemd units and muscle memory during the transition.
	defaultCmd := gateway.NewGatewayCommand()
	cmd := &cobra.Command{
		Use:          binaryName,
		Short:        global.AppTagLine,
		Args:         defaultCmd.Args,
		SilenceUsage: true,
		PreRunE:      defaultCmd.PreRunE,
		RunE:         defaultCmd.RunE,
	}
	cmd.Flags().AddFlagSet(defaultCmd.Flags())

	cmd.AddCommand(
		agent.NewAgentCommand(),
		defaultCmd,
		status.NewStatusCommand(),
		cron.NewCronCommand(),
		skills.NewSkillsCommand(),
		memory.NewMemoryCommand(),
		model.NewModelCommand(),
		install.NewInstallCommand(),
		install.NewUninstallCommand(),
		test.NewTestCommand(),
		token.NewTokenCommand(),
		devicegw.NewDevicesCommand(),
		version.NewVersionCommand(),
	)

	return cmd
}

func main() {
	binaryName := filepath.Base(os.Args[0])
	internal.BinaryName = binaryName
	cmd := NewClawCommand(binaryName)
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
