// ClawEh
// License: MIT

package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/global"
)

// The character card named on a spawn reaches the run function in both modes
// and is kept in the callback task's record, so a relaunch keeps it.
func TestSpawn_CharacterCardReachesRunFull(t *testing.T) {
	ws := t.TempDir()
	var (
		mu    sync.Mutex
		cards []string
	)
	mgr := NewSubagentManager(SubagentManagerConfig{
		Workspace:     ws,
		Live:          NewLiveSet(),
		CallerAgentID: "alice",
		RunFull: func(_ context.Context, _, _, _, card string, _ []string) (*global.SyncResult, func(), error) {
			mu.Lock()
			cards = append(cards, card)
			mu.Unlock()
			return &global.SyncResult{Content: "ok", Iterations: 1}, func() {}, nil
		},
	})
	sp := NewSpawner(mgr)
	sp.SetCardChecker(func(id string) bool { return id == "sherlock" })

	if res, err := sp.Spawn(context.Background(), global.SpawnRequest{Mode: global.SpawnAndWait, Task: "t", CharacterCard: " sherlock "}); err != nil || res.IsError {
		t.Fatalf("wait spawn with a card: %v %+v", err, res)
	}
	done := make(chan struct{})
	res, err := sp.Spawn(context.Background(), global.SpawnRequest{
		Mode: global.SpawnCallback, Name: "bg", Task: "t", CharacterCard: "sherlock",
		OnResult: func(*global.Result) { close(done) },
	})
	if err != nil || res.IsError {
		t.Fatalf("callback spawn with a card: %v %+v", err, res)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("callback not delivered")
	}
	mu.Lock()
	got := append([]string(nil), cards...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "sherlock" || got[1] != "sherlock" {
		t.Errorf("cards passed to RunFull = %q, want [sherlock sherlock]", got)
	}

	// Both task records carry the card.
	entries, err := os.ReadDir(filepath.Join(ws, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	records := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "-status.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(ws, "tasks", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var rec TaskRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatal(err)
		}
		if rec.CharacterCard != "sherlock" {
			t.Errorf("%s: character_card = %q, want sherlock", e.Name(), rec.CharacterCard)
		}
		records++
	}
	if records != 2 {
		t.Errorf("found %d task records, want 2", records)
	}
}

// A spawn on a card that does not exist is refused by name before anything
// runs; without a card nothing is checked.
func TestSpawn_MissingCharacterCardRefused(t *testing.T) {
	ran := false
	mgr := NewSubagentManager(SubagentManagerConfig{
		Workspace: t.TempDir(), Live: NewLiveSet(), CallerAgentID: "alice",
		RunFull: func(_ context.Context, _, _, _, _ string, _ []string) (*global.SyncResult, func(), error) {
			ran = true
			return &global.SyncResult{Content: "ok", Iterations: 1}, func() {}, nil
		},
	})
	sp := NewSpawner(mgr)
	sp.SetCardChecker(func(string) bool { return false })

	res, err := sp.Spawn(context.Background(), global.SpawnRequest{Mode: global.SpawnAndWait, Task: "t", CharacterCard: "nobody"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.ForLLM != `Character card "nobody" does not exist.` {
		t.Errorf("result = %+v, want the missing-card refusal", res)
	}
	if ran {
		t.Error("the worker ran on a missing card")
	}
	if res, err := sp.Spawn(context.Background(), global.SpawnRequest{Mode: global.SpawnAndWait, Task: "t"}); err != nil || res.IsError {
		t.Errorf("a spawn without a card was refused: %v %+v", err, res)
	}
	if !ran {
		t.Error("the worker did not run without a card")
	}
}
