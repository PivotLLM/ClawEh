// ClawEh
// License: MIT

package maestro

import (
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
)

// A temporary agent (a sub-agent clone acting as its source) gets the Maestro
// tools on the source's runner, and registering them changes nothing about
// that runner: same runner, same dispatcher, still pointed at the source's.
func TestTempAgentUsesSourceRunnerUntouched(t *testing.T) {
	reg := newRegistration(t, "alice")
	reg.register(t, newGatedRunner())
	before := cachedEntry("alice")
	if before == nil {
		t.Fatal("the source has no cached runner")
	}
	sourceDisp := before.disp.cur.Load()

	built := tools.NamespacedProvider("maestro", GlobalProvider).Build(tools.ToolDeps{
		Cfg:       reg.cfg,
		AgentCfg:  &reg.cfg.Agents.List[0],
		AgentID:   "alice",
		Workspace: reg.ws,
		Spawn:     newGatedRunner(),
		TempAgent: true,
	})
	if len(built) == 0 {
		t.Fatal("a clone must still get the Maestro tools")
	}
	after := cachedEntry("alice")
	if after != before || after.runner != before.runner {
		t.Fatal("registering a clone replaced the source's runner")
	}
	if after.disp.cur.Load() != sourceDisp {
		t.Fatal("registering a clone re-pointed the source's dispatcher")
	}
}

// With no runner cached for the source, a clone gets a private one that is
// never cached.
func TestTempAgentWithoutSourceRunnerIsNotCached(t *testing.T) {
	reg := newRegistration(t, "bob")
	built := tools.NamespacedProvider("maestro", GlobalProvider).Build(tools.ToolDeps{
		Cfg:       reg.cfg,
		AgentCfg:  &reg.cfg.Agents.List[0],
		AgentID:   "bob",
		Workspace: reg.ws,
		Spawn:     newGatedRunner(),
		TempAgent: true,
	})
	if len(built) == 0 {
		t.Fatal("a clone must still get the Maestro tools")
	}
	if cachedEntry("bob") != nil {
		t.Fatal("a clone's runner was cached for its source")
	}
}
