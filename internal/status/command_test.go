package status

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/app"
)

func TestNewStatusCommand(t *testing.T) {
	cmd := NewStatusCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "status", cmd.Use)

	assert.Len(t, cmd.Aliases, 1)
	assert.True(t, cmd.HasAlias("s"))

	assert.Equal(t, "Show "+app.ShortName()+" status", cmd.Short)

	assert.False(t, cmd.HasSubCommands())

	assert.NotNil(t, cmd.Run)
	assert.Nil(t, cmd.RunE)

	assert.Nil(t, cmd.PersistentPreRun)
	assert.Nil(t, cmd.PersistentPostRun)
}
