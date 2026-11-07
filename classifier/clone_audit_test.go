// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"testing"
)

// A change or rerun a clone makes through the tools names the clone in its
// audit events; the scheduled runs, made for the agent itself, name none.
func TestAuditNamesTheClone(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.run("inbox-priority")

	clone := e.scope
	clone.Clone = "1a2b3c4d"
	if err := e.svc.Update(context.Background(), clone, "inbox-priority", []byte(`{"dry_run":false}`)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := e.svc.Rerun(context.Background(), clone, "inbox-priority", "m1"); err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if err := e.svc.Delete(context.Background(), clone, "inbox-priority"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	writes := e.auditor.kinds(AuditWrite)
	if len(writes) != 3 || writes[0].Clone != "" || writes[1].Clone != "1a2b3c4d" || writes[2].Clone != "1a2b3c4d" {
		t.Fatalf("write events = %+v; want create by the agent, update and delete by the clone", writes)
	}
	for _, ev := range writes {
		if ev.Agent != e.scope.AgentID {
			t.Errorf("write event under %q, want %q", ev.Agent, e.scope.AgentID)
		}
	}
	classified := e.auditor.kinds(AuditClassification)
	last := classified[len(classified)-1]
	if last.Clone != "1a2b3c4d" || last.ItemID != "m1" {
		t.Errorf("rerun event = %+v, want the clone named", last)
	}
	for _, ev := range classified[:len(classified)-1] {
		if ev.Clone != "" {
			t.Errorf("scheduled run event names clone %q", ev.Clone)
		}
	}
}
