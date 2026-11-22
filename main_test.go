package main

import (
	"os"
	"runtime"
	"slices"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/app"
)

func TestNewClawCommand(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{"/usr/local/bin/alice-claw"}

	cmd := NewClawCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "alice-claw", cmd.Use, "the root command is named after the binary as started")
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
	// `webui` is registered on macOS only.
	if runtime.GOOS == "darwin" {
		allowedCommands = append(allowedCommands, "webui")
	}

	subcommands := cmd.Commands()
	assert.Len(t, subcommands, len(allowedCommands))

	for _, subcmd := range subcommands {
		found := slices.Contains(allowedCommands, subcmd.Name())
		assert.True(t, found, "unexpected subcommand %q", subcmd.Name())

		assert.False(t, subcmd.Hidden)
	}
	hasWebUI := slices.ContainsFunc(subcommands, func(c *cobra.Command) bool { return c.Name() == "webui" })
	assert.Equal(t, runtime.GOOS == "darwin", hasWebUI, "webui is registered on macOS and nowhere else")
}

func TestUpdateAlias(t *testing.T) {
	cmd := NewClawCommand()
	subcmd, _, err := cmd.Find([]string{"update"})
	require.NoError(t, err)
	require.NotNil(t, subcmd)
	assert.Equal(t, "upgrade", subcmd.Name())
}
