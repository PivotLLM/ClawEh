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
// Every tool operates on the agent's one memory, <state dir>/cogmem: the
// workspace for a config agent, the agent's own directory for a temporary one
// (a sub-agent clone works on its snapshot of its source's memory there, and
// never reaches the source's). Cognitive memory is ON by default: every agent
// gets these tools unless its `cogmem` flag is false.
package cogmem

import (
	cogmemtools "github.com/PivotLLM/cogmem/tools"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
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

	host := cogmemtools.Host{
		Dir:       cogmemhost.Dir(cd.EffectiveStateDir()),
		Workspace: workspace,
		CheckAttachment: func(ref string) (int64, error) {
			return cogmemhost.Check(cfg, workspace, ref)
		},
	}
	// An ephemeral memory (a sub-agent's snapshot of its source's memory) is
	// never consolidated: nothing is observed into it and it is deleted with
	// the sub-agent.
	if !cd.EphemeralMemory {
		// Read the trigger at call time, not registration time: the gateway
		// installs it after the tool providers are registered.
		host.Consolidate = func(agentID, sessionKey string) {
			if consolidateTrigger != nil {
				consolidateTrigger(agentID, sessionKey)
			}
		}
	}
	defs := cogmemtools.Definitions(host)
	if host.Dir == "" {
		return defs
	}
	// The module's handlers create the store with the umask default; make the
	// directory and database owner-only before each one runs.
	for i := range defs {
		inner := defs[i].Handler
		if inner == nil {
			continue
		}
		dir := host.Dir
		defs[i].Handler = func(call *global.ToolCall) (*global.Result, error) {
			if err := cogmemhost.EnsurePrivate(dir); err != nil {
				logger.WarnCF("cogmem", "Failed to create the memory store privately",
					map[string]any{"path": dir, "error": err.Error()})
			}
			return inner(call)
		}
	}
	return defs
}
