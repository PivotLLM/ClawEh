// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
)

// ownerTestConfig is a configuration with a data directory, so temporary
// agents live under <data>/internal/temp.
func ownerTestConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv(global.EnvVarHome, t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Models = []string{"test-model"}
	cfg.Agents.Defaults.MaxToolIterations = 10
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Name: "Main", Default: true}}
	return cfg
}

// Only the loop that owns the data directory (the gateway) saves, restores
// and cleans temporary agents. A second loop on the same directory — `claw
// agent` beside the running service — leaves them alone.
func TestNewAgentLoop_OnlyOwnerManagesTempAgents(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	cfg := ownerTestConfig(t)
	internal := filepath.Join(cfg.DataDir(), global.InternalDir)
	statePath := filepath.Join(internal, agentreg.StateFileName)

	service := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, nil, OwnsDataDir())
	saved, err := service.GetRegistry().CreateFresh(config.AgentConfig{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	inFlight, err := service.GetRegistry().CreateClone("main", agentreg.EphemeralMemory())
	if err != nil {
		t.Fatalf("Create clone: %v", err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("the owner did not save its temporary agents: %v", err)
	}

	cli := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, nil)
	if _, ok := cli.GetRegistry().Get(saved); ok {
		t.Fatal("a non-owner loop restored the service's temporary agent")
	}
	for _, id := range []string{saved, inFlight} {
		if _, statErr := os.Stat(filepath.Join(internal, agentreg.TempDirName, id)); statErr != nil {
			t.Fatalf("a non-owner loop removed the service's agent %s: %v", id, statErr)
		}
	}
	mine, err := cli.GetRegistry().CreateClone("main")
	if err != nil {
		t.Fatalf("non-owner Create: %v", err)
	}
	after, err := os.ReadFile(statePath)
	if err != nil || string(after) != string(before) {
		t.Fatal("a non-owner loop rewrote temp_agents.json")
	}
	// Its own agents live in a private root, never under the shared
	// internal/temp the service cleans, and go with it.
	info, _ := cli.GetRegistry().Info(mine)
	root := cli.GetRegistry().TempRoot()
	if strings.HasPrefix(info.Spec.StateDir, internal) || !strings.HasPrefix(info.Spec.StateDir, root) {
		t.Fatalf("non-owner agent state dir = %q, want it under its private root %q", info.Spec.StateDir, root)
	}
	cli.GetRegistry().Close()
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("the non-owner's private root survived Close: %v", statErr)
	}
}

// A turn on a temporary agent's instance that a reload has since replaced
// runs on the current instance, never the closed one; a turn for a deleted
// agent is dropped.
func TestRunAgentLoop_StaleTempInstance(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	tl := newTestAgentLoop(t)
	al := tl.al
	id, err := al.GetRegistry().CreateClone("main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	stale, _ := al.GetRegistry().Get(id)
	if err = al.ReloadProviderAndConfig(context.Background(), tl.provider, tl.cfg); err != nil {
		t.Fatalf("reload: %v", err)
	}
	key := routing.BuildAgentMainSessionKey(id)
	opts := processOptions{SessionKey: key, Channel: "subagent", ChatID: key, UserMessage: "hello"}
	if _, err = al.runAgentLoop(context.Background(), stale, opts); err != nil {
		t.Fatalf("turn on a rebuilt agent's old instance: %v, want it run on the current one", err)
	}
	current, _ := al.GetRegistry().Get(id)
	if len(current.Sessions.GetHistory(key)) == 0 {
		t.Fatal("the turn did not run on the current instance")
	}

	if err = al.GetRegistry().Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err = al.runAgentLoop(context.Background(), current, opts); !errors.Is(err, errAgentGone) {
		t.Fatalf("turn on a deleted agent: err = %v, want errAgentGone", err)
	}
}
