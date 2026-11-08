// ClawEh
// License: MIT

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestClassifiersEffective(t *testing.T) {
	defaults := ClassifiersConfig{
		ProcessedDays: 30,
		Limits: ClassifierLimitsConfig{
			MaxRules: 100, MaxPromptChars: 20000, MaxItemChars: 50000,
			MaxItemsPerRun: 500, MaxParallel: 8, MaxActivityHours: 720, MinEverySeconds: 15,
		},
	}
	if got := (ClassifiersConfig{}).Effective(); got != defaults {
		t.Errorf("absent: Effective() = %+v, want %+v", got, defaults)
	}
	set := ClassifiersConfig{ProcessedDays: 7, Limits: ClassifierLimitsConfig{MaxRules: 5, MaxParallel: 2, MinEverySeconds: 60}}
	want := defaults
	want.ProcessedDays = 7
	want.Limits.MaxRules = 5
	want.Limits.MaxParallel = 2
	want.Limits.MinEverySeconds = 60
	if got := set.Effective(); got != want {
		t.Errorf("some set: Effective() = %+v, want %+v", got, want)
	}
}

func TestClassifiersValidate(t *testing.T) {
	if err := (ClassifiersConfig{}).Validate(); err != nil {
		t.Errorf("zero section: %v", err)
	}
	if err := (ClassifiersConfig{ProcessedDays: 3, Limits: ClassifierLimitsConfig{MaxRules: 1}}).Validate(); err != nil {
		t.Errorf("positive values: %v", err)
	}
	err := ClassifiersConfig{ProcessedDays: -1, Limits: ClassifierLimitsConfig{MaxRules: -1, MaxActivityHours: -2, MinEverySeconds: -3}}.Validate()
	if err == nil {
		t.Fatal("negative values accepted")
	}
	for _, want := range []string{
		"The classifier setting processed_days must be 0 (the default) or more.",
		"The classifier maximum for max_rules must be 0 (the default) or more.",
		"The classifier maximum for max_activity_hours must be 0 (the default) or more.",
		"The classifier minimum for min_every_seconds must be 0 (the default) or more.",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestLoadConfig_ClassifiersSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"classifiers":{"processed_days":10,"limits":{"max_parallel":2}}}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	got := cfg.Classifiers.Effective()
	if got.ProcessedDays != 10 || got.Limits.MaxParallel != 2 || got.Limits.MaxRules != DefaultClassifierMaxRules ||
		got.Limits.MinEverySeconds != DefaultClassifierMinEverySeconds {
		t.Errorf("Effective() = %+v", got)
	}
	write(`{"classifiers":{"limits":{"min_every_seconds":60}}}`)
	if cfg, err = LoadConfig(path); err != nil || cfg.Classifiers.Effective().Limits.MinEverySeconds != 60 {
		t.Errorf("LoadConfig with min_every_seconds 60 = %v, %v", cfg, err)
	}
	write(`{"classifiers":{"limits":{"min_every_seconds":-1}}}`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "The classifier minimum for min_every_seconds") {
		t.Errorf("LoadConfig with a negative minimum = %v", err)
	}
	write(`{"classifiers":{"limits":{"max_items_per_run":-5}}}`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "The classifier maximum for max_items_per_run") {
		t.Errorf("LoadConfig with a negative limit = %v", err)
	}
}

func TestStore_ClassifiersSectionNegativeRefused(t *testing.T) {
	s := newTestStore(t)
	err := s.Update(func(c *Config) error {
		c.Classifiers.ProcessedDays = -1
		return nil
	})
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "processed_days must be 0") {
		t.Errorf("Update with a negative value = %v, want a ValidationError", err)
	}
	if s.Current().Classifiers.ProcessedDays != 0 {
		t.Error("a refused update was applied")
	}
	if err := s.Update(func(c *Config) error {
		c.Classifiers.Limits.MinEverySeconds = -1
		return nil
	}); err == nil || !strings.Contains(err.Error(), "min_every_seconds must be 0") {
		t.Errorf("Update with a negative min_every_seconds = %v", err)
	}
	if err := s.Update(func(c *Config) error {
		c.Classifiers.Limits.MaxParallel = 3
		return nil
	}); err != nil {
		t.Fatalf("a valid update was refused: %v", err)
	}
}

func TestAgentSuiteEnabled_Classifiers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents.List = []AgentConfig{
		{ID: "alice", Classifiers: &AgentClassifiersConfig{Enabled: true, Model: "m"}},
		{ID: "Bob", Classifiers: &AgentClassifiersConfig{Enabled: false, Model: "m"}},
		{ID: "carol"},
	}
	for _, tc := range []struct {
		agent string
		want  bool
	}{{"alice", true}, {"bob", false}, {"carol", false}, {"nobody", false}} {
		if got := cfg.AgentSuiteEnabled(tc.agent, "classifiers"); got != tc.want {
			t.Errorf("AgentSuiteEnabled(%q, classifiers) = %v, want %v", tc.agent, got, tc.want)
		}
	}
}

// classifierTestConfig has alice on her own models and bob on the default
// models.
func classifierTestConfig() *Config {
	cfg := DefaultConfig()
	cfg.Providers = []Provider{{Name: "openai", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test-0123456789"}}
	cfg.Models = []ModelConfig{
		{ModelName: "m", Model: "gpt-4o", Provider: "openai", Enabled: true},
		{ModelName: "cheap", Model: "gpt-4o-mini", Provider: "openai", Enabled: true},
		{ModelName: "other", Model: "gpt-x", Provider: "openai", Enabled: true},
	}
	cfg.Agents.Defaults.Models = []string{"m"}
	cfg.Agents.List = []AgentConfig{
		{ID: "alice", Name: "Alice", Default: true, Models: []string{"cheap", "m"}},
		{ID: "bob", Name: "Bob"},
	}
	return cfg
}

// Turning the switch on with no model writes the agent's first model at that
// moment: its own first model, else the first default model.
func TestStoreUpdate_ClassifierModelCompletedOnSwitchOn(t *testing.T) {
	s := storeFor(t, classifierTestConfig())
	if err := s.Update(func(c *Config) error {
		c.AgentByID("alice").Classifiers = &AgentClassifiersConfig{Enabled: true}
		c.AgentByID("bob").Classifiers = &AgentClassifiersConfig{Enabled: true}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := s.Current().AgentByID("alice").ClassifierModel(); got != "cheap" {
		t.Errorf("alice's classifier model = %q, want cheap", got)
	}
	if got := s.Current().AgentByID("bob").ClassifierModel(); got != "m" {
		t.Errorf("bob's classifier model = %q, want m (the default models)", got)
	}

	// It then stays that model when the agent's own models change.
	if err := s.Update(func(c *Config) error {
		c.AgentByID("alice").Models = []string{"m", "cheap"}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := s.Current().AgentByID("alice").ClassifierModel(); got != "cheap" {
		t.Errorf("after the models changed = %q, want cheap", got)
	}

	// A model the save names is kept as given.
	if err := s.Update(func(c *Config) error {
		c.AgentByID("alice").Classifiers.Model = "m"
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := s.Current().AgentByID("alice").ClassifierModel(); got != "m" {
		t.Errorf("chosen model = %q, want m", got)
	}
}

// Every save fills a missing model for an agent whose switch is on: a hand
// edit that left it out, and a save that clears it.
func TestStoreUpdate_ClassifierModelCompletedOnEverySave(t *testing.T) {
	cfg := classifierTestConfig()
	cfg.AgentByID("alice").Classifiers = &AgentClassifiersConfig{Enabled: true}
	s := storeFor(t, cfg)
	if got := s.Current().ClassifierModelProblems(); len(got) != 1 || !got[0].Missing || got[0].Agent != "alice" {
		t.Errorf("ClassifierModelProblems() at load = %+v, want alice missing", got)
	}
	if err := s.Update(func(c *Config) error {
		c.AgentByID("bob").Name = "Bob B"
		return nil
	}); err != nil {
		t.Fatalf("an unrelated save was refused: %v", err)
	}
	if got := s.Current().AgentByID("alice").ClassifierModel(); got != "cheap" {
		t.Errorf("after an unrelated save = %q, want cheap", got)
	}
	if err := s.Update(func(c *Config) error {
		c.AgentByID("alice").Classifiers.Model = ""
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := s.Current().AgentByID("alice").ClassifierModel(); got != "cheap" {
		t.Errorf("after clearing the model = %q, want cheap", got)
	}
	if got := s.Current().ClassifierModelProblems(); len(got) != 0 {
		t.Errorf("ClassifierModelProblems() = %+v", got)
	}
}

func TestStoreUpdate_ClassifierSwitchOnWithoutModelsRefused(t *testing.T) {
	cfg := classifierTestConfig()
	cfg.Agents.Defaults.Models = nil
	s := storeFor(t, cfg)
	err := s.Update(func(c *Config) error {
		c.AgentByID("bob").Classifiers = &AgentClassifiersConfig{Enabled: true}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "Agent Bob has no model for its classifiers; set agents.list[].classifiers.model.") {
		t.Fatalf("refusal = %v", err)
	}
	if s.Current().AgentByID("bob").Classifiers != nil {
		t.Error("a refused update was applied")
	}

	// One already in the file does not block an unrelated save.
	cfg.AgentByID("bob").Classifiers = &AgentClassifiersConfig{Enabled: true}
	s = storeFor(t, cfg)
	if err := s.Update(func(c *Config) error {
		c.AgentByID("alice").Name = "Alice B"
		return nil
	}); err != nil {
		t.Errorf("an unrelated save was refused: %v", err)
	}
}

func TestStoreUpdate_RefusesUnusableClassifierModel(t *testing.T) {
	s := storeFor(t, classifierTestConfig())
	err := s.Update(func(c *Config) error {
		c.AgentByID("alice").Classifiers = &AgentClassifiersConfig{Enabled: true, Model: "other"}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "Agent Alice cannot use model other for its classifiers; pick one of its models.") {
		t.Fatalf("refusal = %v", err)
	}
	// bob runs on the default models, so "m" is usable and "cheap" is not.
	if err := s.Update(func(c *Config) error {
		c.AgentByID("bob").Classifiers = &AgentClassifiersConfig{Enabled: true, Model: "m"}
		return nil
	}); err != nil {
		t.Fatalf("a usable model was refused: %v", err)
	}
	if err := s.Update(func(c *Config) error {
		c.AgentByID("bob").Classifiers.Model = "cheap"
		return nil
	}); err == nil {
		t.Fatal("a model outside the default models was accepted for bob")
	}
	// A switched-off block is not checked.
	if err := s.Update(func(c *Config) error {
		c.AgentByID("alice").Classifiers = &AgentClassifiersConfig{Enabled: false, Model: "other"}
		return nil
	}); err != nil {
		t.Fatalf("an unused model on a switched-off block was refused: %v", err)
	}
}

// An unusable model already in the file does not block an unrelated save.
func TestStoreUpdate_ExistingUnusableClassifierModelNotRefused(t *testing.T) {
	cfg := classifierTestConfig()
	cfg.AgentByID("alice").Classifiers = &AgentClassifiersConfig{Enabled: true, Model: "other"}
	s := storeFor(t, cfg)
	if err := s.Update(func(c *Config) error {
		c.AgentByID("bob").Name = "Bob B"
		return nil
	}); err != nil {
		t.Fatalf("an unrelated save was refused: %v", err)
	}
	got := s.Current().ClassifierModelProblems()
	if len(got) != 1 || got[0].Agent != "alice" || got[0].Model != "other" || got[0].Missing {
		t.Errorf("ClassifierModelProblems() = %+v", got)
	}
}

func TestStoreUpdate_RefusesMissingClassifierModel(t *testing.T) {
	s := storeFor(t, classifierTestConfig())
	err := s.Update(func(c *Config) error {
		c.AgentByID("alice").Classifiers = &AgentClassifiersConfig{Enabled: true, Model: "nope"}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), `agents.list[alice].classifiers.model: model "nope" does not exist`) {
		t.Fatalf("refusal = %v", err)
	}
	cfg := classifierTestConfig()
	cfg.AgentByID("alice").Classifiers = &AgentClassifiersConfig{Enabled: true, Model: "cheap"}
	if refs := cfg.ModelReferences("cheap"); !slices.Contains(refs, "agents.list[alice].classifiers.model") {
		t.Errorf("ModelReferences(cheap) = %v", refs)
	}
}

func TestStoreUpdate_RefusesClassifiersOnHumanAgent(t *testing.T) {
	cfg := humanTestConfig()
	s := storeFor(t, cfg)
	err := s.Update(func(c *Config) error {
		c.AgentByID("bob").Classifiers = &AgentClassifiersConfig{Enabled: true}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "Bob represents a person and cannot run classifiers.") {
		t.Fatalf("refusal = %v", err)
	}
	if strings.Contains(err.Error(), "has no model") {
		t.Errorf("a human agent's model was completed: %v", err)
	}
	if err := s.Update(func(c *Config) error {
		c.AgentByID("alice").Classifiers = &AgentClassifiersConfig{Enabled: true}
		return nil
	}); err != nil {
		t.Fatalf("classifiers on an ordinary agent were refused: %v", err)
	}
}

// The running copy turns a human agent's classifiers off and keeps the agent
// running.
func TestPruneHumanProblems_TurnsClassifiersOff(t *testing.T) {
	cfg := humanTestConfig()
	cfg.AgentByID("bob").Classifiers = &AgentClassifiersConfig{Enabled: true}
	problems := cfg.HumanProblems()
	if !slices.Contains(problemKinds(problems), HumanClassifiers) {
		t.Fatalf("HumanProblems() kinds = %v, want HumanClassifiers", problemKinds(problems))
	}
	cfg.PruneHumanProblems()
	bob := cfg.AgentByID("bob")
	if bob.ClassifiersEnabled() {
		t.Error("a human agent's classifiers are still on")
	}
	if !bob.IsEnabled() {
		t.Error("the human agent was set aside")
	}
}

func TestReservedWorkspaceName_Classifiers(t *testing.T) {
	if !IsReservedWorkspaceName("Classifiers") {
		t.Error("classifiers is not a reserved workspace name")
	}
	if !slices.Contains(AlwaysReadableWorkspaceDirs, "classifiers") {
		t.Error("classifiers is not always readable")
	}
	s := storeFor(t, classifierTestConfig())
	err := s.Update(func(c *Config) error {
		c.AgentByID("alice").Mounts = []MountConfig{{Name: "classifiers", Path: t.TempDir()}}
		return nil
	})
	if err == nil {
		t.Fatal("a mount named classifiers was accepted")
	}
}

// testdata/classifier_defaults.json holds the defaults of the classifiers
// section; the WebUI's CLASSIFIER_DEFAULTS (form-model.test.ts) is checked
// against the same file, so the System page shows the defaults the server
// applies.
func TestClassifierDefaultsFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/classifier_defaults.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture ClassifiersConfig
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if want := (ClassifiersConfig{}).Effective(); fixture != want {
		t.Fatalf("classifier_defaults.json = %+v, want %+v", fixture, want)
	}
}
