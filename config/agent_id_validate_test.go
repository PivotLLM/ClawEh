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

// An agent id not already in normal form (NormalizeAgentID) is refused at
// load, with one sentence naming it and, where one exists, the fix.
func TestLoadConfig_RefusesAgentIDNotInNormalForm(t *testing.T) {
	long := strings.Repeat("a", 65)
	for _, tc := range []struct {
		id   string
		want string
	}{
		{"Alice.Smith", `Agent id "Alice.Smith" may use only lower-case letters, digits, - and _; use "alice-smith".`},
		{"Alice", `Agent id "Alice" may use only lower-case letters, digits, - and _; use "alice".`},
		{" alice", `Agent id " alice" may use only lower-case letters, digits, - and _; use "alice".`},
		{"alice smith", `Agent id "alice smith" may use only lower-case letters, digits, - and _; use "alice-smith".`},
		{"-alice", `Agent id "-alice" must start with a letter or digit; use "alice".`},
		{long, `Agent id "` + long + `" is longer than 64 characters; use "` + long[:64] + `".`},
		{"!!!", `Agent id "!!!" may use only lower-case letters, digits, - and _.`},
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

// Ids already in normal form load unchanged.
func TestLoadConfig_AcceptsAgentIDInNormalForm(t *testing.T) {
	for _, id := range []string{"alice", "bob-2", "a_b", "main", "9lives", strings.Repeat("b", 64)} {
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
		{"empty binding agent", `["bob"]`, `""`, `[]`, ""},
		{
			"binding agent_id", `["bob"]`, `"Bob"`, `[]`,
			`Agent id "Bob" in bindings may use only lower-case letters, digits, - and _; use "bob".`,
		},
		{
			"agent_mentions", `["bob"]`, `"bob"`, `["Alice"]`,
			`Agent id "Alice" in bindings agent_mentions may use only lower-case letters, digits, - and _; use "alice".`,
		},
		{
			"allow_agents", `["Bob"]`, `"bob"`, `[]`,
			`Agent id "Bob" in Alice's subagents.allow_agents may use only lower-case letters, digits, - and _; use "bob".`,
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

// Every save refuses an id not in normal form as a ValidationError (the API
// answers 400 with the message) and writes nothing.
func TestStoreUpdate_RefusesAgentIDNotInNormalForm(t *testing.T) {
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
			"agent id", func(c *Config) { c.Agents.List = append(c.Agents.List, AgentConfig{ID: "Bob"}) },
			`Agent id "Bob" may use only lower-case letters, digits, - and _; use "bob".`,
		},
		{"binding", func(c *Config) {
			c.Bindings = append(c.Bindings, AgentBinding{AgentID: "Alice", Match: BindingMatch{Channel: "slack"}})
		}, `Agent id "Alice" in bindings may use only lower-case letters, digits, - and _; use "alice".`},
		{"allow_agents", func(c *Config) {
			c.Agents.List[0].Subagents = &SubagentsConfig{AllowAgents: []string{"Bob.B"}}
		}, `Agent id "Bob.B" in alice's subagents.allow_agents may use only lower-case letters, digits, - and _; use "bob-b".`},
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
		c.Agents.List = append(c.Agents.List, AgentConfig{ID: "bob"})
		return nil
	}); err != nil {
		t.Fatalf("valid id refused: %v", err)
	}
}

// Two agents with one id are refused at load and on save, once per id
// however often it is repeated; different ids are fine.
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
