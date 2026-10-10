// ClawEh
// License: MIT

package agent

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
)

// agentServicesLoop has three agents: alice may target bob only; bob may
// target nobody; carol exists. alice uses models alpha then beta.
func agentServicesLoop(t *testing.T) *AgentLoop {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.Providers = []config.Provider{{Name: "p", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:0/v1", APIKey: "k"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "alpha", Model: "alpha-wire", Provider: "p", Enabled: true},
		{ModelName: "beta", Model: "beta-wire", Provider: "p", Enabled: true},
		{ModelName: "gamma", Model: "gamma-wire", Provider: "p", Enabled: true},
	}
	cfg.Agents.Defaults.Models = []string{"gamma"}
	cfg.Agents.List = []config.AgentConfig{
		{
			ID: "alice", Default: true, Models: []string{"alpha", "beta"},
			Subagents: &config.SubagentsConfig{AllowAgents: []string{"bob"}},
		},
		{ID: "bob"},
		{ID: "carol"},
	}
	return mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, nil)
}

func TestAgentServices_TargetsAndModels(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al := agentServicesLoop(t)
	alice, bob := newAgentServices(al, "alice"), newAgentServices(al, "bob")

	if !alice.CanTarget("bob") || alice.CanTarget("carol") {
		t.Fatal("alice: want bob allowed and carol refused (subagents.allow_agents)")
	}
	if bob.CanTarget("alice") {
		t.Fatal("bob has no allow list and must target nobody")
	}
	if got := alice.AllowedModels(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("alice models = %v, want [alpha beta]", got)
	}
	if got := bob.AllowedModels(); !slices.Equal(got, []string{"gamma"}) {
		t.Fatalf("bob models = %v, want the default [gamma]", got)
	}
}

// TestAgentServices_CanTargetOnlyConfigAgents: even with allow_agents "*",
// only enabled config agents can be targeted: never a temporary or unknown id.
func TestAgentServices_CanTargetOnlyConfigAgents(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al := agentServicesLoop(t)
	inst, _ := al.GetRegistry().Get("alice")
	inst.Subagents = &config.SubagentsConfig{AllowAgents: []string{"*"}}
	alice := newAgentServices(al, "alice")

	tempID, err := al.GetRegistry().CreateClone("bob")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !alice.CanTarget("bob") || !alice.CanTarget("carol") {
		t.Fatal("allow_agents \"*\" must allow every config agent")
	}
	for _, id := range []string{tempID, "no-such-agent"} {
		if alice.CanTarget(id) {
			t.Fatalf("CanTarget(%q) = true, want false (not a config agent)", id)
		}
	}
	if _, err := alice.CreateClone(tempID); err == nil {
		t.Fatal("cloned a temporary agent")
	}
}

func TestAgentServices_CloneAndDelete(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al := agentServicesLoop(t)
	reg := al.GetRegistry()
	alice, bob := newAgentServices(al, "alice"), newAgentServices(al, "bob")

	if _, err := alice.CreateClone("carol"); err == nil {
		t.Fatal("alice cloned carol, whom she may not target")
	}
	id, err := alice.CreateClone("bob")
	if err != nil {
		t.Fatalf("CreateClone(bob): %v", err)
	}
	clone, ok := reg.Get(id)
	if !ok || !clone.IsTemp() || clone.Spec.SourceID != "bob" {
		t.Fatalf("clone %s not registered as a temporary clone of bob", id)
	}

	if err := bob.Delete(id); !errors.Is(err, errNotOwner) {
		t.Fatalf("bob deleted alice's clone: err = %v, want errNotOwner", err)
	}
	if err := alice.Delete("bob"); !errors.Is(err, errNotOwner) {
		t.Fatalf("alice deleted config agent bob: err = %v, want errNotOwner", err)
	}
	if err := alice.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := reg.Get(id); ok {
		t.Fatal("clone still registered after Delete")
	}
	if err := alice.Delete(id); !errors.Is(err, agentreg.ErrNotFound) {
		t.Fatalf("second Delete: err = %v, want ErrNotFound", err)
	}
}

// TestAgentServices_OwnershipSurvivesRestart: after a restart (a new owner
// loop restoring temp_agents.json) the agent that created a temporary agent
// can still delete it, and another agent still cannot.
func TestAgentServices_OwnershipSurvivesRestart(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	cfg := ownerTestConfig(t)
	cfg.Providers = []config.Provider{{Name: "p", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:0/v1", APIKey: "k"}}
	cfg.Models = []config.ModelConfig{{ModelName: "alpha", Model: "alpha-wire", Provider: "p", Enabled: true}}
	cfg.Agents.Defaults.Models = []string{"alpha"}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "alice", Default: true, Subagents: &config.SubagentsConfig{AllowAgents: []string{"bob"}}},
		{ID: "bob"},
	}

	first := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, nil, OwnsDataDir())
	clone, err := newAgentServices(first, "alice").CreateClone("bob")
	if err != nil {
		t.Fatalf("CreateClone: %v", err)
	}
	fresh, err := newAgentServices(first, "alice").CreateFresh("alpha", tools.WithSystemPrompt("You are Bob."), tools.WithoutMemory())
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	first.GetRegistry().Close()

	restarted := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, nil, OwnsDataDir())
	alice, bob := newAgentServices(restarted, "alice"), newAgentServices(restarted, "bob")
	if got, ok := restarted.GetRegistry().Get(fresh); !ok || got.Spec.Mode != agentreg.ModeNoMemory ||
		got.Spec.SystemPrompt != "You are Bob." || got.Config.CognitiveMemoryEnabled() {
		t.Fatalf("fresh agent's mode and prompt not restored: %+v", got)
	}
	for _, id := range []string{clone, fresh} {
		if _, ok := restarted.GetRegistry().Get(id); !ok {
			t.Fatalf("%s not restored", id)
		}
		if err := bob.Delete(id); !errors.Is(err, errNotOwner) {
			t.Fatalf("bob deleted alice's %s after restart: err = %v, want errNotOwner", id, err)
		}
		if err := alice.Delete(id); err != nil {
			t.Fatalf("alice could not delete her %s after restart: %v", id, err)
		}
		if _, ok := restarted.GetRegistry().Get(id); ok {
			t.Fatalf("%s still registered after Delete", id)
		}
	}
}

func TestAgentServices_CreateFresh(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al := agentServicesLoop(t)
	reg := al.GetRegistry()
	alice := newAgentServices(al, "alice")

	// The model must be one of the caller's own (alpha, beta); gamma is only
	// bob's default.
	for _, model := range []string{"", "  ", "gamma", "nope"} {
		if _, err := alice.CreateFresh(model); err == nil {
			t.Fatalf("CreateFresh(%q) succeeded; want refused (no model / not alice's)", model)
		}
	}
	// An explicitly given blank prompt is refused, never taken for the default.
	for _, p := range []string{"", "   ", "\n\t"} {
		if _, err := alice.CreateFresh("alpha", tools.WithSystemPrompt(p)); err == nil {
			t.Fatalf("CreateFresh with system prompt %q succeeded", p)
		}
	}
	if got := len(reg.ListTemp()); got != 0 {
		t.Fatalf("refused creations left %d temporary agents", got)
	}

	tests := []struct {
		name       string
		opts       []tools.FreshOption
		wantMode   agentreg.Mode
		wantPrompt string
	}{
		{"default", nil, agentreg.ModeMemory, agentreg.DefaultSystemPrompt},
		{
			"named with prompt",
			[]tools.FreshOption{tools.WithName("Bob"), tools.WithSystemPrompt("You are Bob.")},
			agentreg.ModeMemory, "You are Bob.",
		},
		{"without memory", []tools.FreshOption{tools.WithoutMemory()}, agentreg.ModeNoMemory, agentreg.DefaultSystemPrompt},
		{"single shot", []tools.FreshOption{tools.SingleShot()}, agentreg.ModeSingleShot, agentreg.DefaultSystemPrompt},
		{
			"single shot wins over without memory",
			[]tools.FreshOption{tools.WithoutMemory(), tools.SingleShot()},
			agentreg.ModeSingleShot, agentreg.DefaultSystemPrompt,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, err := alice.CreateFresh("beta-wire", tc.opts...)
			if err != nil {
				t.Fatalf("CreateFresh: %v", err)
			}
			fresh, ok := reg.Get(id)
			if !ok || !fresh.IsTemp() || fresh.Spec.IsClone() || !fresh.Spec.Fresh {
				t.Fatalf("fresh agent %s not registered as a fresh temporary agent", id)
			}
			wantName := tools.NewFreshOptions(tc.opts...).Name
			if fresh.Name != wantName || !slices.Equal(fresh.Config.Models, []string{"beta"}) {
				t.Fatalf("fresh agent name %q models %v, want %q [beta]", fresh.Name, fresh.Config.Models, wantName)
			}
			if fresh.Spec.Mode != tc.wantMode || fresh.Spec.SystemPrompt != tc.wantPrompt {
				t.Fatalf("mode %q prompt %q, want %q %q", fresh.Spec.Mode, fresh.Spec.SystemPrompt, tc.wantMode, tc.wantPrompt)
			}
			if got, want := fresh.Config.CognitiveMemoryEnabled(), tc.wantMode == agentreg.ModeMemory; got != want {
				t.Fatalf("cognitive memory = %v, want %v", got, want)
			}
			if fresh.Spec.Owner != "alice" {
				t.Fatalf("owner = %q, want alice", fresh.Spec.Owner)
			}
			if err := alice.Delete(id); err != nil {
				t.Fatalf("Delete: %v", err)
			}
		})
	}
}

// TestToolDeps_CarriesAgentServices: every agent's tools are built with
// AgentServices bound to that agent.
func TestToolDeps_CarriesAgentServices(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	tools.RegisterProvider(depsCaptureProvider{}) // builds no tools; idempotent
	depsCaptured.Lock()
	depsCaptured.byAgent = map[string]tools.AgentServices{}
	depsCaptured.Unlock()

	agentServicesLoop(t)
	depsCaptured.Lock()
	svc := depsCaptured.byAgent["alice"]
	depsCaptured.Unlock()
	if svc == nil {
		t.Fatal("alice's tools were built without AgentServices")
	}
	if !svc.CanTarget("bob") || svc.CanTarget("carol") {
		t.Fatal("alice's AgentServices is not bound to alice")
	}
}

// depsCaptured records the AgentServices each agent's tools were built with.
var depsCaptured struct {
	sync.Mutex
	byAgent map[string]tools.AgentServices
}

// depsCaptureProvider is a tool provider that builds no tools and records the
// deps it is given.
type depsCaptureProvider struct{}

func (depsCaptureProvider) Namespace() string                       { return "test_deps_capture" }
func (depsCaptureProvider) Description() string                     { return "test" }
func (depsCaptureProvider) Category() string                        { return "test" }
func (depsCaptureProvider) ConfigKey() string                       { return "" }
func (depsCaptureProvider) Available(*config.Config) (bool, string) { return true, "" }
func (depsCaptureProvider) Describe() []tools.ToolDescriptor        { return nil }
func (depsCaptureProvider) Build(d tools.ToolDeps) []tools.Tool {
	depsCaptured.Lock()
	defer depsCaptured.Unlock()
	if depsCaptured.byAgent != nil {
		depsCaptured.byAgent[d.AgentID] = d.Agents
	}
	return nil
}
