// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// svcReplier is a Messenger that answers every ask with a short text.
type svcReplier struct {
	mu   sync.Mutex
	asks map[string]int
}

func (m *svcReplier) Ask(_ context.Context, agentID, _ string, _ time.Duration) (Reply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asks[agentID]++
	return Reply{Text: "Hello from " + agentID + ".", Outcome: OutcomeOK}, nil
}

func TestSvcRealControllerRunsToCompletion(t *testing.T) {
	e := svcSetup(t)
	messenger := &svcReplier{asks: map[string]int{}}
	svc := New(Host{Messenger: messenger, Agents: e.agents, Notifier: e.notifier, Logger: e.logger, Schemas: JSONSchemaValidator{}})
	t.Cleanup(func() { svcClose(t, svc) })

	id, err := svcLaunch(t, svc, svcSimpleJSON, e.opts())
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	svcEventually(t, "completion notice", func() bool { return e.notifier.count() == 1 })
	if p := e.notifier.problems(); len(p) != 0 {
		t.Errorf("notice order: %v", p)
	}
	res, err := svc.Results(t.Context(), e.scope, id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusCompleted || !res.Complete || len(res.Layers) != 1 || len(res.Layers[0].Outputs) != 2 {
		t.Errorf("result = %+v", res)
	}
	if len(e.agents.deletedIDs()) != 1 {
		t.Errorf("deleted %v, want the fresh participant", e.agents.deletedIDs())
	}
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	if messenger.asks["alice"] != 1 {
		t.Errorf("asks = %v", messenger.asks)
	}
}

func TestSvcRealControllerPausesAndCancels(t *testing.T) {
	e := svcSetup(t)
	svc := New(Host{Messenger: &svcReplier{asks: map[string]int{}}, Agents: e.agents, Notifier: e.notifier, Logger: e.logger, Schemas: JSONSchemaValidator{}})
	t.Cleanup(func() { svcClose(t, svc) })

	id, err := svcLaunch(t, svc, svcSimpleJSON, e.opts())
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	// The forum may already be finished; a pause is then refused.
	if err := svc.Pause(t.Context(), e.scope, id); err != nil && !errors.Is(err, ErrInvalidState) {
		t.Fatalf("pause: %v", err)
	}
	if err := svc.Cancel(t.Context(), e.scope, id); err != nil && !errors.Is(err, ErrInvalidState) {
		t.Fatalf("cancel: %v", err)
	}
	svcEventually(t, "terminal", func() bool {
		sum, err := svc.Status(t.Context(), e.scope, id)
		return err == nil && sum.Status.Terminal()
	})
	svcEventually(t, "completion notice", func() bool { return e.notifier.count() == 1 })
}
