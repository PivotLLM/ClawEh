// ClawEh
// License: MIT

package agents

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

// TestSpawn_CarriesRemoteOrigin: a sub-agent started from a turn that began
// with a remote chat runs remote, in wait and callback mode alike, and its
// task record keeps the mark for a relaunch; a local turn's stays local.
func TestSpawn_CarriesRemoteOrigin(t *testing.T) {
	for _, mode := range []global.SpawnMode{global.SpawnAndWait, global.SpawnCallback} {
		for _, remote := range []bool{false, true} {
			got := make(chan bool, 1)
			mgr := NewSubagentManager(SubagentManagerConfig{
				Workspace:     t.TempDir(),
				Live:          NewLiveSet(),
				CallerAgentID: "alice",
				RunFull: func(ctx context.Context, _, _, _ string, _ []string) (*global.SyncResult, func(), error) {
					got <- tools.RemoteOrigin(ctx)
					return &global.SyncResult{Content: "done"}, func() {}, nil
				},
			})
			ctx := context.Background()
			if remote {
				ctx = tools.WithRemoteOrigin(ctx)
			}
			res, err := NewSpawner(mgr).Spawn(ctx, global.SpawnRequest{Mode: mode, Task: "work", Name: "job"})
			if err != nil || res.IsError {
				t.Fatalf("mode %v: Spawn = %+v, %v", mode, res, err)
			}
			select {
			case r := <-got:
				if r != remote {
					t.Fatalf("mode %v: worker remote = %v, want %v", mode, r, remote)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("mode %v: worker did not run", mode)
			}
		}
	}
}
