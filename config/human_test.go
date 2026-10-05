// ClawEh
// License: MIT

package config

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// humanTestConfig is a valid configuration with one ordinary agent (alice)
// and one human agent (bob) reached in his own chat.
func humanTestConfig() *Config {
	cfg := DefaultConfig()
	cfg.Providers = []Provider{
		{Name: "openai", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test-0123456789"},
		{Name: "People", Protocol: HumanProtocol},
	}
	cfg.Models = []ModelConfig{
		{ModelName: "m", Model: "gpt-4o", Provider: "openai", Enabled: true},
		{ModelName: "Bob (human)", Model: "bob", Provider: "People", Enabled: true, RequestTimeout: 600},
	}
	cfg.Agents.Defaults.Models = []string{"m"}
	cfg.Agents.List = []AgentConfig{
		{ID: "alice", Name: "Alice", Default: true, Models: []string{"m"}},
		{ID: "bob", Name: "Bob", Models: []string{"Bob (human)"}},
	}
	cfg.Bindings = []AgentBinding{
		{AgentID: "alice", Match: BindingMatch{Channel: "telegram-main"}},
		{AgentID: "bob", Default: true, Match: BindingMatch{Channel: "telegram-main", Peer: &PeerMatch{Kind: "direct", ID: "4242"}}},
	}
	return cfg
}

func problemKinds(ps []HumanProblem) []HumanProblemKind {
	out := make([]HumanProblemKind, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Kind)
	}
	return out
}

func TestHumanModelRecognition(t *testing.T) {
	cfg := humanTestConfig()
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"Bob (human)", true},
		{"bob", false}, // a person is named by model_name only, never by model id
		{"m", false},
		{"gpt-4o", false},
		{"", false},
		{"missing", false},
	} {
		if got := cfg.IsHumanModel(tc.name); got != tc.want {
			t.Errorf("IsHumanModel(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	if !cfg.IsHumanAgent("bob") || !cfg.IsHumanAgent("BOB") {
		t.Error("bob is not recognised as a human agent")
	}
	if cfg.IsHumanAgent("alice") || cfg.IsHumanAgent("nobody") {
		t.Error("an ordinary or unknown agent is reported human")
	}
	if got := cfg.HumanRequestTimeout("bob"); got != 600 {
		t.Errorf("HumanRequestTimeout = %d, want the model's 600", got)
	}
	cfg.Models[1].RequestTimeout = 0
	cfg.Agents.Defaults.RequestTimeout = 90
	if got := cfg.HumanRequestTimeout("bob"); got != 90 {
		t.Errorf("HumanRequestTimeout = %d, want the default 90", got)
	}
}

func TestHumanProtocolNeedsNoEndpointOrKey(t *testing.T) {
	cfg := humanTestConfig()
	if err := cfg.ValidateProviders(); err != nil {
		t.Fatalf("ValidateProviders: %v", err)
	}
	if err := cfg.ValidateProvider(1); err != nil {
		t.Fatalf("ValidateProvider: %v", err)
	}
	if !cfg.Providers[1].HasCredentials() {
		t.Error("a human provider reports missing credentials")
	}
	if dp, dm := cfg.PruneInvalid(); dp != 0 || dm != 0 {
		t.Errorf("PruneInvalid dropped %d providers, %d models", dp, dm)
	}
}

func TestHumanProblems(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(c *Config)
		want   []HumanProblemKind
		text   string
	}{
		{name: "valid", mutate: func(*Config) {}},
		{name: "a disabled human agent is not checked", mutate: func(c *Config) {
			off := false
			c.Agents.List[1].Enabled = &off
			c.Bindings = c.Bindings[:1]
		}},
		{name: "fallback after the person", mutate: func(c *Config) {
			c.Agents.List[1].Models = []string{"Bob (human)", "m"}
		}, want: []HumanProblemKind{HumanExtraModels}, text: "Bob represents a person, so Bob (human) must be its only model."},
		{name: "a model before the person", mutate: func(c *Config) {
			c.Agents.List[0].Models = []string{"m", "Bob (human)"}
		}, want: []HumanProblemKind{HumanExtraModels, HumanDefault, HumanNoChat}, text: "Alice represents a person"},
		{name: "explicit default", mutate: func(c *Config) {
			c.Agents.List[0].Default = false
			c.Agents.List[1].Default = true
		}, want: []HumanProblemKind{HumanDefault}, text: "Bob represents a person and can't be the default agent."},
		{name: "a second binding", mutate: func(c *Config) {
			c.Bindings = append(c.Bindings, AgentBinding{AgentID: "bob", Match: BindingMatch{Channel: "slack", Peer: &PeerMatch{Kind: "channel", ID: "C1"}}})
		}, want: []HumanProblemKind{HumanBindings}, text: "Bob must have exactly one chat: its default."},
		{name: "a whole bot routed to the person", mutate: func(c *Config) {
			c.Bindings[1] = AgentBinding{AgentID: "bob", Default: true, DeliverTo: "4242", Match: BindingMatch{Channel: "telegram-bob"}}
		}, want: []HumanProblemKind{HumanBindings}, text: "Bob needs a chat of its own"},
		{name: "model name shared with another model", mutate: func(c *Config) {
			c.Models = append(c.Models, ModelConfig{ModelName: "Bob (human)", Model: "gpt-4o", Provider: "openai", Enabled: true})
		}, want: []HumanProblemKind{HumanSharedName}, text: "The model name Bob (human) is used by a person and by another model."},
		{name: "no default binding", mutate: func(c *Config) {
			c.Bindings[1].Default = false
		}, want: []HumanProblemKind{HumanNoChat}, text: "Bob needs a default chat where the person is reached."},
		{name: "chat bound to another agent as a peer", mutate: func(c *Config) {
			c.Bindings = append(c.Bindings, AgentBinding{AgentID: "alice", Match: BindingMatch{Channel: "telegram-main", Peer: &PeerMatch{Kind: "direct", ID: "4242"}}})
		}, want: []HumanProblemKind{HumanSharedChat}, text: "Bob's chat is also used by Alice."},
		{name: "chat is another agent's delivery target", mutate: func(c *Config) {
			c.Bindings[0].DeliverTo = "4242"
		}, want: []HumanProblemKind{HumanSharedChat}, text: "also used by Alice"},
		{name: "same chat id on another channel is fine", mutate: func(c *Config) {
			c.Bindings = append(c.Bindings, AgentBinding{AgentID: "alice", Match: BindingMatch{Channel: "slack", Peer: &PeerMatch{Kind: "channel", ID: "4242"}}})
		}},
		{name: "summarization model", mutate: func(c *Config) {
			c.Summarization.Models = []string{"Bob (human)"}
		}, want: []HumanProblemKind{HumanModelMisused}, text: "Bob (human) represents a person and can't be a summarization model."},
		{name: "agent summarization model", mutate: func(c *Config) {
			c.Agents.List[0].SummarizationModels = []string{"Bob (human)"}
		}, want: []HumanProblemKind{HumanModelMisused}, text: "a summarization model"},
		{name: "vision model", mutate: func(c *Config) {
			c.Agents.Defaults.VisionModel = "Bob (human)"
		}, want: []HumanProblemKind{HumanModelMisused}, text: "can't be a vision model."},
		{name: "vision fallback", mutate: func(c *Config) {
			c.Agents.Defaults.VisionModelFallbacks = []string{"Bob (human)"}
		}, want: []HumanProblemKind{HumanModelMisused}, text: "can't be a vision model."},
		{name: "default models", mutate: func(c *Config) {
			c.Agents.Defaults.Models = []string{"m", "Bob (human)"}
		}, want: []HumanProblemKind{HumanModelMisused}, text: "can't be a default model."},
		{name: "sub-agent models", mutate: func(c *Config) {
			c.Agents.List[0].Subagents = &SubagentsConfig{Models: []string{"Bob (human)"}}
		}, want: []HumanProblemKind{HumanModelMisused}, text: "can't be a sub-agent model."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := humanTestConfig()
			tc.mutate(cfg)
			got := cfg.HumanProblems()
			if !slices.Equal(problemKinds(got), tc.want) {
				t.Fatalf("problems = %+v, want kinds %v", got, tc.want)
			}
			if tc.text != "" && !strings.Contains(got[0].Message, tc.text) {
				t.Errorf("message %q does not contain %q", got[0].Message, tc.text)
			}
		})
	}
}

func newHumanTestStore(t *testing.T, cfg *Config) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	p := filepath.Join(dir, "config.json")
	if err := SaveConfig(p, cfg); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(p)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// A save that introduces a human-agent problem is refused; one that adds a
// human agent before its chat is saved, because the binding can only name an
// agent that exists.
func TestStoreUpdate_HumanRules(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(c *Config)
		refused string
	}{
		{name: "valid change", mutate: func(c *Config) { c.Agents.List[0].Name = "Alice A." }},
		{name: "human agent before its chat", mutate: func(c *Config) {
			c.Agents.List = append(c.Agents.List, AgentConfig{ID: "bob2", Models: []string{"Bob (human)"}})
		}},
		{name: "fallback", mutate: func(c *Config) {
			c.Agents.List[1].Models = append(c.Agents.List[1].Models, "m")
		}, refused: "must be its only model"},
		{name: "shared chat", mutate: func(c *Config) {
			c.Bindings[0].DeliverTo = "4242"
		}, refused: "also used by Alice"},
		{name: "summarizer", mutate: func(c *Config) {
			c.Summarization.Models = []string{"Bob (human)"}
		}, refused: "can't be a summarization model"},
		{name: "default agent", mutate: func(c *Config) {
			c.Agents.List[1].Default = true
		}, refused: "can't be the default agent"},
		{name: "second binding", mutate: func(c *Config) {
			c.Bindings = append(c.Bindings, AgentBinding{AgentID: "bob", Match: BindingMatch{Channel: "slack", Peer: &PeerMatch{Kind: "channel", ID: "C1"}}})
		}, refused: "exactly one chat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newHumanTestStore(t, humanTestConfig())
			err := s.Update(func(c *Config) error { tc.mutate(c); return nil })
			if tc.refused == "" {
				if err != nil {
					t.Fatalf("Update refused: %v", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || !strings.Contains(err.Error(), tc.refused) {
				t.Fatalf("Update error = %v, want a validation error containing %q", err, tc.refused)
			}
		})
	}
}

// A problem already in the file does not block an unrelated save.
func TestStoreUpdate_ExistingHumanProblemDoesNotBlock(t *testing.T) {
	cfg := humanTestConfig()
	cfg.Agents.List[1].Models = []string{"Bob (human)", "m"}
	s := newHumanTestStore(t, cfg)
	if err := s.Update(func(c *Config) error { c.Agents.List[0].Name = "Alice A."; return nil }); err != nil {
		t.Fatalf("unrelated save refused: %v", err)
	}
}

func TestPruneHumanProblems(t *testing.T) {
	t.Run("valid config is unchanged", func(t *testing.T) {
		cfg := humanTestConfig()
		if got := cfg.PruneHumanProblems(); len(got) != 0 {
			t.Fatalf("pruned %+v from a valid config", got)
		}
		if !cfg.Agents.List[1].IsEnabled() || !cfg.Agents.List[0].IsEnabled() {
			t.Fatal("an agent was disabled")
		}
	})
	t.Run("invalid agents disabled, misused models dropped", func(t *testing.T) {
		cfg := humanTestConfig()
		cfg.Agents.List[1].Models = []string{"Bob (human)", "m"}
		cfg.Summarization.Models = []string{"Bob (human)", "m"}
		cfg.Agents.Defaults.VisionModel = "Bob (human)"
		got := cfg.PruneHumanProblems()
		if len(got) != 3 {
			t.Fatalf("pruned %+v, want 3 problems", got)
		}
		if cfg.Agents.List[1].IsEnabled() {
			t.Error("bob is still enabled")
		}
		if !cfg.Agents.List[0].IsEnabled() {
			t.Error("alice was disabled")
		}
		if !slices.Equal(cfg.Summarization.Models, []string{"m"}) {
			t.Errorf("summarization.models = %v, want [m]", cfg.Summarization.Models)
		}
		if cfg.Agents.Defaults.VisionModel != "" {
			t.Errorf("vision_model = %q, want it cleared", cfg.Agents.Defaults.VisionModel)
		}
		if !slices.Equal(cfg.Agents.List[1].Models, []string{"Bob (human)", "m"}) {
			t.Error("the agent's own model list was edited")
		}
	})
	t.Run("a model sharing a person's name is dropped", func(t *testing.T) {
		cfg := humanTestConfig()
		cfg.Models = append(cfg.Models, ModelConfig{ModelName: "Bob (human)", Model: "gpt-4o", Provider: "openai", Enabled: true})
		cfg.PruneHumanProblems()
		if len(cfg.Models) != 2 || cfg.Models[1].Provider != "People" {
			t.Fatalf("models = %+v, want the impostor entry dropped", cfg.Models)
		}
		if !cfg.Agents.List[1].IsEnabled() {
			t.Error("bob was disabled")
		}
	})
}
