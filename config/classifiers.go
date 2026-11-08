// ClawEh
// License: MIT

package config

import (
	"errors"
	"slices"
	"strings"
)

// ClassifiersConfig holds the install-wide classifier settings: how long
// processed item ids are kept, and the maximums classifier_validate,
// classifier_create and classifier_update check a definition against. A
// field left out or set to 0 uses its default; a negative value is refused at
// load and on save. A change applies to the next validation and run.
type ClassifiersConfig struct {
	ProcessedDays int                    `json:"processed_days,omitempty"`
	Limits        ClassifierLimitsConfig `json:"limits,omitzero"`
}

// ClassifierLimitsConfig is the install's maximum for a classifier
// definition's limits, and its minimum for trigger.every_seconds.
type ClassifierLimitsConfig struct {
	MaxRules         int `json:"max_rules,omitempty"`
	MaxPromptChars   int `json:"max_prompt_chars,omitempty"`
	MaxItemChars     int `json:"max_item_chars,omitempty"`
	MaxItemsPerRun   int `json:"max_items_per_run,omitempty"`
	MaxParallel      int `json:"max_parallel,omitempty"`
	MaxActivityHours int `json:"max_activity_hours,omitempty"`
	MinEverySeconds  int `json:"min_every_seconds,omitempty"`
}

// The defaults of the classifiers section.
const (
	DefaultClassifierProcessedDays    = 30
	DefaultClassifierMaxRules         = 100
	DefaultClassifierMaxPromptChars   = 20000
	DefaultClassifierMaxItemChars     = 50000
	DefaultClassifierMaxItemsPerRun   = 500
	DefaultClassifierMaxParallel      = 8
	DefaultClassifierMaxActivityHours = 720
	DefaultClassifierMinEverySeconds  = 15
)

// Effective returns the section with every unset (0) field at its default.
func (c ClassifiersConfig) Effective() ClassifiersConfig {
	orDefault := func(v, def int) int {
		if v <= 0 {
			return def
		}
		return v
	}
	l := c.Limits
	return ClassifiersConfig{
		ProcessedDays: orDefault(c.ProcessedDays, DefaultClassifierProcessedDays),
		Limits: ClassifierLimitsConfig{
			MaxRules:         orDefault(l.MaxRules, DefaultClassifierMaxRules),
			MaxPromptChars:   orDefault(l.MaxPromptChars, DefaultClassifierMaxPromptChars),
			MaxItemChars:     orDefault(l.MaxItemChars, DefaultClassifierMaxItemChars),
			MaxItemsPerRun:   orDefault(l.MaxItemsPerRun, DefaultClassifierMaxItemsPerRun),
			MaxParallel:      orDefault(l.MaxParallel, DefaultClassifierMaxParallel),
			MaxActivityHours: orDefault(l.MaxActivityHours, DefaultClassifierMaxActivityHours),
			MinEverySeconds:  orDefault(l.MinEverySeconds, DefaultClassifierMinEverySeconds),
		},
	}
}

// Validate refuses a negative value, naming every one.
func (c ClassifiersConfig) Validate() error {
	var errs []error
	l := c.Limits
	for _, f := range []struct {
		what  string
		value int
	}{
		{"setting processed_days", c.ProcessedDays},
		{"maximum for max_rules", l.MaxRules},
		{"maximum for max_prompt_chars", l.MaxPromptChars},
		{"maximum for max_item_chars", l.MaxItemChars},
		{"maximum for max_items_per_run", l.MaxItemsPerRun},
		{"maximum for max_parallel", l.MaxParallel},
		{"maximum for max_activity_hours", l.MaxActivityHours},
		{"minimum for min_every_seconds", l.MinEverySeconds},
	} {
		if f.value < 0 {
			errs = append(errs, errors.New("The classifier "+f.what+" must be 0 (the default) or more."))
		}
	}
	return errors.Join(errs...)
}

// AgentClassifiersConfig is an agent's classifiers block: the switch that
// gives it the classifier tools and runs its classifiers, and the one model
// every classifier it owns uses (set by the operator; agents cannot change
// it).
type AgentClassifiersConfig struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model,omitempty"`
}

// ClassifiersEnabled reports whether the agent's classifiers switch is on.
func (a *AgentConfig) ClassifiersEnabled() bool {
	return a != nil && a.Classifiers != nil && a.Classifiers.Enabled
}

// ClassifierModel returns the agent's classifier model, trimmed; "" when none
// is set.
func (a *AgentConfig) ClassifierModel() string {
	if a == nil || a.Classifiers == nil {
		return ""
	}
	return strings.TrimSpace(a.Classifiers.Model)
}

// agentModelList is the list of models an agent runs on: its own models, else
// the default models.
func (c *Config) agentModelList(a *AgentConfig) []string {
	if len(a.Models) > 0 {
		return a.Models
	}
	return c.Agents.Defaults.Models
}

// firstAgentModel is the first non-empty entry of the agent's model list, or
// "" when it has none.
func (c *Config) firstAgentModel(a *AgentConfig) string {
	for _, m := range c.agentModelList(a) {
		if m = strings.TrimSpace(m); m != "" {
			return m
		}
	}
	return ""
}

// ClassifierModelUsable reports whether model is one of the agent's models
// (its own models, else the default models), whatever its classifiers
// switch says.
func (c *Config) ClassifierModelUsable(a *AgentConfig, model string) bool {
	model = strings.TrimSpace(model)
	return a != nil && model != "" &&
		slices.ContainsFunc(c.agentModelList(a), func(m string) bool { return strings.TrimSpace(m) == model })
}

// ClassifierModelProblem is an agent whose classifiers are on and whose
// classifier model is not usable: none is set (Missing), or Model is not one
// of the agent's models.
type ClassifierModelProblem struct {
	Agent   string // the configured agent id
	Model   string // the classifier model ("" when Missing)
	Missing bool
}

// ClassifierModelProblems lists every agent with the classifiers switch on
// whose classifier model is missing or is not one of its models (its own
// models, else the default models). Its classifiers fail every run until
// the operator picks a usable model.
func (c *Config) ClassifierModelProblems() []ClassifierModelProblem {
	var out []ClassifierModelProblem
	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		if !a.ClassifiersEnabled() {
			continue
		}
		model := a.ClassifierModel()
		switch {
		case model == "":
			out = append(out, ClassifierModelProblem{Agent: a.ID, Missing: true})
		case !c.ClassifierModelUsable(a, model):
			out = append(out, ClassifierModelProblem{Agent: a.ID, Model: model})
		}
	}
	return out
}

// newClassifierModelProblems is Store.Update's refusal of a classifier model
// the agent cannot use: one error per agent whose problem next introduces
// (same agent and model). A missing model is not refused here:
// completeClassifierModels fills it on every save.
func newClassifierModelProblems(before, next *Config) []error {
	key := func(p ClassifierModelProblem) string { return NormalizeAgentID(p.Agent) + "|" + p.Model }
	return newProblems(before.ClassifierModelProblems(), next.ClassifierModelProblems(), key,
		func(p ClassifierModelProblem) error {
			if p.Missing {
				return nil
			}
			return errors.New("Agent " + next.agentName(p.Agent) + " cannot use model " + p.Model +
				" for its classifiers; pick one of its models.")
		})
}

// completeClassifierModels gives every agent whose classifiers switch is on
// and that names no classifier model (a save that turns the switch on, or
// one that clears the model) the agent's first model at this moment. An
// agent with no model at all is refused only when this save brings it about
// (as for the other rules, one already in the file does not block an
// unrelated save). A human agent is left alone: it cannot run classifiers
// (HumanClassifiers).
func completeClassifierModels(before, next *Config) []error {
	var errs []error
	for i := range next.Agents.List {
		a := &next.Agents.List[i]
		if !a.ClassifiersEnabled() || a.ClassifierModel() != "" {
			continue
		}
		if _, human := next.HumanModelOf(a); human {
			continue
		}
		model := next.firstAgentModel(a)
		if model == "" {
			if was := before.AgentByID(a.ID); was.ClassifiersEnabled() && was.ClassifierModel() == "" && before.firstAgentModel(was) == "" {
				continue
			}
			errs = append(errs, errors.New("Agent "+a.DisplayName()+
				" has no model for its classifiers; set agents.list[].classifiers.model."))
			continue
		}
		a.Classifiers.Model = model
	}
	return errs
}

// agentName is the display name of the configured agent id, or the id when
// no agent has it.
func (c *Config) agentName(id string) string {
	if a := c.AgentByID(id); a != nil {
		return a.DisplayName()
	}
	return id
}
