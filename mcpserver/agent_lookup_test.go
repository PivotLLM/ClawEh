// ClawEh
// License: MIT

package mcpserver

import (
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
)

// A tools/call for an agent the host was not started with (a temporary agent
// created later) dispatches through the lookup; a registered agent never
// consults it, and without a lookup the agent is unknown.
func TestRegistryFor_FallsBackToLookup(t *testing.T) {
	alice := newRegistryWith()
	clone := newRegistryWith()
	looked := []string{}
	srv, err := New(append(minimalOpts(alice), WithAgentLookup(func(id string) (*tools.ToolRegistry, bool) {
		looked = append(looked, id)
		if id == "c1" {
			return clone, true
		}
		return nil, false
	}))...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if reg, ok := srv.registryFor("alice"); !ok || reg != alice {
		t.Fatal("a registered agent must resolve to its own registry")
	}
	if reg, ok := srv.registryFor("c1"); !ok || reg != clone {
		t.Fatal("a temporary agent must resolve through the lookup")
	}
	if _, ok := srv.registryFor("nobody"); ok {
		t.Fatal("an unknown agent must not resolve")
	}
	if len(looked) != 2 || looked[0] != "c1" {
		t.Fatalf("lookup consulted for %v, want only the unregistered ids", looked)
	}

	plain, err := New(minimalOpts(alice)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := plain.registryFor("c1"); ok {
		t.Fatal("without a lookup an unregistered agent must not resolve")
	}
}
