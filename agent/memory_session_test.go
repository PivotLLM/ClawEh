// ClawEh - Cognitive Memory
// License: MIT

package agent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/PivotLLM/cogmem"
	"github.com/PivotLLM/cogmem/store"
	"github.com/PivotLLM/ctxengine/memory"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/providers"
)

func openSessionStore(t *testing.T, agent *AgentInstance, key string) *store.Store {
	t.Helper()
	s, err := store.Open(store.DBPath(cogmemhost.Dir(agent.Workspace)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestWireCognitiveMemory_GatesOnAgentFlag: only agents with cogmem on get a
// session; sub-agent sessions are ephemeral.
func TestWireCognitiveMemory_GatesOnAgentFlag(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	agent := al.registry.GetDefaultAgent()
	mem := al.wireCognitiveMemory(agent, "agent:main:main")
	if mem == nil {
		t.Fatal("default agent has cognitive memory on; expected a session")
	}
	mem.Close()
	off := false
	agent.Config.Cogmem = &off
	defer func() { agent.Config.Cogmem = nil }()
	if al.wireCognitiveMemory(agent, "agent:main:main") != nil {
		t.Fatal("agent with cogmem off got a memory session")
	}
	var none *cogmem.Session
	if inj := recallInjections(context.Background(), none, "x"); inj != nil {
		t.Fatalf("nil session produced injections %+v", inj)
	}
}

// TestMemorySession_BackfillsInboxFromArchiveOnce: on first open after the
// upgrade, archived messages past the watermark are copied into the inbox so
// nothing already spoken is lost to memory; a second open does not repeat it.
func TestMemorySession_BackfillsInboxFromArchiveOnce(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	agent := al.registry.GetDefaultAgent()
	const key = "agent:main:main"
	ctx := context.Background()

	// A store that consolidated through seq 1 before the inbox existed.
	if err := os.MkdirAll(cogmemhost.Dir(agent.Workspace), 0o755); err != nil {
		t.Fatal(err)
	}
	pre, err := store.Open(store.DBPath(cogmemhost.Dir(agent.Workspace)))
	if err != nil {
		t.Fatal(err)
	}
	if err := pre.SetWatermark(ctx, pre.DB(), store.InboxStateKey, 1, 1); err != nil {
		t.Fatal(err)
	}
	_ = pre.Close()

	// An archive holding seqs 1..4.
	a, err := memory.Open(archiveDBPath(agent.Workspace, key))
	if err != nil {
		t.Fatal(err)
	}
	for seq, m := range []providers.Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "tool", Content: "three"},
		{Role: "user", Content: "four"},
	} {
		if err := a.Append(int64(seq+1), m, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	_ = a.Close()

	mem := al.wireCognitiveMemory(agent, key)
	if mem.Store() == nil {
		t.Fatal("store did not open")
	}
	s := openSessionStore(t, agent, key)
	rows, _ := s.InboxRange(ctx, s.DB(), 0, 100)
	if len(rows) != 2 || rows[0].Seq != 2 || rows[1].Seq != 4 {
		t.Fatalf("backfilled inbox = %+v, want seqs 2 and 4 (past watermark 1, meaningful only)", rows)
	}
	mem.Close()

	// Second open: flag set, nothing copied again even though the inbox is empty.
	if _, err := s.DeleteInboxThrough(ctx, s.DB(), 100); err != nil {
		t.Fatal(err)
	}
	again := al.wireCognitiveMemory(agent, key)
	again.Store()
	defer again.Close()
	if n, _ := s.InboxCount(ctx, s.DB()); n != 0 {
		t.Fatalf("backfill ran twice: %d rows", n)
	}
}
