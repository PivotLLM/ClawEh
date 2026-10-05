// ClawEh - Cognitive Memory
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package cogmem mounts the cognitive-memory tools from the cogmem module
// (github.com/PivotLLM/cogmem/tools) as a ClawEh tool provider. The module
// defines the tools with BARE names ("domain_get", "memory_create", ...); the
// aggregator publishes them under the "cogmem" namespace as
// "cogmem_domain_get" and so on.
//
// Every tool operates on the agent's one memory, <workspace>/cogmem, shared by
// all of its sessions, except in a sub-agent session, where it operates on the
// sub-agent's throwaway snapshot (cogmemhost.SubagentDir). Cognitive memory is
// ON by default: every agent gets these tools unless its `cogmem` flag is false.
package cogmem

import (
	cogmemtools "github.com/PivotLLM/cogmem/tools"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
)

// GlobalProvider exposes the cognitive-memory tools through the global layer.
var GlobalProvider globalCogmemProvider

type globalCogmemProvider struct{}

// Namespace/Description/Available satisfy global.HostMeta. The tools are always
// available (the store is created on demand); access is gated per-agent via the
// cogmem suite flag.
func (globalCogmemProvider) Namespace() string { return "cogmem" }

func (globalCogmemProvider) Description() string {
	return "Cognitive memory: durable domains and hooks"
}

func (globalCogmemProvider) Available(cfg any) (bool, string) { return true, "" }

// Suite marks cognitive memory as an all-or-nothing tool suite gated by the
// per-agent `cogmem` flag (default ON), not the per-tool allowlist.
func (globalCogmemProvider) Suite() string { return "cogmem" }

// consolidateTrigger is installed by the gateway once the background
// consolidation manager is running. Nil until then, in which case the
// consolidate tool reports that no worker is available.
var consolidateTrigger func(agentID, sessionKey string)

// SetConsolidateTrigger installs the (non-blocking) consolidation trigger.
func SetConsolidateTrigger(fn func(agentID, sessionKey string)) { consolidateTrigger = fn }

func (globalCogmemProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	// Recover Claw's rich, strongly-typed dependencies. A deps-free enumeration
	// (Describe) passes a zero Deps, so cd is the zero ToolDeps and workspace is
	// empty — handlers guard on an empty session/workspace and never touch disk
	// during cataloguing.
	var cd tools.ToolDeps
	if v, ok := deps.Host.(tools.ToolDeps); ok {
		cd = v
	}
	workspace := cd.Workspace
	// Config is needed to validate a memory's file attachment against the agent's
	// read permissions; nil during deps-free enumeration, where no handler runs.
	var cfg *config.Config
	if v, ok := deps.Cfg.(*config.Config); ok {
		cfg = v
	}

	checkAttachment := func(ref string) (int64, error) {
		return cogmemhost.Check(cfg, workspace, ref)
	}
	primary := cogmemtools.Definitions(cogmemtools.Host{
		Dir:             cogmemhost.Dir(workspace),
		Workspace:       workspace,
		CheckAttachment: checkAttachment,
		// Read the trigger at call time, not registration time: the gateway
		// installs it after the tool providers are registered.
		Consolidate: func(agentID, sessionKey string) {
			if consolidateTrigger != nil {
				consolidateTrigger(agentID, sessionKey)
			}
		},
	})
	// subagentDefinitions binds the tools to a sub-agent's snapshot directory.
	// Consolidate is left unset: the snapshot is ephemeral (nothing is observed
	// into its inbox, see agent.wireCognitiveMemory) and deleted after the run,
	// and the gateway's trigger would consolidate the primary's memory instead.
	subagentDefinitions := func(sessionKey string) []global.ToolDefinition {
		return cogmemtools.Definitions(cogmemtools.Host{
			Dir:             cogmemhost.SubagentDir(workspace, sessionKey),
			Workspace:       workspace,
			CheckAttachment: checkAttachment,
		})
	}

	// Sub-agents get the full cogmem toolset, including writes, but on a
	// snapshot of the memory copied into their own directory and deleted after
	// the run (see agent.runSubagentTask). The module binds each handler to one
	// directory while this provider is shared by every session of the agent, so
	// the directory is chosen per call from the call's session key: a sub-agent
	// session reads and writes its snapshot and never the primary's memory.
	defs := make([]global.ToolDefinition, len(primary))
	for i, d := range primary {
		primaryHandler := d.Handler
		d.Handler = func(call *global.ToolCall) (*global.Result, error) {
			if workspace == "" || !routing.IsSubagentSessionKey(call.Session) {
				return primaryHandler(call)
			}
			// Definitions returns the same tools in the same order for any Host.
			return subagentDefinitions(call.Session)[i].Handler(call)
		}
		defs[i] = d
	}
	return defs
}
