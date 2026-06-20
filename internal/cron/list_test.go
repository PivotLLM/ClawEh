package cron

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PivotLLM/ClawEh/pkg/cron"
)

func TestNewListSubcommand(t *testing.T) {
	fn := func() string { return "" }
	cmd := newListCommand(fn)

	require.NotNil(t, cmd)

	assert.Equal(t, "List all scheduled jobs", cmd.Short)
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what was
// printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

// TestCronListShowsAgent verifies the list output includes the owning agent, and
// "(operator/legacy)" for an unowned job.
func TestCronListShowsAgent(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "jobs.json")
	cs := cron.NewCronService(storePath, nil)

	owned, err := cs.AddJob("Owned", cron.CronSchedule{Kind: "cron", Expr: "0 9 * * *"}, "m", "agent", "slack", "C1", "channel")
	require.NoError(t, err)
	owned.AgentID = "amber"
	require.NoError(t, cs.UpdateJob(owned))

	unowned, err := cs.AddJob("Legacy", cron.CronSchedule{Kind: "cron", Expr: "0 10 * * *"}, "m", "agent", "slack", "C2", "channel")
	require.NoError(t, err)
	// Touch via UpdateJob so it persists without an AgentID.
	require.NoError(t, cs.UpdateJob(unowned))

	out := captureStdout(t, func() { cronListCmd(storePath) })

	assert.Contains(t, out, "Agent:")
	assert.Contains(t, out, "amber")
	assert.Contains(t, out, "(operator/legacy)")
}
