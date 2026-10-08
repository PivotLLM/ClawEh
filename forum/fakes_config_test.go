// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Test helpers of seam (a), prefixed cfgt so they cannot collide with the
// helpers of the other seams in the same package.

// cfgtExampleJSON is the configuration example of spec §7 (Alice takes part
// as herself, Bob as a clone, the chair and the editor are fresh).
const cfgtExampleJSON = `{
  "version": 1, "name": "design-review",
  "brief": {"purpose": "Expose weaknesses before adoption.",
            "task": "Review the proposal and recommend concrete changes."},
  "sources": {"report": {"decode": "text",
    "inline": "Proposal: replace the help desk with an AI assistant."}},
  "participants": {
    "alice":    {"agent": "alice", "instructions": "Challenge cost, migration and reversibility."},
    "bob":      {"clone": "bob", "instructions": "Challenge trust assumptions and failure containment."},
    "chair":    {"model": "default", "instructions": "End repetitive discussion; preserve unresolved objections."},
    "editor":   {"model": "default", "instructions": "Prioritize supported findings. Preserve dissent."}
  },
  "schemas": {"findings": {
    "type": "object", "required": ["agreements", "disagreements"],
    "additionalProperties": false,
    "properties": {
      "agreements": {"type": "array", "items": {"type": "string"}},
      "disagreements": {"type": "array", "items": {"type": "string"}}
    }
  }},
  "limits": {"max_calls": 30, "max_duration_seconds": 1800,
    "call_timeout_seconds": 300, "max_attempts_per_turn": 2, "max_parallel_calls": 2},
  "layers": [
    {"id": "review", "participants": ["alice", "bob"],
     "instructions": "Identify specific agreements and objections.",
     "inputs": [{"from": "source:report"}],
     "delivery": "after_round", "max_rounds": 1,
     "output": {"format": "json", "schema": "findings"}},
    {"id": "debate", "participants": ["alice", "bob"],
     "instructions": "Challenge findings; explain what would resolve objections.",
     "inputs": [{"from": "source:report"}, {"from": "layer:review"}],
     "delivery": "per_turn", "max_rounds": 3, "output": {"format": "text"},
     "moderator": {"participant": "chair", "after_round": 1, "every_rounds": 1,
                   "inputs": [{"from": "source:report"}]}},
    {"id": "report", "participants": ["editor"],
     "instructions": "Write prioritized changes, dissent and verification needs.",
     "inputs": [{"from": "source:report"}, {"from": "layer:review"},
                {"from": "layer:debate", "optional": true}],
     "delivery": "after_round", "max_rounds": 1, "output": {"format": "markdown"}}
  ],
  "result_layers": ["report"]
}`

// cfgtExample decodes a fresh copy of the §7 example.
func cfgtExample(t *testing.T) *Config {
	t.Helper()
	cfg, err := Decode([]byte(cfgtExampleJSON))
	if err != nil {
		t.Fatalf("decode example: %v", err)
	}
	return cfg
}

// cfgtWantIssue fails unless err is a *ValidationError with an issue at
// path whose message contains every one of substrs.
func cfgtWantIssue(t *testing.T, err error, path string, substrs ...string) {
	t.Helper()
	ve, ok := errors.AsType[*ValidationError](err)
	if !ok {
		t.Fatalf("want *ValidationError with an issue at %q, got %v", path, err)
	}
	for _, is := range ve.Issues {
		if is.Path != path {
			continue
		}
		matched := true
		for _, s := range substrs {
			matched = matched && strings.Contains(is.Message, s)
		}
		if matched {
			return
		}
	}
	t.Fatalf("no issue at %q containing %q; issues:\n%v", path, substrs, ve)
}

// cfgtRaw is a JSON literal as json.RawMessage.
func cfgtRaw(s string) json.RawMessage { return json.RawMessage(s) }

// cfgtStrs returns a pointer to the slice of its arguments.
func cfgtStrs(v ...string) *[]string { return &v }

// cfgtAgents is a fake Agents for preflight. allowed lists the targets the
// launcher may name; exists the registered agents; models each agent's
// model list. errOn makes the named method fail.
type cfgtAgents struct {
	allowed map[string]bool
	exists  map[string]bool
	models  map[string][]ModelInfo
	errOn   string

	mu    sync.Mutex
	calls []string
}

var errCfgtHost = errors.New("host unavailable")

// cfgtNewAgents builds the fake for the §7 example with the launcher
// "launcher": Alice and Bob exist and are allowed; every agent has the
// models "default" and "large".
func cfgtNewAgents() *cfgtAgents {
	two := []ModelInfo{{Name: "default", Provider: "p", Protocol: "openai"}, {Name: "large", Provider: "p", Protocol: "openai"}}
	return &cfgtAgents{
		allowed: map[string]bool{"alice": true, "bob": true},
		exists:  map[string]bool{"alice": true, "bob": true, "launcher": true},
		models:  map[string][]ModelInfo{"launcher": two, "alice": two, "bob": two},
	}
}

func (a *cfgtAgents) record(call string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, call)
}

func (a *cfgtAgents) called(call string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Contains(a.calls, call)
}

func (a *cfgtAgents) Exists(_ context.Context, agentID string) (bool, error) {
	a.record("Exists " + agentID)
	if a.errOn == "Exists" {
		return false, errCfgtHost
	}
	return a.exists[agentID], nil
}

func (a *cfgtAgents) MayTarget(_ context.Context, launcherID, targetID string) (bool, error) {
	a.record("MayTarget " + launcherID + " " + targetID)
	if a.errOn == "MayTarget" {
		return false, errCfgtHost
	}
	return a.allowed[targetID], nil
}

func (a *cfgtAgents) Models(_ context.Context, agentID string) ([]ModelInfo, error) {
	a.record("Models " + agentID)
	if a.errOn == "Models" {
		return nil, errCfgtHost
	}
	return a.models[agentID], nil
}

func (a *cfgtAgents) CreateClone(context.Context, CloneSpec) (string, error) {
	return "", errors.New("preflight must not create agents")
}

func (a *cfgtAgents) CreateFresh(context.Context, FreshSpec) (string, error) {
	return "", errors.New("preflight must not create agents")
}

func (a *cfgtAgents) Delete(context.Context, string, string) error {
	return errors.New("preflight must not delete agents")
}

func (a *cfgtAgents) Touch(context.Context, string, string) error {
	return errors.New("preflight must not touch agents")
}

// cfgtEnv is a PreflightEnv over agents with the real schema adapter.
func cfgtEnv(agents Agents) PreflightEnv {
	return PreflightEnv{Launcher: "launcher", Agents: agents}
}
