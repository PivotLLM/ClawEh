// ClawEh - Cognitive Memory
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package cogmem exposes the cognitive-memory store (pkg/cogmem/store) as a
// transport-neutral tool provider with BARE names ("domain_get", "hook_create",
// ...). The aggregator mounts the provider under the "cogmem" namespace, so the
// published tool names are "cogmem_domain_get", "cogmem_hook_create", etc.
// Names follow the object_verb convention (domain_*, hook_*, memory_*).
//
// Every tool is session-scoped: it operates on the per-session .cogmem.db
// resolved from the workspace and ToolCall.Session. Cognitive memory is ON by
// default (DefaultAllow true): every agent gets these tools unless its tool
// allowlist explicitly excludes them — i.e. cognitive memory is opt-OUT.
package cogmem

import (
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the cognitive-memory tools through the global layer.
var GlobalProvider globalCogmemProvider

type globalCogmemProvider struct{}

// Namespace/Description/Available satisfy global.HostMeta. The tools are always
// available (the store is created on demand); access is gated per-agent via the
// tool allowlist.
func (globalCogmemProvider) Namespace() string { return "cogmem" }
func (globalCogmemProvider) Description() string {
	return "Cognitive memory: durable domains and hooks"
}

func (globalCogmemProvider) Available(cfg any) (bool, string) { return true, "" }

// consolidateTrigger is an optional package-level hook the Phase 3 consolidation
// worker can install via SetConsolidateTrigger. When nil, the consolidate tool
// reports that the worker is not yet running.
var consolidateTrigger func(agentID, sessionKey string)

// SetConsolidateTrigger installs the (non-blocking) consolidation trigger. Phase
// 3 wires the worker here; until then the consolidate tool degrades gracefully.
func SetConsolidateTrigger(fn func(agentID, sessionKey string)) { consolidateTrigger = fn }

func (globalCogmemProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	// Recover Claw's rich, strongly-typed dependencies. A deps-free enumeration
	// (Describe) passes a zero Deps, so cd is the zero ToolDeps and workspace is
	// empty — handlers guard on an empty session/workspace and never touch disk
	// during cataloguing.
	cd, _ := deps.Host.(tools.ToolDeps)
	workspace := cd.Workspace

	def := func(name, desc string, params []global.Parameter, allow bool, h handlerFunc) global.ToolDefinition {
		return global.ToolDefinition{
			Name:          name,
			Description:   desc,
			Parameters:    params,
			Category:      "memory",
			SessionScoped: true,
			DefaultAllow:  global.Allow(allow),
			Handler:       wrap(workspace, h),
		}
	}

	return []global.ToolDefinition{
		def("domain_get",
			"Load a memory domain by id together with its active hooks (rendered as readable text including each hook id).",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "Domain id (e.g. d3K9P)."},
			}, true, getDomain),

		def("hook_search",
			"Search active memory hooks by a case-insensitive substring of their text.",
			[]global.Parameter{
				{Name: "query", Type: "string", Required: true, Description: "Substring to match against hook text."},
				{Name: "limit", Type: "integer", Required: false, Description: "Max results (default 20)."},
			}, true, search),

		def("domain_list",
			"List memory domains (id, name, summary, status). Optionally filter by status.",
			[]global.Parameter{
				{Name: "status", Type: "string", Required: false, Description: "Filter by status.",
					Enum: []any{"active", "review", "archived"}},
			}, true, listDomains),

		def("memory_explain",
			"Summarize the status, source, and evidence of a domain or hook id.",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "A domain id (d…) or hook id (h…)."},
			}, true, explain),

		def("hook_create",
			"Record a durable memory hook (a preference, rule, fact, project_state, workflow, or lesson). With NO domain_id and NO domain_hint it records to your always-on 'general' domain (global rules/preferences/facts that should always be in context). Give a domain_hint to create/use a project domain, or a domain_id to target a specific one.",
			[]global.Parameter{
				{Name: "domain_id", Type: "string", Required: false, Description: "Target domain id. If omitted and no domain_hint is given, records to the always-on general domain."},
				{Name: "domain_hint", Type: "string", Required: false, Description: "Name for a new project domain when domain_id is not given (omit to use the general domain)."},
				{Name: "kind", Type: "string", Required: true, Description: "Hook kind.",
					Enum: []any{"preference", "rule", "fact", "project_state", "workflow", "lesson"}},
				{Name: "text", Type: "string", Required: true, Description: "The memory content to store."},
				{Name: "confidence", Type: "number", Required: false, Description: "Confidence 0..1 (default 0.9)."},
				{Name: "status", Type: "string", Required: false, Description: "active (default) or review.",
					Enum: []any{"active", "review"}},
			}, true, remember),

		def("domain_update",
			"Patch a domain under optimistic concurrency (requires the current expected_version).",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "Domain id."},
				{Name: "expected_version", Type: "integer", Required: true, Description: "Version you last read; rejected if stale."},
				{Name: "set_summary", Type: "string", Required: false, Description: "Replace the domain summary."},
				{Name: "set_blockers", Type: "array", Items: "string", Required: false, Description: "Replace the blockers list."},
				{Name: "set_next_actions", Type: "array", Items: "string", Required: false, Description: "Replace the next-actions list."},
				{Name: "set_constraints", Type: "array", Items: "string", Required: false, Description: "Replace the constraints list."},
				{Name: "set_triggers", Type: "string", Required: false, Description: "Replace the tool triggers: a comma-separated list of tool-name substrings (e.g. \"google_gmail,microsoft365_mail\"). This domain is auto-loaded into context whenever you use a tool whose name contains any token (case- and underscore-insensitive). Empty string clears triggers."},
			}, true, updateDomain),

		def("hook_retire",
			"Retire a hook (it stays in the audit trail but leaves active memory).",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "Hook id."},
				{Name: "reason", Type: "string", Required: true, Description: "Why it is being retired."},
			}, true, retireHook),

		def("domain_create",
			"Create a new memory domain and return its assigned id.",
			[]global.Parameter{
				{Name: "type", Type: "string", Required: true, Description: "Domain type.",
					Enum: []any{"project", "workflow", "repo"}},
				{Name: "name", Type: "string", Required: true, Description: "Domain name."},
				{Name: "summary", Type: "string", Required: false, Description: "Optional one-line summary."},
				{Name: "triggers", Type: "string", Required: false, Description: "Optional tool triggers: a comma-separated list of tool-name substrings (e.g. \"google_gmail,microsoft365_mail\" or \"system\"). This domain is auto-loaded into context whenever you use a tool whose name contains any token. Matching ignores case and treats _ and __ the same."},
			}, true, createDomain),

		def("domain_archive",
			"Archive a domain so it is no longer used in prompting.",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "Domain id."},
			}, true, archiveDomain),

		def("hook_forget",
			"Retire all active hooks matching a query (optionally limited to one domain). Reports how many were retired.",
			[]global.Parameter{
				{Name: "query", Type: "string", Required: true, Description: "Substring to match against active hook text."},
				{Name: "domain_id", Type: "string", Required: false, Description: "Restrict to this domain."},
			}, true, forget),

		def("memory_consolidate",
			"Request a (non-blocking) memory-consolidation pass. Returns immediately.",
			[]global.Parameter{
				{Name: "scope", Type: "string", Required: false, Description: "Optional scope hint (currently advisory)."},
			}, true, consolidate),

		def("memory_status",
			"Report cognitive-memory health: database path, last consolidation run, and pending-hook count.",
			nil, true, status),
	}
}
