// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"errors"
	"testing"
)

// Agent ids are case-insensitive: "Bob" and "bob" are one agent, and a blank
// id names none.
func TestSameAgent(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"Bob", "bob", true},
		{"bOB", "BOB", true},
		{"bob", "alice", false},
		{"", "", false},
		{" ", "", false},
	} {
		if got := sameAgent(tc.a, tc.b); got != tc.want {
			t.Errorf("sameAgent(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// The ownership checks treat a launcher recorded as "Bob" as the agent "bob"
// the store was opened for, and still refuse another agent.
func TestOwnerChecksIgnoreCase(t *testing.T) {
	s := &forumStore{owner: "bob"}
	if err := checkOwner(s, &Snapshot{Origin: Origin{AgentID: "Bob"}}); err != nil {
		t.Errorf("checkOwner(Bob as bob) = %v", err)
	}
	if err := checkOwner(s, &Snapshot{Origin: Origin{AgentID: "alice"}}); !errors.Is(err, errForeign) {
		t.Errorf("checkOwner(alice as bob) = %v, want errForeign", err)
	}
}

// Two participants that are the same real agent in different cases are
// refused like two with one spelling.
func TestRealAgentsIgnoreCase(t *testing.T) {
	cfg := cfgtExample(t)
	cfg.Participants["alice"] = Participant{Agent: "Bob"}
	cfg.Participants["bob"] = Participant{Agent: "bob"}
	cfgtWantIssue(t, validateStatic(cfg), "participants.bob.agent", "an agent can take one seat")
}
