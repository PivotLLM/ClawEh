// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"errors"
	"strings"
	"testing"
)

// A forum directory lives in its launcher's workspace and is not trusted
// for whose forum it is: opened in another agent's scope (its snapshot
// edited, or the directory copied), it is refused as damaged and Recover
// does not resume it.
func TestSvcForumOfAnotherLauncherRefused(t *testing.T) {
	e := svcSetup(t)
	id, err := e.svc.Launch(t.Context(), []byte(svcSimpleJSON), e.opts())
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	e.restart()
	other := Scope{AgentID: "bob", BaseDirectory: e.scope.BaseDirectory}
	if _, err := e.svc.Status(t.Context(), other, id); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Status in bob's scope = %v, want ErrCorrupt", err)
	}
	if _, err := e.svc.Results(t.Context(), other, id); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Results in bob's scope = %v, want ErrCorrupt", err)
	}
	if err := e.svc.Recover(t.Context(), []Scope{other}); err == nil || !strings.Contains(err.Error(), "launcher") {
		t.Errorf("Recover in bob's scope = %v, want a refusal naming the launcher", err)
	}
	if _, err := e.svc.Status(t.Context(), e.scope, id); err != nil {
		t.Errorf("Status in the launcher's scope: %v", err)
	}
}

// Temporary agents are deleted on behalf of the forum's launcher, which the
// host checks against each agent's recorded owner.
func TestSvcDeleteNamesTheLauncher(t *testing.T) {
	e := svcSetup(t)
	messenger := &svcReplier{asks: map[string]int{}}
	svc := New(Host{Messenger: messenger, Agents: e.agents, Notifier: e.notifier, Logger: e.logger, Schemas: JSONSchemaValidator{}})
	t.Cleanup(func() { svcClose(t, svc) })
	if _, err := svc.Launch(t.Context(), []byte(svcSimpleJSON), e.opts()); err != nil {
		t.Fatalf("launch: %v", err)
	}
	svcEventually(t, "the temporary agent's deletion", func() bool { return len(e.agents.deletedIDs()) == 1 })
	e.agents.mu.Lock()
	defer e.agents.mu.Unlock()
	if len(e.agents.launchers) == 0 {
		t.Fatal("no Delete or Touch was made")
	}
	for _, l := range e.agents.launchers {
		if l != e.scope.AgentID {
			t.Errorf("Delete/Touch named launcher %q, want %q", l, e.scope.AgentID)
		}
	}
}
