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
// Every tool is session-scoped: it operates on the per-session .cogmem.db
// resolved from the workspace and ToolCall.Session. Cognitive memory is ON by
// default: every agent gets these tools unless its `cogmem` flag is false.
package cogmem

import (
	cogmemtools "github.com/PivotLLM/cogmem/tools"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
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
	cd, _ := deps.Host.(tools.ToolDeps)
	workspace := cd.Workspace
	// Config is needed to validate a memory's file attachment against the agent's
	// read permissions; nil during deps-free enumeration, where no handler runs.
	cfg, _ := deps.Cfg.(*config.Config)

	host := cogmemtools.Host{
		Workspace: workspace,
		CheckAttachment: func(ref string) (int64, error) {
			return cogmemhost.Check(cfg, workspace, ref)
		},
		// Read the trigger at call time, not registration time: the gateway
		// installs it after the tool providers are registered.
		Consolidate: func(agentID, sessionKey string) {
			if consolidateTrigger != nil {
				consolidateTrigger(agentID, sessionKey)
			}
		},
	}
	// Sub-agents get the full cogmem toolset, including writes. Their memory is a
	// snapshot copied onto the sub-agent session's own DB and deleted after the
	// run (see agent.runSubagentTask), so any writes land on that throwaway copy
	// and the primary's memory is never touched — the isolation, not tool
	// withholding, is what protects it.
	return cogmemtools.Definitions(host)
}
