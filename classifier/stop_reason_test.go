// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"testing"
)

// A run stopped while an item is in flight records why: the classifier's own
// stop, or the service shutting down. A shutdown also closes the run's stop
// channel, so with both ready the shutdown must win every time.
func TestRunStopReason(t *testing.T) {
	for _, tc := range []struct {
		name     string
		shutdown bool
		want     string
	}{
		{"classifier stopped", false, "the classifier was stopped"},
		{"service shut down", true, "the service stopped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			def := definition()
			def["limits"] = map[string]any{"parallel": 1}
			e.create(def)

			entered := make(chan struct{}, 3)
			release := make(chan struct{})
			e.model.setReply(func(string, string) (string, error) {
				entered <- struct{}{}
				<-release
				return "urgent", nil
			})
			d, err := e.svc.validDefinition(e.scope, "inbox-priority")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop := make(chan struct{})
			done := make(chan RunRecord, 1)
			go func() { done <- e.svc.runClassifier(ctx, e.scope, d, stop) }()

			<-entered // the first item is being classified
			close(stop)
			if tc.shutdown {
				cancel()
			}
			close(release)
			rec := <-done
			if rec.Status != RunStopped || rec.Error != tc.want {
				t.Errorf("run = %+v, want stopped with %q", rec, tc.want)
			}
		})
	}
}
