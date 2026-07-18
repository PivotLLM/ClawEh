// ClawEh
// License: MIT

package maestro

import (
	"context"
	"testing"

	mllm "github.com/PivotLLM/Maestro/llm"

	toolsagents "github.com/PivotLLM/ClawEh/pkg/tools/agents"
)

// depthCapturingRunner records the sub-agent depth carried by the context it is
// dispatched with.
type depthCapturingRunner struct{ gotDepth int }

func (r *depthCapturingRunner) RunSync(ctx context.Context, _, _ string) (string, error) {
	r.gotDepth = toolsagents.SpawnDepth(ctx)
	return "ok", nil
}

// TestDispatcher_PropagatesDepth verifies the Maestro host dispatcher forwards
// the caller's context (carrying sub-agent depth) to the SyncRunner, so a
// Maestro worker's dispatch is bounded by MaxSpawnDepth rather than resetting to
// depth 0. This closes the last link of the ctx chain: Maestro runner → Dispatch
// → RunSync.
func TestDispatcher_PropagatesDepth(t *testing.T) {
	runner := &depthCapturingRunner{}
	d := &dispatcher{run: runner}

	ctx := toolsagents.WithSpawnDepth(context.Background(), 2)
	if _, err := d.Dispatch(ctx, &mllm.DispatchRequest{Prompt: "do it"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if runner.gotDepth != 2 {
		t.Fatalf("dispatcher dropped depth: SyncRunner saw %d, want 2", runner.gotDepth)
	}
}
