package main

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/app"
)

func TestNewClawCommand(t *testing.T) {
	cmd := NewClawCommand("claw")

	require.NotNil(t, cmd)

	assert.Equal(t, "claw", cmd.Use)
	assert.Equal(t, app.TagLine(), cmd.Short)

	assert.True(t, cmd.HasSubCommands())
	assert.True(t, cmd.HasAvailableSubCommands())

	// The merged binary attaches the gateway flags (-d / -T) on the root so
	// `claw` (no subcommand) launches the server.
	assert.True(t, cmd.HasFlags())

	assert.Nil(t, cmd.Run)
	// `claw` with no args launches the gateway, so RunE must be set.
	assert.NotNil(t, cmd.RunE)

	assert.Nil(t, cmd.PersistentPreRun)
	assert.Nil(t, cmd.PersistentPostRun)

	allowedCommands := []string{
		"acp",
		"admin",
		"agent",
		"backup",
		"cron",
		"devices",
		"gateway",
		"install",
		"memory",
		"test",
		"model",
		"network",
		"restore",
		"sessions",
		"skills",
		"status",
		"tls",
		"token",
		"uninstall",
		"upgrade",
		"version",
	}

	subcommands := cmd.Commands()
	assert.Len(t, subcommands, len(allowedCommands))

	for _, subcmd := range subcommands {
		found := slices.Contains(allowedCommands, subcmd.Name())
		assert.True(t, found, "unexpected subcommand %q", subcmd.Name())

		assert.False(t, subcmd.Hidden)
	}
}

func TestUpdateAlias(t *testing.T) {
	cmd := NewClawCommand("claw")
	subcmd, _, err := cmd.Find([]string{"update"})
	require.NoError(t, err)
	require.NotNil(t, subcmd)
	assert.Equal(t, "upgrade", subcmd.Name())
}
