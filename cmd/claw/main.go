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

	"github.com/PivotLLM/ClawEh/cmd/claw/internal"
	"github.com/PivotLLM/ClawEh/cmd/claw/internal/agent"
	"github.com/PivotLLM/ClawEh/cmd/claw/internal/auth"
	"github.com/PivotLLM/ClawEh/cmd/claw/internal/cron"
	"github.com/PivotLLM/ClawEh/cmd/claw/internal/gateway"
	"github.com/PivotLLM/ClawEh/cmd/claw/internal/test"
	"github.com/PivotLLM/ClawEh/cmd/claw/internal/model"
	"github.com/PivotLLM/ClawEh/cmd/claw/internal/onboard"
	"github.com/PivotLLM/ClawEh/cmd/claw/internal/skills"
	"github.com/PivotLLM/ClawEh/cmd/claw/internal/status"
	"github.com/PivotLLM/ClawEh/cmd/claw/internal/version"
	"github.com/PivotLLM/ClawEh/pkg/global"
)

func NewPicoclawCommand(binaryName string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   binaryName,
		Short: global.AppTagLine,
	}

	cmd.AddCommand(
		onboard.NewOnboardCommand(),
		agent.NewAgentCommand(),
		auth.NewAuthCommand(),
		gateway.NewGatewayCommand(),
		status.NewStatusCommand(),
		cron.NewCronCommand(),
		skills.NewSkillsCommand(),
		model.NewModelCommand(),
		test.NewTestCommand(),
		version.NewVersionCommand(),
	)

	return cmd
}

func main() {
	binaryName := filepath.Base(os.Args[0])
	internal.BinaryName = binaryName
	cmd := NewPicoclawCommand(binaryName)
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
