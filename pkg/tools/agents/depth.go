// ClawEh - sub-agent recursion depth
// License: MIT

package agents

import "context"

// MaxSpawnDepth bounds sub-agent recursion. A primary (top-level) agent turn is
// depth 0; the sub-agents it spawns run at depth 1, theirs at depth 2, and so
// on. A spawn is refused once the spawning agent is already AT MaxSpawnDepth.
//
// This replaces the old blanket PrimaryOnly restriction (effectively "depth <=
// 1"): sub-agents now inherit the parent's full toolset and may themselves
// spawn / re-enter Maestro, but only to a bounded depth, so a runaway
// spawn→spawn→… (or maestro→maestro→…) chain cannot occur.
const MaxSpawnDepth = 3

type spawnDepthKey struct{}

// WithSpawnDepth returns a context carrying the running agent's sub-agent depth.
func WithSpawnDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, spawnDepthKey{}, depth)
}

// SpawnDepth reports the running agent's sub-agent depth from ctx (0 when unset,
// i.e. a primary turn).
func SpawnDepth(ctx context.Context) int {
	if d, ok := ctx.Value(spawnDepthKey{}).(int); ok {
		return d
	}
	return 0
}
