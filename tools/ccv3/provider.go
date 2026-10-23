// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package ccv3 is the ccv3 tool: one tool, published under the bare name
// "ccv3", through which an agent manages the character-card library (list,
// get, create, update, rename, copy, delete). It is an all-or-nothing suite
// gated by the agent's `ccv3` switch (default off).
package ccv3

import (
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
)

// Suite is the suite's name: the per-agent switch (`ccv3`) and the tool's
// name.
const Suite = "ccv3"

// GlobalProvider mounts the ccv3 tool. Register it with
// tools.BareNamespacedProvider so the tool is published as "ccv3".
var GlobalProvider globalCCv3Provider

type globalCCv3Provider struct{}

func (globalCCv3Provider) Namespace() string { return Suite }
func (globalCCv3Provider) Description() string {
	return "Character cards: manage the character-card library"
}
func (globalCCv3Provider) Available(any) (bool, string) { return true, "" }

// Suite marks the tool as a suite gated by the agent's `ccv3` switch.
func (globalCCv3Provider) Suite() string { return Suite }

// RegisterTools returns the ccv3 tool when the agent's switch is on. A clone
// acts as its source (deps.AgentID), so a clone of an agent with the switch,
// a forum participant included, has the tool.
func (globalCCv3Provider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	c, ok := deps.Cfg.(*config.Config)
	if !ok || c == nil {
		// Enumeration pass: the suite is one toggle, not catalogue entries.
		return nil
	}
	if !c.AgentSuiteEnabled(deps.AgentID, Suite) {
		return nil
	}
	return []global.ToolDefinition{definition(&tool{cfg: c, agentID: deps.AgentID})}
}
