// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// The ccv3 suite is off unless the agent's `ccv3` switch is on.
func TestConfig_AgentSuiteEnabledCCv3(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"agents":{"list":[{"id":"alice","ccv3":true},{"id":"bob"}]}}`), &c); err != nil {
		t.Fatal(err)
	}
	if !c.AgentSuiteEnabled("alice", "ccv3") {
		t.Error("ccv3 is off for alice, whose switch is on")
	}
	if c.AgentSuiteEnabled("Alice", "ccv3") != true {
		t.Error("ccv3 switch is not matched case-insensitively")
	}
	if c.AgentSuiteEnabled("bob", "ccv3") {
		t.Error("ccv3 is on for bob, whose switch is absent (default off)")
	}
	if c.AgentSuiteEnabled("nobody", "ccv3") {
		t.Error("ccv3 is on for an unknown agent")
	}
	out, err := json.Marshal(c.Agents.List[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "ccv3") || strings.Contains(string(out), "character_card") {
		t.Errorf("an agent without the keys is saved as %s, want neither key", out)
	}
}

func TestConfig_CardsPath(t *testing.T) {
	c := &Config{dataDir: "/data"}
	if got, want := c.CardsPath(), filepath.Join("/data", "cards"); got != want {
		t.Errorf("CardsPath() = %q, want %q", got, want)
	}
}

// A human agent renders no prompt, so a card on it would be ignored: the store
// refuses a save that introduces one, names the agent, and lets a save that
// does not touch an existing one through.
func TestStoreUpdate_RefusesCardOnHumanAgent(t *testing.T) {
	cfg := humanTestConfig() // bob is a human agent; alice is not
	store := storeFor(t, cfg)

	err := store.Update(func(c *Config) error {
		c.AgentByID("bob").CharacterCard = "sherlock"
		return nil
	})
	if err == nil {
		t.Fatal("a card on a human agent was accepted")
	}
	if !strings.Contains(err.Error(), "Bob represents a person and cannot have a character card \"sherlock\".") {
		t.Errorf("refusal = %q", err)
	}
	if err := store.Update(func(c *Config) error {
		c.AgentByID("alice").CharacterCard = "sherlock"
		return nil
	}); err != nil {
		t.Fatalf("a card on an ordinary agent was refused: %v", err)
	}

	// One already in the file does not block an unrelated save.
	cfg.AgentByID("bob").CharacterCard = "watson"
	store = storeFor(t, cfg)
	if got := cfg.CardsOnHumanAgents(); len(got) != 1 || got[0] != "bob" {
		t.Errorf("CardsOnHumanAgents() = %v, want [bob]", got)
	}
	if err := store.Update(func(c *Config) error {
		c.AgentByID("alice").Name = "Alice B"
		return nil
	}); err != nil {
		t.Fatalf("an unrelated save was refused: %v", err)
	}
}

// storeFor saves cfg to a temporary data directory and opens a Store on it.
func storeFor(t *testing.T, cfg *Config) *Store {
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
