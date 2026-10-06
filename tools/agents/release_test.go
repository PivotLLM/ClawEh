package agents

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

// TestSpawnCallback_ReleasesAfterDelivery: a background worker is released
// only after its completion has been delivered, and also when it failed.
func TestSpawnCallback_ReleasesAfterDelivery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runErr error
	}{
		{"success", nil},
		{"failure", errors.New("worker failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu    sync.Mutex
				order []string
			)
			released := make(chan struct{})
			mgr := NewSubagentManager(SubagentManagerConfig{
				Workspace:     t.TempDir(),
				Live:          NewLiveSet(),
				CallerAgentID: "alice",
				RunFull: func(_ context.Context, _, _, _ string, _ []string) (*global.SyncResult, func(), error) {
					release := func() {
						mu.Lock()
						order = append(order, "released")
						mu.Unlock()
						close(released)
					}
					if tc.runErr != nil {
						return nil, release, tc.runErr
					}
					return &global.SyncResult{Content: "ok", Iterations: 1}, release, nil
				},
			})
			cb := func(context.Context, *tools.ToolResult) {
				mu.Lock()
				order = append(order, "delivered")
				mu.Unlock()
			}
			if _, err := mgr.SpawnCallback("t", "n", "", "cli", "direct", "", nil, cb, 0); err != nil {
				t.Fatalf("SpawnCallback: %v", err)
			}
			select {
			case <-released:
			case <-time.After(3 * time.Second):
				t.Fatal("worker was never released")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(order) != 2 || order[0] != "delivered" || order[1] != "released" {
				t.Errorf("order = %v, want delivered then released", order)
			}
		})
	}
}

// TestRunSync_ReleasesWorker: a synchronous run releases its worker once the
// result is in hand, on success and on failure.
func TestRunSync_ReleasesWorker(t *testing.T) {
	for _, runErr := range []error{nil, errors.New("worker failed")} {
		released := 0
		mgr := NewSubagentManager(SubagentManagerConfig{
			Workspace: t.TempDir(),
			Live:      NewLiveSet(),
			RunFull: func(_ context.Context, _, _, _ string, _ []string) (*global.SyncResult, func(), error) {
				if runErr != nil {
					return nil, func() { released++ }, runErr
				}
				return &global.SyncResult{Content: "ok"}, func() { released++ }, nil
			},
		})
		res, err := mgr.RunSync(context.Background(), "task", "", "")
		if (err != nil) != (runErr != nil) || (runErr == nil && res.Content != "ok") {
			t.Fatalf("RunSync = %+v, %v; want the worker's outcome", res, err)
		}
		if released != 1 {
			t.Errorf("err=%v: worker released %d times, want 1", runErr, released)
		}
	}
}
