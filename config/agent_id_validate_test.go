// ClawEh
// License: MIT

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigFile writes body as config.json in a fresh CLAW_HOME.
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func agentListJSON(id string) string {
	return `{"agents": {"list": [{"id": ` + quoteJSON(id) + `, "default": true}]}}`
}

func quoteJSON(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// An agent id breaking the rule is refused at load, with one sentence naming
// it and, where one exists, the fix, keeping the operator's case.
func TestLoadConfig_RefusesInvalidAgentID(t *testing.T) {
	long := strings.Repeat("a", 65)
	for _, tc := range []struct {
		id   string
		want string
	}{
		{"Alice.Smith", `Agent id "Alice.Smith" may use only letters, digits, - and _; use "Alice-Smith".`},
		{" alice", `Agent id " alice" may use only letters, digits, - and _; use "alice".`},
		{"Alice Smith", `Agent id "Alice Smith" may use only letters, digits, - and _; use "Alice-Smith".`},
		{"-alice", `Agent id "-alice" must start with a letter or digit; use "alice".`},
		{"_Bob", `Agent id "_Bob" must start with a letter or digit; use "Bob".`},
		{long, `Agent id "` + long + `" is longer than 64 characters; use "` + long[:64] + `".`},
		{"!!!", `Agent id "!!!" may use only letters, digits, - and _.`},
		{"", `An agent has no id; give it one, such as "alice".`},
	} {
		t.Run(tc.id, func(t *testing.T) {
			_, err := LoadConfig(writeConfigFile(t, agentListJSON(tc.id)))
			if err == nil || err.Error() != tc.want {
				t.Fatalf("LoadConfig(id %q) = %v, want %q", tc.id, err, tc.want)
			}
		})
	}
}

// Valid ids, upper case included, load as written.
func TestLoadConfig_AcceptsValidAgentID(t *testing.T) {
	for _, id := range []string{"alice", "Bob", "bOB", "bob-2", "a_b", "main", "MAIN", "9lives", strings.Repeat("b", 64)} {
		cfg, err := LoadConfig(writeConfigFile(t, agentListJSON(id)))
		if err != nil {
			t.Fatalf("LoadConfig(id %q) = %v", id, err)
		}
		if got := cfg.Agents.List[0].ID; got != id {
			t.Errorf("id %q loaded as %q", id, got)
		}
	}
}

// References to agents in bindings (agent_id, agent_mentions) and in
// subagents.allow_agents follow the same rule; "*" and an empty binding
// agent_id (the default agent) are allowed.
func TestLoadConfig_AgentReferences(t *testing.T) {
	agents := `"agents": {"list": [
		{"id": "alice", "name": "Alice", "default": true, "subagents": {"allow_agents": %s}},
		{"id": "bob"}]}`
	binding := `"bindings": [{"agent_id": %s, "agent_mentions": %s, "match": {"channel": "slack"}}]`
	build := func(allow, bindingID, mentions string) string {
		return "{" + strings.Replace(agents, "%s", allow, 1) + ", " +
			strings.Replace(strings.Replace(binding, "%s", bindingID, 1), "%s", mentions, 1) + "}"
	}
	for _, tc := range []struct {
		name                       string
		allow, bindingID, mentions string
		want                       string
	}{
		{"valid", `["bob", "*"]`, `"bob"`, `["*", "alice"]`, ""},
		{"other case", `["BOB"]`, `"Bob"`, `["ALICE"]`, ""},
		{"empty binding agent", `["bob"]`, `""`, `[]`, ""},
		{
			"binding agent_id", `["bob"]`, `"Bob Smith"`, `[]`,
			`Agent id "Bob Smith" in bindings may use only letters, digits, - and _; use "Bob-Smith".`,
		},
		{
			"agent_mentions", `["bob"]`, `"bob"`, `["Alice!"]`,
			`Agent id "Alice!" in bindings agent_mentions may use only letters, digits, - and _; use "Alice".`,
		},
		{
			"allow_agents", `["-Bob"]`, `"bob"`, `[]`,
			`Agent id "-Bob" in Alice's subagents.allow_agents must start with a letter or digit; use "Bob".`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfigFile(t, build(tc.allow, tc.bindingID, tc.mentions)))
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("LoadConfig = %v, want no error", err)
			case tc.want != "" && (err == nil || err.Error() != tc.want):
				t.Fatalf("LoadConfig = %v, want %q", err, tc.want)
			}
		})
	}
}

// Every save refuses an invalid id as a ValidationError (the API answers 400
// with the message) and writes nothing.
func TestStoreUpdate_RefusesInvalidAgentID(t *testing.T) {
	p := writeConfigFile(t, agentListJSON("alice"))
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		fn   func(c *Config)
		want string
	}{
		{
			"agent id", func(c *Config) { c.Agents.List = append(c.Agents.List, AgentConfig{ID: "Bob Smith"}) },
			`Agent id "Bob Smith" may use only letters, digits, - and _; use "Bob-Smith".`,
		},
		{"binding", func(c *Config) {
			c.Bindings = append(c.Bindings, AgentBinding{AgentID: "Alice/x", Match: BindingMatch{Channel: "slack"}})
		}, `Agent id "Alice/x" in bindings may use only letters, digits, - and _; use "Alice-x".`},
		{"allow_agents", func(c *Config) {
			c.Agents.List[0].Subagents = &SubagentsConfig{AllowAgents: []string{"Bob.B"}}
		}, `Agent id "Bob.B" in alice's subagents.allow_agents may use only letters, digits, - and _; use "Bob-B".`},
		{
			"same agent in another case", func(c *Config) { c.Agents.List = append(c.Agents.List, AgentConfig{ID: "ALICE"}) },
			`Agent ids "alice" and "ALICE" name the same agent; give each agent its own id.`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Update(func(c *Config) error { tc.fn(c); return nil })
			var verr *ValidationError
			if !errors.As(err, &verr) || err.Error() != tc.want {
				t.Fatalf("Update = %v, want ValidationError %q", err, tc.want)
			}
			if after, rerr := os.ReadFile(p); rerr != nil || string(after) != string(before) {
				t.Error("refused update was written")
			}
		})
	}
	if err := s.Update(func(c *Config) error {
		c.Agents.List = append(c.Agents.List, AgentConfig{ID: "Bob"})
		return nil
	}); err != nil {
		t.Fatalf("valid id refused: %v", err)
	}
}

// Two agents with one id, in any case, are refused at load and on save, once
// per agent however often it is repeated; different ids are fine.
func TestAgentIDUsedTwice(t *testing.T) {
	const want = `Agent id "alice" is used twice; give each agent its own id.`
	for _, tc := range []struct {
		name string
		ids  []string
		want string
	}{
		{"twice", []string{"alice", "alice"}, want},
		{"three times", []string{"alice", "bob", "alice", "alice"}, want},
		{"different", []string{"alice", "bob"}, ""},
		{"case", []string{"Bob", "bob"}, `Agent ids "Bob" and "bob" name the same agent; give each agent its own id.`},
		{"cases", []string{"Bob", "alice", "bob", "Bob", "BOB"}, `Agent ids "Bob", "bob" and "BOB" name the same agent; give each agent its own id.`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var agents []string
			for _, id := range tc.ids {
				agents = append(agents, `{"id": `+quoteJSON(id)+`}`)
			}
			_, err := LoadConfig(writeConfigFile(t, `{"agents": {"list": [`+strings.Join(agents, ", ")+`]}}`))
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("LoadConfig = %v, want no error", err)
			case tc.want != "" && (err == nil || err.Error() != tc.want):
				t.Fatalf("LoadConfig = %v, want %q", err, tc.want)
			}
		})
	}

	p := writeConfigFile(t, agentListJSON("alice"))
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(c *Config) error {
		c.Agents.List = append(c.Agents.List, AgentConfig{ID: "alice"})
		return nil
	})
	var verr *ValidationError
	if !errors.As(err, &verr) || err.Error() != want {
		t.Fatalf("Update = %v, want ValidationError %q", err, want)
	}
}
