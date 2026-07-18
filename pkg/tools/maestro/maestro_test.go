// ClawEh
// License: MIT

package maestro

import (
	"context"
	"path/filepath"
	"testing"

	mllm "github.com/PivotLLM/Maestro/llm"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

type fakeRunner struct {
	out     string
	gotTask string
}

func (f *fakeRunner) RunSync(_ context.Context, task, _ string) (string, error) {
	f.gotTask = task
	return f.out, nil
}

func TestProvider_GatingAndDispatch(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{Agents: config.AgentsConfig{List: []config.AgentConfig{
		{ID: "alice", Maestro: true},
		{ID: "bob"}, // maestro off
	}}}
	fr := &fakeRunner{out: "the answer"}

	register := func(agentID string) []global.ToolDefinition {
		return GlobalProvider.RegisterTools(global.Deps{
			Cfg:     cfg,
			AgentID: agentID,
			Host:    tools.ToolDeps{Workspace: filepath.Join(tmp, agentID)},
			Spawn:   fr,
		})
	}

	// Enabled agent gets the whole suite. Maestro is available to sub-agents too
	// (a worker may run its own taskset), so no tool is marked PrimaryOnly —
	// recursion is bounded by MaxSpawnDepth in the Spawner instead.
	defs := register("alice")
	if len(defs) == 0 {
		t.Fatal("alice (maestro on) should get the maestro tool suite")
	}
	for _, d := range defs {
		if d.PrimaryOnly {
			t.Errorf("maestro tool %q must NOT be PrimaryOnly (sub-agents may use maestro)", d.Name)
		}
	}
	// The LLM-management tools are dropped when host-dispatched.
	for _, d := range defs {
		if d.Name == "llm_list" || d.Name == "llm_dispatch" || d.Name == "llm_test" {
			t.Errorf("host-dispatched maestro must not expose %q", d.Name)
		}
	}

	// Disabled agent gets nothing.
	if got := register("bob"); got != nil {
		t.Errorf("bob (maestro off) should get no tools, got %d", len(got))
	}

	// The dispatcher runs the task prompt through the host runner and maps it back.
	d := &dispatcher{run: fr}
	res, err := d.Dispatch(context.Background(), &mllm.DispatchRequest{Prompt: "audit control X"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !res.Success || res.Text != "the answer" {
		t.Errorf("dispatch result = %+v, want success with text 'the answer'", res)
	}
	if fr.gotTask != "audit control X" {
		t.Errorf("runner received task %q, want 'audit control X'", fr.gotTask)
	}
}
